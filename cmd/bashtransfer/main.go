// Command bashtransfer è il server di relay: riceve un upload, risponde con i comandi
// di download e inoltra il body al primo ricevente, senza salvarlo su disco.
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
	healthcheck := flag.Bool("healthcheck", false, "controlla il server locale (BASHT_LISTEN) ed esce 0 se risponde")
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
		"max_sessions", cfg.MaxSessions,
		"buf_size", cfg.BufSize,
		"log_level", cfg.LogLevel.String(),
		"go", runtime.Version(),
		"gomaxprocs", runtime.GOMAXPROCS(0))

	reg := relay.NewRegistry(cfg.MaxSessions, cfg.WaitTimeout)
	opts := server.Options{PublicURL: cfg.PublicURL, IdleTimeout: cfg.IdleTimeout, Logger: logger}
	if cfg.AuthUser != "" {
		opts.Authorize = server.BasicAuth(cfg.AuthUser, cfg.AuthPassword)
	}
	handler := server.New(reg, relay.NewCopier(cfg.BufSize), opts)
	// Nessun ReadTimeout né WriteTimeout: bloccherebbero il relay in streaming.
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    16 << 10,
		// Errori interni di net/http (accept, header malformati): evento http_error.
		ErrorLog: slog.NewLogLogger(logger.With("component", "net/http").Handler(), slog.LevelWarn),
		// Serve all'handler per rilevare un mittente caduto mentre attende il ricevente.
		ConnContext: server.ConnContext,
	}
	// All'arresto il listener chiude: nessun ricevente può più arrivare, quindi i mittenti in
	// attesa vengono chiusi subito e il periodo di grazia resta ai trasferimenti in corso.
	srv.RegisterOnShutdown(reg.ExpireWaiting)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Listen prima del log: server_listening compare solo quando la porta accetta connessioni, e un
	// indirizzo occupato fa fallire run subito.
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
	// Dal secondo segnale torna il comportamento predefinito: un altro Ctrl-C termina subito il
	// processo invece di attendere shutdownTimeout.
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

// newLogger crea il logger del server: text (logfmt) o json, una riga per evento, su w.
func newLogger(w io.Writer, format string, level slog.Level) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}
	if format == config.LogJSON {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}

// healthURL ricava l'URL locale dall'indirizzo di ascolto: un host vuoto o non specificato
// diventa il loopback.
func healthURL(listen string) (string, error) {
	if listen == "" {
		listen = ":8080"
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("BASHT_LISTEN non valida: %q: %w", listen, err)
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/", nil
}

// checkHealth esegue GET / sul server locale. Nessun proxy e nessuna rete esterna.
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
		return fmt.Errorf("stato %d", resp.StatusCode)
	}
	return nil
}
