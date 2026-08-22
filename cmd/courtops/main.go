// Command courtops runs the CourtOps web application.
//
// One process serves the organizer controls, the public court board and the
// small JSON API. It applies database migrations on start-up, then serves until
// it is asked to stop, at which point it finishes the requests already in
// flight before closing the database pool.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nicholashu/courtops/internal/config"
	"github.com/nicholashu/courtops/internal/store"
	"github.com/nicholashu/courtops/internal/web"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	migrateOnly := flag.Bool("migrate-only", false, "apply database migrations and exit")
	printVersion := flag.Bool("version", false, "print the build version and exit")
	flag.Parse()

	if *printVersion {
		fmt.Println(version)
		return
	}

	if err := run(*migrateOnly); err != nil {
		slog.Error("courtops failed to start", "error", err)
		os.Exit(1)
	}
}

func run(migrateOnly bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := newLogger(cfg)
	slog.SetDefault(log)

	// A signal now cancels start-up as well as serving, so a hung database
	// connection during a deploy can still be interrupted.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	startupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	st, err := store.Open(startupCtx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer st.Close()
	st.RatingsEnabled = cfg.RatingsEnabled

	applied, err := st.Migrate(startupCtx)
	if err != nil {
		return fmt.Errorf("migrations: %w", err)
	}
	if len(applied) > 0 {
		log.Info("migrations applied", "count", len(applied), "files", applied)
	} else {
		log.Info("database schema is current")
	}
	if migrateOnly {
		return nil
	}

	srv, err := web.New(cfg, st, log, version)
	if err != nil {
		return err
	}

	httpServer := &http.Server{
		Addr:    cfg.Addr,
		Handler: srv,
		// Caddy terminates TLS in front of this process; these timeouts are
		// about a slow or stuck client, not about the proxy.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       90 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	log.Info("courtops starting",
		"version", version,
		"addr", cfg.Addr,
		"env", cfg.Env,
		"read_only", cfg.ReadOnly,
		"ratings", cfg.RatingsEnabled,
		"google_login", cfg.Google.Configured(),
		"organizers", len(cfg.AllowedOrganizers),
	)
	if !cfg.IsProduction() && cfg.DevOrganizer != "" {
		log.Warn("development sign-in is enabled", "organizer", cfg.DevOrganizer)
	}

	serveErr := make(chan error, 1)
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		log.Info("shutdown signal received; finishing in-flight requests")
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancelShutdown()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		// Requests that did not finish in time are cut off here. Every write is
		// a single transaction, so an interrupted request leaves the database
		// in a legal state: the transaction either committed or rolled back.
		log.Error("graceful shutdown timed out", "error", err)
		return err
	}
	log.Info("stopped cleanly")
	return nil
}

// newLogger writes structured JSON in production and readable text locally.
func newLogger(cfg config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	if cfg.IsProduction() {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}
