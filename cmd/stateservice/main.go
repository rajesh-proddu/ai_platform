// Command stateservice serves the state plane of HLD §7: one service, two scopes.
//
// P0 declares the routes and the wire types (internal/session). The handlers behind them reply 501
// until P1 adds the Redis session scope, the pgvector memory scope and the context assembler.
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

	addr := httpx.Env("STATE_HTTP_ADDR", ":8090")
	shutdownTimeout := httpx.EnvDuration("SHUTDOWN_TIMEOUT", 10*time.Second)

	// TODO(P1): construct the store and wire it into the handlers below:
	//   session scope → Redis    (REDIS_URL, default redis://redis:6379/0)
	//   memory scope  → pgvector (PGVECTOR_URL, pgvector.infra.svc.cluster.local:5432)
	// session.NewInMemoryStore() is the P0 reference implementation and the contract both must meet.

	if err := httpx.ListenAndServe(addr, routes(), shutdownTimeout); err != nil {
		slog.Error("server exited", "error", err)
		os.Exit(1)
	}
}

func routes() *http.ServeMux {
	mux := http.NewServeMux()
	httpx.Health(mux)

	// --- session scope (HLD §7.1) ---
	mux.HandleFunc("POST /v1/sessions", httpx.NotImplemented)
	mux.HandleFunc("GET /v1/sessions/{id}", httpx.NotImplemented)
	mux.HandleFunc("DELETE /v1/sessions/{id}", httpx.NotImplemented)
	mux.HandleFunc("PATCH /v1/sessions/{id}/run-state", httpx.NotImplemented)
	mux.HandleFunc("POST /v1/sessions/{id}/turns", httpx.NotImplemented)
	mux.HandleFunc("GET /v1/sessions/{id}/turns", httpx.NotImplemented)

	// --- memory scope (HLD §7.2) ---
	mux.HandleFunc("POST /v1/memories", httpx.NotImplemented)
	mux.HandleFunc("GET /v1/memories", httpx.NotImplemented)
	mux.HandleFunc("DELETE /v1/memories/{id}", httpx.NotImplemented)

	// TODO(P1): POST /v1/sessions/{id}/context — the context assembler of HLD §7.3.
	return mux
}
