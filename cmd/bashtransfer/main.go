// Command bashtransfer è il server di relay: riceve un upload, risponde con i comandi
// di download e inoltra il body al primo ricevente, senza salvarlo su disco.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"bashtransfer/internal/config"
	"bashtransfer/internal/relay"
	"bashtransfer/internal/server"
)

const shutdownTimeout = 10 * time.Second

func main() {
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
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	reg := relay.NewRegistry(cfg.MaxSessions, cfg.WaitTimeout)
	handler := server.New(reg, relay.NewCopier(cfg.BufSize), server.Options{PublicURL: cfg.PublicURL})
	// Nessun ReadTimeout né WriteTimeout: bloccherebbero il relay in streaming.
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, 1)
	go func() {
		logger.Info("in ascolto", "addr", cfg.Listen)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	logger.Info("arresto in corso")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("shutdown forzato", "err", err)
		_ = srv.Close()
	}
	return nil
}
