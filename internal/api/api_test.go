package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

type fakeHealth struct{ err error }

func (f fakeHealth) Ping(context.Context) error { return f.err }

func TestHealthzOK(t *testing.T) {
	srv := NewServer(fakeHealth{nil})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body healthResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Status != "ok" {
		t.Errorf("status = %q, want ok", body.Status)
	}
}

func TestHealthzDegraded(t *testing.T) {
	srv := NewServer(fakeHealth{err: errors.New("db down")})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	var body healthResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Status != "degraded" {
		t.Errorf("status = %q, want degraded", body.Status)
	}
}

func TestHealthzNilHealth(t *testing.T) {
	srv := NewServer(nil)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestUnknownRoute404(t *testing.T) {
	srv := NewServer(fakeHealth{nil})
	req := httptest.NewRequest(http.MethodGet, "/nope", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// Every endpoint that takes a time window reads from/to through rangeBound, so
// the four accepted forms are the same everywhere. This is the table of what
// each form means.
func TestRangeBoundReadsEveryAcceptedForm(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name  string
		value string
		upper bool
		// want is the exact stored timestamp, for the forms that have one.
		want string
		// within is how close to now-span the answer has to be, for a relative
		// form; zero means the case is not relative.
		span time.Duration
		none bool
		fail bool
	}{
		{name: "empty is no bound", value: "", none: true},
		{name: "all is no bound", value: rangeAll, none: true},
		{name: "a day starts at midnight", value: "2026-08-23", want: "2026-08-23T00:00:00Z"},
		{name: "a day as an upper bound covers the whole day", value: "2026-08-23", upper: true,
			want: "2026-08-23T23:59:59.999Z"},
		{name: "rfc3339 keeps its instant", value: "2026-08-23T07:15:00Z", want: "2026-08-23T07:15:00Z"},
		{name: "rfc3339 with an offset becomes utc", value: "2026-08-23T09:15:00+02:00", want: "2026-08-23T07:15:00Z"},
		{name: "7d is seven days back", value: "7d", span: 7 * 24 * time.Hour},
		{name: "30d is thirty days back", value: "30d", span: 30 * 24 * time.Hour},
		{name: "90d is ninety days back", value: "90d", span: 90 * 24 * time.Hour},
		{name: "12w is twelve weeks back", value: "12w", span: 12 * 7 * 24 * time.Hour},
		{name: "a span is not rounded by upper", value: "7d", upper: true, span: 7 * 24 * time.Hour},
		{name: "6m steps back six calendar months", value: "6m",
			span: now.Sub(now.AddDate(0, -6, 0))},
		{name: "an unknown unit is not a span", value: "7y", fail: true},
		{name: "zero is not a span", value: "0d", fail: true},
		{name: "an unreadable day is rejected", value: "2026-13-01", fail: true},
		{name: "an unreadable timestamp is rejected", value: "yesterday", fail: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := rangeBound(tc.value, tc.upper)
			if tc.fail {
				if err == nil {
					t.Fatalf("rangeBound(%q) = %v, want an error", tc.value, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("rangeBound(%q): %v", tc.value, err)
			}
			if tc.none {
				if got != nil {
					t.Fatalf("rangeBound(%q) = %q, want no bound", tc.value, *got)
				}
				return
			}
			if got == nil {
				t.Fatalf("rangeBound(%q) = no bound, want one", tc.value)
			}
			at, err := time.Parse(time.RFC3339Nano, *got)
			if err != nil {
				t.Fatalf("rangeBound(%q) = %q, which is not a timestamp: %v", tc.value, *got, err)
			}
			if tc.span != 0 {
				// The span is measured from the moment of the call, so the
				// answer is checked against the window it must fall in rather
				// than against one exact instant.
				drift := now.Add(-tc.span).Sub(at)
				if drift < -time.Minute || drift > time.Minute {
					t.Fatalf("rangeBound(%q) = %q, want about %s back", tc.value, *got, tc.span)
				}
				return
			}
			if want, _ := time.Parse(time.RFC3339Nano, tc.want); !at.Equal(want) {
				t.Fatalf("rangeBound(%q) = %q, want %q", tc.value, *got, tc.want)
			}
		})
	}
}

// The default overview window is written in the same relative form a caller
// may type, so "no from at all" and from=30d are one window.
func TestRangeWindowDefaultsToTheSameWindowAs30d(t *testing.T) {
	empty, err := rangeWindow(url.Values{}, overviewDefaultFrom)
	if err != nil {
		t.Fatalf("default window: %v", err)
	}
	explicit, err := rangeWindow(url.Values{"from": {"30d"}}, overviewDefaultFrom)
	if err != nil {
		t.Fatalf("explicit window: %v", err)
	}
	if empty.From == nil || explicit.From == nil {
		t.Fatalf("both windows need a lower bound: %v / %v", empty.From, explicit.From)
	}
	left, _ := time.Parse(time.RFC3339Nano, *empty.From)
	right, _ := time.Parse(time.RFC3339Nano, *explicit.From)
	if drift := left.Sub(right); drift < -time.Minute || drift > time.Minute {
		t.Fatalf("default lower bound %q and from=30d %q are different windows", *empty.From, *explicit.From)
	}
	if empty.To != nil {
		t.Errorf("default window upper bound = %q, want none", *empty.To)
	}
}
