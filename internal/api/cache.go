package api

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"flatline/internal/runtime"
)

const (
	maxCacheEntryBytes = 32 << 20
	maxCacheTotalBytes = 96 << 20
)

// StatusSource is the running daemon's view of itself. Tests and the
// health-only server leave it unset; the API then reports an idle daemon and
// versions its responses from its own write counter alone.
type StatusSource interface {
	DataVersion() int64
	Progress() runtime.ImportProgress
}

// VersionBumper is the daemon's persisted version counter. When the status
// source implements it, a user's own write goes through the same counter the
// daemon publishes, so one number covers both and it survives a restart.
type VersionBumper interface {
	BumpDataVersion() int64
}

func (s *Server) SetStatusSource(source StatusSource) { s.status = source }

// dataVersion is what every ETag and every cache entry is keyed on. It moves
// when the daemon finishes a refresh pass and when the user writes an
// annotation, and at no other time.
func (s *Server) dataVersion() int64 {
	s.versionMu.Lock()
	local := s.localVersion
	s.versionMu.Unlock()
	if s.status != nil {
		return s.status.DataVersion() + local
	}
	return local
}

func (s *Server) bumpDataVersion() {
	if bumper, ok := s.status.(VersionBumper); ok {
		bumper.BumpDataVersion()
		return
	}
	s.versionMu.Lock()
	s.localVersion++
	s.versionMu.Unlock()
}

type cacheEntry struct {
	body []byte
}

// responseCache holds one generation of responses. A new data version drops
// the whole generation rather than expiring entries one by one: every entry
// was derived from the same facts, so they all go stale together.
type responseCache struct {
	mu      sync.Mutex
	version int64
	total   int
	entries map[string]cacheEntry
}

func newResponseCache() *responseCache {
	return &responseCache{version: -1, entries: make(map[string]cacheEntry)}
}

func (c *responseCache) get(version int64, key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.version != version {
		return nil, false
	}
	entry, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	return entry.body, true
}

func (c *responseCache) put(version int64, key string, body []byte) {
	if len(body) > maxCacheEntryBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.version != version {
		c.version, c.total, c.entries = version, 0, make(map[string]cacheEntry)
	}
	if c.total+len(body) > maxCacheTotalBytes {
		return
	}
	c.entries[key] = cacheEntry{body: body}
	c.total += len(body)
}

// cached serves a GET response from the process-local cache and answers
// If-None-Match with 304.
func (s *Server) cached(handler http.HandlerFunc) http.HandlerFunc {
	return s.cachedWhen(func(*http.Request) bool { return true }, handler)
}

// tagged versions a response without storing it, for endpoints whose body is
// too varied to be worth caching but which the browser can still revalidate.
func (s *Server) tagged(handler http.HandlerFunc) http.HandlerFunc {
	return s.cachedWhen(func(*http.Request) bool { return false }, handler)
}

func (s *Server) cachedWhen(cacheable func(*http.Request) bool, handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		version := s.dataVersion()
		key := r.URL.Path + "?" + r.URL.RawQuery
		// The boot stamp is what makes two processes incomparable. The version
		// counter alone was not enough: a daemon that restarted began again at
		// 1, and a browser holding v1 from the previous process was told its
		// copy was current — the overview then showed 903 sessions beside a
		// sidebar that had just fetched 1164.
		etag := fmt.Sprintf("\"v%d-%s-%s\"", version, s.boot, shortHash(key))
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "no-cache")
		if etagMatches(r.Header.Get("If-None-Match"), etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		store := cacheable(r)
		if store {
			if body, ok := s.cache.get(version, key); ok {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(body)
				return
			}
		}
		recorder := &captureWriter{ResponseWriter: w, status: http.StatusOK, capture: store}
		handler(recorder, r)
		if store && recorder.capture && recorder.status == http.StatusOK {
			s.cache.put(version, key, recorder.body)
		}
	}
}

// captureWriter streams the response to the client and keeps a copy for the
// cache. A response that outgrows the entry limit stops being copied; it is
// still delivered in full.
type captureWriter struct {
	http.ResponseWriter
	status  int
	body    []byte
	capture bool
}

func (c *captureWriter) WriteHeader(status int) {
	c.status = status
	c.ResponseWriter.WriteHeader(status)
}

func (c *captureWriter) Write(p []byte) (int, error) {
	if c.capture {
		if len(c.body)+len(p) > maxCacheEntryBytes {
			c.capture, c.body = false, nil
		} else {
			c.body = append(c.body, p...)
		}
	}
	return c.ResponseWriter.Write(p)
}

func etagMatches(header, etag string) bool {
	if strings.TrimSpace(header) == "" {
		return false
	}
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}
	return false
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}
