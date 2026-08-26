package api

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"flatline/internal/adapters"
	"flatline/internal/canonical"
	"flatline/internal/eventstore"
	"flatline/internal/storage"
)

const lifecycleProject = "/synthetic/project-lifecycle"

// lifecycleFixtureDB builds four signatures, one per lifecycle state, out of
// synthetic sessions placed relative to now so the seven-day window means the
// same thing whenever the test runs. Nothing here is exported history.
func lifecycleFixtureDB(t *testing.T) *storage.DB {
	t.Helper()
	db := testAPIDB(t)
	ctx := context.Background()
	store := eventstore.New(db)
	now := time.Now().UTC()

	// output, sessions and how long ago each of them ran.
	cases := []struct {
		key     string
		output  string
		agesInD []int
	}{
		// first seen 2 days ago, in two sessions: new.
		{"new", "bash: line 1: ruffnew: command not found", []int{2, 1}},
		// first seen 40 days ago and again 1 day ago: active.
		{"active", "bash: line 1: ruffactive: command not found", []int{40, 1}},
		// two sessions, both well outside the window: quiet.
		{"quiet", "bash: line 1: ruffquiet: command not found", []int{40, 30}},
		// one session, outside the window: once.
		{"once", "bash: line 1: ruffonce: command not found", []int{40}},
	}
	for _, item := range cases {
		for index, age := range item.agesInD {
			at := now.AddDate(0, 0, -age)
			sourceID := fmt.Sprintf("lc-%s-%d", item.key, index)
			sessionID, err := store.IngestSession(ctx, adapters.SourceCodex, adapters.SessionMeta{
				SourceSessionID: sourceID, StartedAt: &at, CWD: lifecycleProject, Title: "合成会话 " + sourceID,
			})
			if err != nil {
				t.Fatalf("ingest session %s: %v", sourceID, err)
			}
			events := []canonical.Event{
				frictionTestCallEvent(sessionID, sourceID+"-call", at, map[string]any{
					"tool_name": "Bash", "tool_use_id": sourceID + "-1", "tool_input": `{"command":"ruff check ."}`,
				}),
				frictionTestEvent(sessionID, sourceID+"-result", at.Add(time.Second), map[string]any{
					"tool_use_id": sourceID + "-1", "tool_output": item.output, "exit_code": 127,
				}),
			}
			if _, err := store.IngestEvents(ctx, sessionID, events); err != nil {
				t.Fatalf("ingest events %s: %v", sourceID, err)
			}
			if _, err := store.IngestFriction(ctx, sessionID, events); err != nil {
				t.Fatalf("ingest friction %s: %v", sourceID, err)
			}
			if err := store.RecomputeSessionStats(ctx, sessionID); err != nil {
				t.Fatalf("recompute stats %s: %v", sourceID, err)
			}
			if err := store.RecomputeSessionProjections(ctx, sessionID); err != nil {
				t.Fatalf("project %s: %v", sourceID, err)
			}
		}
	}
	return db
}

type lifecycleGroup struct {
	Signature                 string `json:"signature"`
	SampleLine                string `json:"sample_line"`
	Status                    string `json:"status"`
	SessionCount              int    `json:"session_count"`
	SessionsLastWindow        int    `json:"sessions_last_7d"`
	CountLastWindow           int    `json:"count_last_7d"`
	DaysActive                int    `json:"days_active"`
	ProjectSessionsLastWindow *int   `json:"project_sessions_last_7d"`
	Hint                      *struct {
		Kind      string `json:"kind"`
		Mechanism string `json:"mechanism"`
	} `json:"hint"`
}

func TestFrictionSignatureLifecycleStates(t *testing.T) {
	handler := NewServerWithDB(lifecycleFixtureDB(t)).Handler()
	var response struct {
		Groups     []lifecycleGroup `json:"groups"`
		GroupBy    string           `json:"group_by"`
		WindowDays int              `json:"window_days"`
		Summary    struct {
			ByHintKind []frictionHintKindCount `json:"by_hint_kind"`
		} `json:"summary"`
	}
	getJSON(t, handler, "/api/v1/friction?group=signature", &response)
	if response.GroupBy != "signature" || response.WindowDays != 7 {
		t.Fatalf("group_by=%q window_days=%d", response.GroupBy, response.WindowDays)
	}

	byKey := make(map[string]lifecycleGroup, len(response.Groups))
	for _, group := range response.Groups {
		for _, key := range []string{"new", "active", "quiet", "once"} {
			if strings.Contains(group.SampleLine, "ruff"+key+":") {
				byKey[key] = group
			}
		}
	}
	want := map[string]string{"new": FrictionStatusNew, "active": FrictionStatusActive,
		"quiet": FrictionStatusQuiet, "once": FrictionStatusOnce}
	for key, status := range want {
		group, ok := byKey[key]
		if !ok {
			t.Fatalf("signature %q missing from %+v", key, response.Groups)
		}
		if group.Status != status {
			t.Errorf("signature %q status = %q, want %q (sessions_last_7d=%d count_last_7d=%d session_count=%d)",
				key, group.Status, status, group.SessionsLastWindow, group.CountLastWindow, group.SessionCount)
		}
		if group.Hint == nil || group.Hint.Kind != "environment" {
			t.Errorf("signature %q hint = %+v, want the environment mechanism", key, group.Hint)
		}
	}
	if byKey["active"].SessionsLastWindow != 1 || byKey["quiet"].CountLastWindow != 0 {
		t.Errorf("window counts: active=%+v quiet=%+v", byKey["active"], byKey["quiet"])
	}
	if byKey["quiet"].ProjectSessionsLastWindow == nil {
		t.Errorf("a quiet signature must say whether the same projects ran at all")
	}
	if byKey["new"].ProjectSessionsLastWindow != nil {
		t.Errorf("only a quiet signature carries project_sessions_last_7d")
	}
	if byKey["active"].DaysActive < 30 {
		t.Errorf("active days_active = %d, want the whole span", byKey["active"].DaysActive)
	}
	// The default signature order leads with what is still happening.
	if len(response.Groups) == 0 || response.Groups[0].Status != FrictionStatusActive {
		t.Errorf("first group = %+v, want the active signature first", response.Groups)
	}
	if len(response.Summary.ByHintKind) == 0 || response.Summary.ByHintKind[0].Kind != "environment" {
		t.Errorf("by_hint_kind = %+v", response.Summary.ByHintKind)
	}
}

func TestFrictionLifecycleWindowIsConfigurable(t *testing.T) {
	handler := NewServerWithDB(lifecycleFixtureDB(t)).Handler()
	var response struct {
		Groups     []lifecycleGroup `json:"groups"`
		WindowDays int              `json:"window_days"`
	}
	// A 60-day window puts every signature inside it, so nothing is quiet and
	// the ones that started 40 days ago are no longer "before the window".
	getJSON(t, handler, "/api/v1/friction?group=signature&window=60", &response)
	if response.WindowDays != 60 {
		t.Fatalf("window_days = %d, want 60", response.WindowDays)
	}
	for _, group := range response.Groups {
		if group.Status == FrictionStatusQuiet {
			t.Errorf("signature %q is quiet in a 60-day window: %+v", group.SampleLine, group)
		}
	}
}

func TestOverviewReportsFrictionLifecycle(t *testing.T) {
	handler := NewServerWithDB(lifecycleFixtureDB(t)).Handler()
	var overview struct {
		Lifecycle struct {
			WindowDays int              `json:"window_days"`
			New        int              `json:"new"`
			Active     int              `json:"active"`
			Quiet      int              `json:"quiet"`
			Once       int              `json:"once"`
			TopNew     []lifecycleGroup `json:"top_new"`
			TopActive  []lifecycleGroup `json:"top_active"`
			TopQuiet   []lifecycleGroup `json:"top_quiet"`
		} `json:"friction_lifecycle"`
	}
	getJSON(t, handler, "/api/v1/overview?from=all", &overview)
	life := overview.Lifecycle
	if life.WindowDays != 0 || life.New != 0 || life.Active != 3 || life.Quiet != 0 || life.Once != 1 {
		t.Fatalf("friction_lifecycle = %+v", life)
	}
	if len(life.TopNew) != 0 || len(life.TopActive) != 3 || len(life.TopQuiet) != 0 {
		t.Fatalf("all-time lifecycle top lists = new %d active %d quiet %d", len(life.TopNew), len(life.TopActive), len(life.TopQuiet))
	}
}

func TestOverviewFrictionLifecycleAndRecentSessionsUseSelectedRange(t *testing.T) {
	handler := NewServerWithDB(lifecycleFixtureDB(t)).Handler()
	var overview struct {
		Lifecycle struct {
			WindowDays int `json:"window_days"`
			New        int `json:"new"`
			Active     int `json:"active"`
			Quiet      int `json:"quiet"`
			Once       int `json:"once"`
		} `json:"friction_lifecycle"`
		Recent []struct {
			StartedAt *string `json:"started_at"`
		} `json:"recent_sessions"`
	}
	getJSON(t, handler, "/api/v1/overview?from=7d", &overview)
	if overview.Lifecycle.WindowDays != 7 {
		t.Fatalf("friction_lifecycle.window_days = %d, want the selected 7-day range", overview.Lifecycle.WindowDays)
	}
	if overview.Lifecycle.New != 1 || overview.Lifecycle.Active != 1 || overview.Lifecycle.Quiet != 1 || overview.Lifecycle.Once != 1 {
		t.Fatalf("friction_lifecycle for selected range = %+v; historical records must classify active and quiet signatures", overview.Lifecycle)
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -7)
	for _, session := range overview.Recent {
		if session.StartedAt == nil {
			continue
		}
		started, err := time.Parse(time.RFC3339Nano, *session.StartedAt)
		if err != nil {
			t.Fatalf("parse recent session timestamp %q: %v", *session.StartedAt, err)
		}
		if started.Before(cutoff.Add(-time.Minute)) {
			t.Fatalf("overview returned a recent session outside the selected range: %s", started)
		}
	}
}

func TestProjectPageReportsFrictionLifecycleAndHints(t *testing.T) {
	handler := NewServerWithDB(lifecycleFixtureDB(t)).Handler()
	var page struct {
		Project struct {
			IsHomeDir bool `json:"is_home_dir"`
		} `json:"project"`
		Friction struct {
			ByHintKind []frictionHintKindCount `json:"by_hint_kind"`
			Recurring  []lifecycleGroup        `json:"recurring"`
			Lifecycle  struct {
				New    int `json:"new"`
				Active int `json:"active"`
				Quiet  int `json:"quiet"`
			} `json:"lifecycle"`
		} `json:"friction"`
	}
	getJSON(t, handler, "/api/v1/projects/"+urlEscape(lifecycleProject)+"?from=all", &page)
	if page.Project.IsHomeDir {
		t.Errorf("a synthetic project directory must not be marked as the home directory")
	}
	if page.Friction.Lifecycle.New != 1 || page.Friction.Lifecycle.Active != 1 || page.Friction.Lifecycle.Quiet != 1 {
		t.Fatalf("project lifecycle = %+v", page.Friction.Lifecycle)
	}
	if len(page.Friction.ByHintKind) == 0 {
		t.Fatalf("project by_hint_kind is empty")
	}
	if len(page.Friction.Recurring) == 0 || page.Friction.Recurring[0].Hint == nil {
		t.Fatalf("project recurring friction carries no hint: %+v", page.Friction.Recurring)
	}
}
