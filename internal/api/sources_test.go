package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"flatline/internal/eventstore"
)

// The source registry is what makes "which machine did this session come from"
// answerable. These tests hold the two rules that matter: a PUT changes only
// the name and the switch, and a POST records a root without importing
// anything.

func TestSourcesListsRegisteredRootsAndCountsTheirSessions(t *testing.T) {
	db := aggregateFixtureDB(t)
	ctx := context.Background()
	store := eventstore.New(db)
	if err := store.RegisterSource(ctx, "codex", "/synthetic/codex-root", "Codex"); err != nil {
		t.Fatalf("register: %v", err)
	}
	exec(t, db, `INSERT INTO native_files (path, size, mtime_ns, session_id, last_read_at)
		VALUES ('/synthetic/codex-root/a.jsonl', 1, 1, 'codex:agg-main', '2026-08-01T00:00:00Z')`)
	if attached, err := store.AttachSessionSources(ctx); err != nil || attached != 1 {
		t.Fatalf("attach = %d (%v), want 1", attached, err)
	}

	handler := NewServerWithDB(db).Handler()
	var page struct {
		Sources []eventstore.Source `json:"sources"`
		Note    string              `json:"note"`
	}
	getJSON(t, handler, "/api/v1/sources", &page)
	if len(page.Sources) != 1 {
		t.Fatalf("sources = %+v", page.Sources)
	}
	if page.Sources[0].Sessions != 1 {
		t.Fatalf("source sessions = %d, want the one session read from that root", page.Sources[0].Sessions)
	}
	if !strings.Contains(page.Note, "只读") {
		t.Fatalf("note does not say the scan is read-only: %q", page.Note)
	}

	// The session row carries the name of the root it came from, which is what
	// a multi-machine list is read by.
	var session struct {
		Session struct {
			MachineLabel *string `json:"machine_label"`
			SourceLabel  *string `json:"source_label"`
		} `json:"session"`
	}
	if _, err := store.UpdateSource(ctx, page.Sources[0].ID, eventstore.SourceUpdate{
		MachineLabel: strPtr("工作站"),
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	getJSON(t, handler, "/api/v1/sessions/codex:agg-main", &session)
	if session.Session.MachineLabel == nil || *session.Session.MachineLabel != "工作站" {
		t.Fatalf("session machine_label = %v", session.Session.MachineLabel)
	}
}

func TestSourcePutChangesOnlyTheNameAndTheSwitch(t *testing.T) {
	db := aggregateFixtureDB(t)
	ctx := context.Background()
	store := eventstore.New(db)
	if err := store.RegisterSource(ctx, "codex", "/synthetic/codex-root", "Codex"); err != nil {
		t.Fatalf("register: %v", err)
	}
	sources, err := store.ListSources(ctx)
	if err != nil || len(sources) != 1 {
		t.Fatalf("list = %+v (%v)", sources, err)
	}
	handler := NewServerWithDB(db).Handler()

	body := `{"id":` + strconv.FormatInt(sources[0].ID, 10) + `,"label":"另一台机器","machine_label":"laptop","enabled":false}`
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPut, "/api/v1/sources", strings.NewReader(body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", recorder.Code, recorder.Body.String())
	}
	updated, err := store.ListSources(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if updated[0].Root != sources[0].Root {
		t.Fatalf("root changed from %q to %q; a different root is a different source", sources[0].Root, updated[0].Root)
	}
	if updated[0].Enabled {
		t.Fatal("enabled was not turned off")
	}
	if updated[0].MachineLabel == nil || *updated[0].MachineLabel != "laptop" {
		t.Fatalf("machine_label = %v", updated[0].MachineLabel)
	}

	// A root the request tries to change is refused outright rather than
	// silently ignored.
	rejected := httptest.NewRecorder()
	handler.ServeHTTP(rejected, httptest.NewRequest(http.MethodPut, "/api/v1/sources",
		strings.NewReader(`{"id":`+strconv.FormatInt(sources[0].ID, 10)+`,"root":"/elsewhere"}`)))
	if rejected.Code != http.StatusBadRequest {
		t.Fatalf("PUT with a root = %d, want 400", rejected.Code)
	}
}

func TestSourcePostRecordsARootWithoutImportingIt(t *testing.T) {
	db := aggregateFixtureDB(t)
	handler := NewServerWithDB(db).Handler()
	root := t.TempDir()

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/sources",
		strings.NewReader(`{"kind":"codex","root":"`+root+`","label":"rsync 过来的"}`)))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("POST = %d: %s", recorder.Code, recorder.Body.String())
	}
	sources, err := eventstore.New(db).ListSources(context.Background())
	if err != nil || len(sources) != 1 || sources[0].Root != root {
		t.Fatalf("sources = %+v (%v)", sources, err)
	}
	if sources[0].Sessions != 0 {
		t.Fatalf("a newly recorded root already has %d sessions; POST records where to read, it does not read", sources[0].Sessions)
	}

	for _, body := range []string{
		`{"kind":"not-a-harness","root":"` + root + `"}`,
		`{"kind":"codex","root":"relative/path"}`,
		`{"kind":"codex","root":"` + root + `/missing"}`,
	} {
		rejected := httptest.NewRecorder()
		handler.ServeHTTP(rejected, httptest.NewRequest(http.MethodPost, "/api/v1/sources", strings.NewReader(body)))
		if rejected.Code != http.StatusBadRequest {
			t.Errorf("POST %s = %d, want 400", body, rejected.Code)
		}
	}
}
