// Command api serves the homelab inventory HTTP API.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/WizardOfMeh/inventory-api/internal/config"
	"github.com/WizardOfMeh/inventory-api/internal/httpapi"
	"github.com/WizardOfMeh/inventory-api/internal/store"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// signal.NotifyContext cancels ctx on SIGINT/SIGTERM, which is what
	// Kubernetes sends before it kills the pod.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := openDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	api := &httpapi.API{
		Store: store.New(db),
		Ping:  db.PingContext,
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           httpapi.Routes(api, cfg.APIToken),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
	}

	// The server runs in its own goroutine so main can wait on ctx.Done().
	errCh := make(chan error, 1)
	go func() {
		slog.Info("server starting", "addr", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		slog.Info("shutdown signal received")
	}

	// Stop accepting new connections and let in-flight requests finish.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}

	slog.Info("server stopped")
	return nil
}

func openDB(ctx context.Context, cfg config.Config) (*sql.DB, error) {
	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)

	// sql.Open only builds the pool; it never contacts the server, so the
	// first real check is the ping below.
	//
	// Exiting on the first failed ping is wrong for a containerised service:
	// the database is a separate lifecycle, and "not ready yet" is normal
	// during a deploy or a database restart. Failing fast there produces a
	// CrashLoopBackOff on top of an outage. Retry with exponential backoff
	// instead, but keep it bounded so a genuinely bad DSN still fails the
	// startup probe rather than hanging forever.
	if err := pingWithRetry(ctx, db, cfg.StartupTimeout); err != nil {
		db.Close()
		return nil, err
	}

	slog.Info("database connected")
	return db, nil
}

// pingWithRetry blocks until the database answers, the overall budget runs
// out, or ctx is cancelled (SIGTERM during startup).
func pingWithRetry(ctx context.Context, db *sql.DB, budget time.Duration) error {
	deadline := time.Now().Add(budget)
	backoff := 500 * time.Millisecond
	const maxBackoff = 5 * time.Second

	for attempt := 1; ; attempt++ {
		pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := db.PingContext(pingCtx)
		cancel()

		if err == nil {
			return nil
		}

		// The parent context is cancelled on SIGINT/SIGTERM: stop retrying
		// and let the process exit promptly instead of ignoring the signal.
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if time.Now().Add(backoff).After(deadline) {
			return fmt.Errorf("database unreachable after %s: %w", budget, err)
		}

		slog.Warn("database not ready, retrying",
			"attempt", attempt,
			"retry_in", backoff.String(),
			"err", err,
		)

		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}

		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}
