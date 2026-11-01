// Command bashtransfer is the relay server: it receives an upload, replies with the
// download commands and forwards the body to the first receiver, without saving it to disk.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"bashtransfer/internal/config"
	"bashtransfer/internal/relay"
	"bashtransfer/internal/server"
)

const shutdownTimeout = 10 * time.Second

func main() {
	healthcheck := flag.Bool("healthcheck", false, "checks the local server (BASHT_LISTEN) and exits 0 if it responds")
	flag.Parse()
	if *healthcheck {
		if err := checkHealth(os.Getenv("BASHT_LISTEN")); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	logger := newLogger(os.Stderr, cfg.LogFormat, cfg.LogLevel)
	logger.Info("server_start",
		"listen", cfg.Listen,
		"public_url", cfg.PublicURL,
		"auth", cfg.AuthUser != "",
		"wait_timeout", cfg.WaitTimeout.String(),
		"idle_timeout", cfg.IdleTimeout.String(),
		"sender_grace", cfg.SenderGrace.String(),
		"max_sessions", cfg.MaxSessions,
		"buf_size", cfg.BufSize,
		"log_level", cfg.LogLevel.String(),
		"go", runtime.Version(),
		"gomaxprocs", runtime.GOMAXPROCS(0))

	reg := relay.NewRegistry(cfg.MaxSessions, cfg.WaitTimeout)
	reg.SetSenderGrace(cfg.SenderGrace)
	opts := server.Options{PublicURL: cfg.PublicURL, IdleTimeout: cfg.IdleTimeout, Logger: logger}
	if cfg.AuthUser != "" {
		opts.Authorize = server.BasicAuth(cfg.AuthUser, cfg.AuthPassword)
	}
	handler := server.New(reg, relay.NewCopier(cfg.BufSize), opts)
	// No ReadTimeout or WriteTimeout: they would block the streaming relay.
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    16 << 10,
		// Internal net/http errors (accept, malformed headers): http_error event.
		ErrorLog: slog.NewLogLogger(logger.With("component", "net/http").Handler(), slog.LevelWarn),
		// Lets the handler detect a sender that dropped while waiting for the receiver.
		ConnContext: server.ConnContext,
		// Plaintext HTTP/1.1 and HTTP/2 (h2c): an HTTP/1.1 proxy (Apache mod_proxy_http, nginx http)
		// sends the whole body before reading the response and blocks the sender, which would not see
		// the commands. With h2c (Apache: ProxyPass h2c://) request and response flow together.
		Protocols: protocols(),
	}
	// On shutdown the listener closes: no receiver can arrive anymore, so waiting senders are
	// closed immediately and the grace period is kept for transfers in progress.
	srv.RegisterOnShutdown(reg.ExpireWaiting)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Listen before logging: server_listening appears only once the port accepts connections, and a
	// busy address makes run fail immediately.
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	errc := make(chan error, 1)
	go func() {
		logger.Info("server_listening", "addr", ln.Addr().String())
		errc <- srv.Serve(ln)
	}()

	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	// After the first signal the default behavior is restored: another Ctrl-C terminates the
	// process immediately instead of waiting for shutdownTimeout.
	stop()

	start := time.Now()
	logger.Info("shutdown_start", "sessions", reg.Len(), "grace", shutdownTimeout.String())
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	forced := false
	if err := srv.Shutdown(shutdownCtx); err != nil {
		forced = true
		logger.Warn("shutdown_forced", "err", err.Error(), "sessions", reg.Len())
		_ = srv.Close()
	}
	logger.Info("shutdown_done", "forced", forced,
		"duration_s", math.Round(time.Since(start).Seconds()*1000)/1000)
	return nil
}

// newLogger creates the server logger: text (logfmt) or json, one line per event, to w.
func newLogger(w io.Writer, format string, level slog.Level) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}
	if format == config.LogJSON {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}

// healthURL derives the local URL from the listen address: an empty or unspecified host
// becomes the loopback.
func healthURL(listen string) (string, error) {
	if listen == "" {
		listen = ":8080"
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("BASHT_LISTEN invalid: %q: %w", listen, err)
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/", nil
}

// checkHealth performs GET / on the local server. No proxy and no external network.
func checkHealth(listen string) error {
	u, err := healthURL(listen)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
	resp, err := client.Get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

// protocols enables HTTP/1.1 and HTTP/2 without TLS on the same port.
func protocols() *http.Protocols {
	p := new(http.Protocols)
	p.SetHTTP1(true)
	p.SetUnencryptedHTTP2(true)
	return p
}
