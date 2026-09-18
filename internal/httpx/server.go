// Package httpx holds the small amount of HTTP boilerplate shared by the P0 services:
// env-backed configuration, a health endpoint, JSON replies and graceful shutdown.
//
// It exists because cmd/stateservice and cmd/registry need exactly the same thing. It is not a
// framework and should not grow into one.
package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Env returns the value of the environment variable key, or def when it is unset or empty.
func Env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// EnvDuration returns the duration parsed from the environment variable key, or def when it is
// unset, empty or unparseable.
func EnvDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		slog.Warn("ignoring unparseable duration", "key", key, "value", v, "error", err)
		return def
	}
	return d
}

// Health registers GET /healthz on mux. P0 has no dependencies to probe; readiness against Redis
// and Postgres is P1, and belongs on a separate /readyz.
func Health(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
}

// JSON writes v as a JSON response with the given status.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("writing response", "error", err)
	}
}

// NotImplemented replies 501 with the route's planned phase. P0 declares routes and types; the
// handlers behind them land in P1 (HLD §10).
func NotImplemented(w http.ResponseWriter, r *http.Request) {
	JSON(w, http.StatusNotImplemented, map[string]string{
		"error": "not implemented",
		"route": r.Method + " " + r.URL.Path,
		"phase": "P1",
	})
}

// ListenAndServe runs an HTTP server on addr until SIGINT/SIGTERM, then drains in-flight requests
// within the shutdown timeout.
func ListenAndServe(addr string, handler http.Handler, shutdownTimeout time.Duration) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		slog.Info("shutting down", "timeout", shutdownTimeout)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
