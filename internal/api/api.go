// Package api assembles the local HTTP API: /healthz, the read endpoints the
// SPA calls, and the few explicit writes (disposition, cleanup, annotation,
// source registration). This file holds the Server, the route table and the
// helpers shared across domains; each domain has its own file next to it.
// Everything is served on loopback only (ADR-1/ADR-2).
package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"flatline/internal/storage"
	"flatline/internal/vital"
	"flatline/internal/web"
)

// HealthChecker reports whether the backing store is reachable.
type HealthChecker interface {
	Ping(ctx context.Context) error
}

// Server is the local API handler set.
type Server struct {
	health HealthChecker
	db     *storage.DB
	vital  *vital.Repository
	status StatusSource
	cache  *responseCache
	// boot identifies this Server, and so this daemon process, inside every
	// ETag it mints. Two processes never share one.
	boot string

	versionMu    sync.Mutex
	localVersion int64

	// coverage caches which hint keywords the rule assets applicable to each
	// project mention. It
	// is read from files on disk, so it is kept until the rule assets change.
	coverage coverageCache
}

// bootStamp is a value no other process of this daemon can produce.
func bootStamp() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + strconv.FormatInt(int64(os.Getpid()), 36)
}

// NewServer builds the API handler. health may be nil, in which case /healthz
// reports the process is up but the store is not wired.
func NewServer(health HealthChecker) *Server {
	server := &Server{health: health, cache: newResponseCache(), boot: bootStamp()}
	if db, ok := health.(*storage.DB); ok {
		server.db = db
		server.vital = vital.NewRepository(db, vital.NewMachine(vital.DefaultConfig()))
	}
	return server
}

// NewServerWithDB enables the read API in addition to /healthz. NewServer is
// kept source-compatible for the health-only unit tests and accepts a
// *storage.DB as well.
func NewServerWithDB(db *storage.DB) *Server {
	return &Server{health: db, db: db, vital: vital.NewRepository(db, vital.NewMachine(vital.DefaultConfig())), cache: newResponseCache(), boot: bootStamp()}
}

// Handler returns the root http.Handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("HEAD /healthz", s.handleHealthz)
	if s.db != nil {
		mux.HandleFunc("GET /api/v1/assets", s.cached(s.handleAssets))
		mux.HandleFunc("GET /api/v1/assets/{id}", s.tagged(s.handleAsset))
		mux.HandleFunc("GET /api/v1/assets/{id}/transitions", s.tagged(s.handleAssetTransitions))
		mux.HandleFunc("GET /api/v1/assets/{id}/opportunities", s.tagged(s.handleAssetOpportunities))
		mux.HandleFunc("GET /api/v1/assets/{id}/participations", s.tagged(s.handleAssetParticipations))
		mux.HandleFunc("GET /api/v1/assets/{id}/dispositions", s.tagged(s.handleAssetDispositions))
		mux.HandleFunc("GET /api/v1/assets/{id}/references", s.tagged(s.handleAssetReferences))
		mux.HandleFunc("GET /api/v1/assets/{id}/source", s.tagged(s.handleAssetSource))
		mux.HandleFunc("POST /api/v1/assets/{id}/dispositions", s.handleCreateDisposition)
		mux.HandleFunc("POST /api/v1/assets/{id}/restore", s.handleRestoreAsset)
		mux.HandleFunc("GET /api/v1/sessions", s.tagged(s.handleSessions))
		mux.HandleFunc("GET /api/v1/sessions/facets", s.tagged(s.handleSessionFacets))
		mux.HandleFunc("GET /api/v1/sessions/export", s.handleSessionsExport)
		mux.HandleFunc("GET /api/v1/sessions/{id}", s.tagged(s.handleSession))
		mux.HandleFunc("GET /api/v1/sessions/{id}/fleet", s.tagged(s.handleSessionFleet))
		mux.HandleFunc("PUT /api/v1/sessions/{id}/annotation", s.handleSessionAnnotation)
		mux.HandleFunc("GET /api/v1/sessions/{id}/events/{event_id}", s.tagged(s.handleSessionEvent))
		// Only the unfiltered friction overview is worth holding in the
		// response cache; every filtered shape is asked for once.
		mux.HandleFunc("GET /api/v1/friction", s.cachedWhen(unfiltered, s.handleFriction))
		mux.HandleFunc("GET /api/v1/projects", s.cached(s.handleProjects))
		mux.HandleFunc("GET /api/v1/projects/{key}", s.cached(s.handleProject))
		mux.HandleFunc("GET /api/v1/search", s.tagged(s.handleSearch))
		mux.HandleFunc("GET /api/v1/tools", s.cached(s.handleTools))
		mux.HandleFunc("GET /api/v1/overview", s.cached(s.handleOverview))
		mux.HandleFunc("GET /api/v1/insights", s.cached(s.handleInsights))
		mux.HandleFunc("GET /api/v1/signature-watches", s.tagged(s.handleSignatureWatches))
		mux.HandleFunc("POST /api/v1/signature-watches", s.handleCreateSignatureWatch)
		mux.HandleFunc("POST /api/v1/signature-watches/cancel", s.handleCancelSignatureWatch)
		mux.HandleFunc("GET /api/v1/timeline", s.cached(s.handleTimeline))
		mux.HandleFunc("GET /api/v1/notifications", s.cached(s.handleNotifications))
		mux.HandleFunc("GET /api/v1/stats", s.cached(s.handleStats))
		mux.HandleFunc("GET /api/v1/stats/time", s.cached(s.handleTimeStats))
		mux.HandleFunc("GET /api/v1/cleanup", s.tagged(s.handleCleanup))
		mux.HandleFunc("POST /api/v1/cleanup", s.handleBatchCleanup)
		mux.HandleFunc("GET /api/v1/sources", s.tagged(s.handleSources))
		mux.HandleFunc("PUT /api/v1/sources", s.handleUpdateSource)
		mux.HandleFunc("POST /api/v1/sources", s.handleCreateSource)
		mux.HandleFunc("GET /api/v1/ingest/status", s.handleIngestStatus)
		// The health report and the refresh trigger are never cached: they
		// change while the data version stands still.
		mux.HandleFunc("GET /api/v1/ingest/health", s.handleIngestHealth)
		// The now view is live state like health: never cached, never 304ed.
		mux.HandleFunc("GET /api/v1/now", s.handleNow)
		mux.HandleFunc("POST /api/v1/ingest/refresh", s.handleIngestRefresh)
		mux.Handle("/", web.Handler())
	}
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

func rawJSONOrNull(value string) json.RawMessage {
	if strings.TrimSpace(value) == "" {
		return json.RawMessage(`null`)
	}
	return json.RawMessage(value)
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func intPointer(value int) *int { return &value }

// unfiltered reports whether a request carries no query at all, which is the
// only shape of the friction overview worth caching.
func unfiltered(r *http.Request) bool { return r.URL.RawQuery == "" }

func queryLimit(r *http.Request, fallback int) int {
	value, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || value <= 0 {
		return fallback
	}
	if value > 5000 {
		return 5000
	}
	return value
}

func queryOffset(r *http.Request) int {
	value, err := strconv.Atoi(r.URL.Query().Get("offset"))
	if err != nil || value < 0 {
		return 0
	}
	if value > 1000000000 {
		return 1000000000
	}
	return value
}

// rangeAll is the from/to value that asks for no bound at all.
const rangeAll = "all"

// overviewDefaultFrom is the lower bound of a window nobody asked for. It is
// written in the same relative form a caller may type, so the default window
// and from=30d are the same window.
const overviewDefaultFrom = "30d"

// relativeRangePattern is a span back from now: 7d, 12w, 6m.
var relativeRangePattern = regexp.MustCompile(`^([1-9][0-9]{0,3})([dwm])$`)

// rangeBound reads one end of a time window into the stored timestamp form, or
// nil for no bound. Every endpoint that takes from/to reads them through here,
// so the four accepted forms are the same everywhere: a day (2026-08-23), an
// RFC3339 timestamp, a span back from now (7d / 12w / 6m), and "all" — which,
// like an empty value, is no bound.
//
// upper stretches a bare day to its last millisecond, so to=2026-08-23 covers
// that whole day. A span is measured from the moment of the request and is not
// rounded to a day either way.
func rangeBound(value string, upper bool) (*string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == rangeAll {
		return nil, nil
	}
	if match := relativeRangePattern.FindStringSubmatch(strings.ToLower(value)); match != nil {
		count, err := strconv.Atoi(match[1])
		if err != nil {
			return nil, fmt.Errorf("invalid time %q", value)
		}
		return stampPointer(relativeInstant(count, match[2])), nil
	}
	if len(value) == 10 {
		day, err := time.Parse("2006-01-02", value)
		if err != nil {
			return nil, fmt.Errorf("invalid date %q", value)
		}
		if upper {
			return stampPointer(day.Add(24*time.Hour - time.Millisecond)), nil
		}
		return stampPointer(day), nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, fmt.Errorf("invalid time %q", value)
	}
	return stampPointer(parsed), nil
}

// relativeInstant is now less the span the unit names. A month steps back by
// calendar month, so 6m from 2026-08-23 is 2026-02-23 rather than 180 days.
func relativeInstant(count int, unit string) time.Time {
	now := time.Now().UTC()
	switch unit {
	case "d":
		return now.AddDate(0, 0, -count)
	case "w":
		return now.AddDate(0, 0, -7*count)
	default:
		return now.AddDate(0, -count, 0)
	}
}

func stampPointer(at time.Time) *string {
	stamp := formatTime(at)
	return &stamp
}

// rangeWindow reads the from/to pair of one request. defaultFrom stands in
// when the caller gave no from at all; "" leaves the window open at that end.
func rangeWindow(values url.Values, defaultFrom string) (overviewRange, error) {
	from := strings.TrimSpace(values.Get("from"))
	if from == "" {
		from = defaultFrom
	}
	lower, err := rangeBound(from, false)
	if err != nil {
		return overviewRange{}, err
	}
	upper, err := rangeBound(values.Get("to"), true)
	if err != nil {
		return overviewRange{}, err
	}
	return overviewRange{From: lower, To: upper}, nil
}

// boundStamp is one bound as the friction filters carry it: "" for no bound.
func boundStamp(bound *string) string {
	if bound == nil {
		return ""
	}
	return *bound
}

// finishRows reports the first failure of a completed result set: the error the
// iteration stopped on, or the error closing it. It closes the set either way.
func finishRows(rows *sql.Rows) error {
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	return rows.Close()
}

// closeRows is finishRows for a handler: it writes the failure to w and reports
// whether the caller may carry on.
func closeRows(w http.ResponseWriter, rows *sql.Rows) bool {
	if err := finishRows(rows); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
