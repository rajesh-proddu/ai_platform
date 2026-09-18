// Command registry serves the model registry plane of HLD §9: what models exist, which version is
// live where, and whether it is allowed to be.
//
// P0 declares the routes and the wire types (internal/registry). The handlers behind them reply 501
// until P1 adds the durable metadata store.
package main

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/rajesh-proddu/ai_platform/internal/httpx"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	addr := httpx.Env("REGISTRY_HTTP_ADDR", ":8091")
	shutdownTimeout := httpx.EnvDuration("SHUTDOWN_TIMEOUT", 10*time.Second)

	// TODO(P1): construct the registry and wire it into the handlers below — durable metadata in
	// MySQL/Postgres (REGISTRY_DB_URL), artifacts in S3. registry.NewInMemoryRegistry() is the P0
	// reference implementation and the contract the durable one must meet.

	if err := httpx.ListenAndServe(addr, routes(), shutdownTimeout); err != nil {
		slog.Error("server exited", "error", err)
		os.Exit(1)
	}
}

func routes() *http.ServeMux {
	mux := http.NewServeMux()
	httpx.Health(mux)

	// --- model cards (HLD §9) ---
	mux.HandleFunc("POST /v1/models", httpx.NotImplemented)
	mux.HandleFunc("GET /v1/models", httpx.NotImplemented)
	mux.HandleFunc("GET /v1/models/{id}/versions/{version}", httpx.NotImplemented)
	// Rollout is a state change, never a delete: retired versions stay resolvable for audit.
	mux.HandleFunc("PUT /v1/models/{id}/versions/{version}/rollout-state", httpx.NotImplemented)

	// --- deployment bindings (HLD §9) ---
	mux.HandleFunc("POST /v1/deployments", httpx.NotImplemented)
	mux.HandleFunc("GET /v1/models/{id}/versions/{version}/deployments", httpx.NotImplemented)
	mux.HandleFunc("DELETE /v1/deployments/{id}", httpx.NotImplemented)

	return mux
}
