// Package api assembles the local HTTP API. P1 exposes only the health
// check; all endpoints are served on loopback only (ADR-1/ADR-2).
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// HealthChecker reports whether the backing store is reachable.
type HealthChecker interface {
	Ping(ctx context.Context) error
}

// Server is the local API handler set.
type Server struct {
	health HealthChecker
}

// NewServer builds the API handler. health may be nil, in which case /healthz
// reports the process is up but the store is not wired.
func NewServer(health HealthChecker) *Server {
	return &Server{health: health}
}

// Handler returns the root http.Handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("HEAD /healthz", s.handleHealthz)
	return mux
}

type healthResponse struct {
	Status string `json:"status"`
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	resp := healthResponse{Status: "ok"}
	code := http.StatusOK

	if s.health != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := s.health.Ping(ctx); err != nil {
			resp = healthResponse{Status: "degraded"}
			code = http.StatusServiceUnavailable
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(resp)
}
