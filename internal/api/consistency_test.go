package api

import (
	"context"
	"testing"
	"time"

	"flatline/internal/adapters"
	"flatline/internal/canonical"
	"flatline/internal/eventstore"
)

// The accuracy gate of §16. One number must be one number: if two endpoints
// answer the same question with different totals, one of them is wrong, and
// this test is what turns that into a red build instead of a page nobody
// trusts.

func TestEveryEndpointCountsSessionsTheSameWay(t *testing.T) {
	db := aggregateFixtureDB(t)
	handler := NewServer(db).Handler()

	var health struct {
		Counts struct {
			Sessions int `json:"sessions"`
			Friction int `json:"friction"`
		} `json:"counts"`
	}
	getJSON(t, handler, "/api/v1/ingest/health", &health)

	var overview struct {
		Sessions struct {
			Total   int `json:"total"`
			InRange int `json:"in_range"`
		} `json:"sessions"`
	}
	getJSON(t, handler, "/api/v1/overview?from=all&include=all", &overview)

	var facets struct {
		Total int `json:"total"`
	}
	getJSON(t, handler, "/api/v1/sessions/facets?thread=all&empty=all&from=all", &facets)

	var list struct {
		Pagination struct {
			Total int `json:"total"`
		} `json:"pagination"`
	}
	getJSON(t, handler, "/api/v1/sessions?thread=all&empty=all&from=all&limit=1", &list)

	var stats struct {
		SessionCount int `json:"session_count"`
	}
	getJSON(t, handler, "/api/v1/stats", &stats)

	var projects struct {
		Projects []struct {
			Sessions int `json:"sessions"`
		} `json:"projects"`
	}
	getJSON(t, handler, "/api/v1/projects", &projects)
	projectTotal := 0
	for _, project := range projects.Projects {
		projectTotal += project.Sessions
	}

	// health.counts.sessions is the whole table; every other reading of "how
	// many sessions" with the filters opened up has to land on the same number.
	for name, value := range map[string]int{
		"overview.sessions.total":    overview.Sessions.Total,
		"overview.sessions.in_range": overview.Sessions.InRange,
		"facets.total":               facets.Total,
		"sessions.pagination.total":  list.Pagination.Total,
		"stats.session_count":        stats.SessionCount,
		"Σ projects.sessions":        projectTotal,
	} {
		if value != health.Counts.Sessions {
			t.Errorf("%s = %d, health.counts.sessions = %d", name, value, health.Counts.Sessions)
		}
	}
}

func TestFrictionTotalsAgreeAcrossEndpoints(t *testing.T) {
	db := aggregateFixtureDB(t)
	handler := NewServer(db).Handler()

	var health struct {
		Counts struct {
			Friction int `json:"friction"`
		} `json:"counts"`
	}
	getJSON(t, handler, "/api/v1/ingest/health", &health)

	var friction struct {
		Summary frictionSummaryResponse `json:"summary"`
	}
	getJSON(t, handler, "/api/v1/friction?group=signature", &friction)

	// health counts friction_records rows; the friction page counts the source
	// events behind them, minus the expected nonzero exits it reports on its
	// own line. With one record per event in the fixture the two must line up.
	total := friction.Summary.TotalEvents + friction.Summary.ExpectedExitCount
	if total != health.Counts.Friction {
		t.Errorf("friction.summary.total_events + expected_exit_count = %d, health.counts.friction = %d",
			total, health.Counts.Friction)
	}

	// The same records, grouped four ways, still add up to the same total.
	for _, group := range []string{"project", "category", "tool", "signature"} {
		var response frictionOverviewResponse
		getJSON(t, handler, "/api/v1/friction?limit=500&group="+group, &response)
		sum := 0
		for _, item := range response.Groups {
			sum += item.FrictionCount
		}
		if sum != friction.Summary.TotalEvents {
			t.Errorf("group=%s sums to %d, summary.total_events = %d", group, sum, friction.Summary.TotalEvents)
		}
		if response.Pagination.Total != len(response.Groups) {
			t.Errorf("group=%s pagination.total = %d, listed %d", group, response.Pagination.Total, len(response.Groups))
		}
	}
}

func TestExpectedExitIsExcludedFromFrictionAndReportedOnItsOwn(t *testing.T) {
	db := aggregateFixtureDB(t)
	ctx := context.Background()
	store := eventstore.New(db)
	sessionID := "codex:agg-main"

	// A ripgrep call that matched nothing: exit 1 is ripgrep's answer, not a
	// failure. It is recorded, and then left out of every friction count.
	at := aggregateStart.Add(5 * time.Minute)
	events := ripgrepNoMatchEvents(sessionID, at)
	if _, err := store.IngestEvents(ctx, sessionID, events); err != nil {
		t.Fatalf("ingest events: %v", err)
	}
	if _, err := store.IngestFriction(ctx, sessionID, events); err != nil {
		t.Fatalf("ingest friction: %v", err)
	}
	if err := store.RecomputeSessionProjections(ctx, sessionID); err != nil {
		t.Fatalf("project: %v", err)
	}
	if err := store.RecomputeSessionStats(ctx, sessionID); err != nil {
		t.Fatalf("stats: %v", err)
	}

	handler := NewServer(db).Handler()
	var response frictionOverviewResponse
	getJSON(t, handler, "/api/v1/friction?group=signature&limit=500", &response)
	if response.Summary.ExpectedExitCount != 1 {
		t.Fatalf("expected_exit_count = %d, want 1", response.Summary.ExpectedExitCount)
	}
	for _, group := range response.Groups {
		if group.Category == "expected_exit" {
			t.Fatalf("an expected exit is still listed as friction: %+v", group)
		}
	}

	// Asking for the category explicitly still finds it: the record is a fact,
	// it is just not friction.
	var explicit frictionOverviewResponse
	getJSON(t, handler, "/api/v1/friction?group=signature&category=expected_exit&limit=500", &explicit)
	if explicit.Summary.TotalEvents != 1 {
		t.Fatalf("category=expected_exit total_events = %d, want 1", explicit.Summary.TotalEvents)
	}

	// The session's own counters agree with the page.
	var session struct {
		Session struct {
			FrictionCount     int `json:"friction_count"`
			ExpectedExitCount int `json:"expected_exit_count"`
		} `json:"session"`
	}
	getJSON(t, handler, "/api/v1/sessions/"+sessionID, &session)
	if session.Session.ExpectedExitCount != 1 {
		t.Fatalf("session.expected_exit_count = %d, want 1", session.Session.ExpectedExitCount)
	}
}

// ripgrepNoMatchEvents is the call/result pair the test above records.
func ripgrepNoMatchEvents(sessionID string, at time.Time) []canonical.Event {
	return []canonical.Event{
		frictionTestCallEvent(sessionID, sessionID+"-rg-call", at, map[string]any{
			"tool_name": "Bash", "tool_use_id": sessionID + "-rg", "tool_input": `{"command":"rg -n needle src/"}`,
		}),
		frictionTestEvent(sessionID, sessionID+"-rg-result", at.Add(time.Second), map[string]any{
			"tool_use_id": sessionID + "-rg", "tool_output": "", "exit_code": 1,
		}),
	}
}

func TestUsageReportsUnrecordedRatherThanZero(t *testing.T) {
	db := aggregateFixtureDB(t)
	ctx := context.Background()
	store := eventstore.New(db)

	measured := int64(1234)
	if err := store.RecordFileUsage(ctx, "/synthetic/main.jsonl", "codex:agg-main", &eventstore.SessionUsage{
		Source: eventstore.UsageSourceCodex, TotalTokens: &measured, OutputTokens: &measured,
		ByModel: []eventstore.ModelUsage{{Model: "fixture-model", Turns: 2, TotalTokens: &measured}},
	}, "parser/test"); err != nil {
		t.Fatalf("record usage: %v", err)
	}

	handler := NewServer(db).Handler()
	var measuredSession struct {
		Session struct {
			Usage usageResponse `json:"usage"`
		} `json:"session"`
	}
	getJSON(t, handler, "/api/v1/sessions/codex:agg-main", &measuredSession)
	if measuredSession.Session.Usage.TotalTokens == nil || *measuredSession.Session.Usage.TotalTokens != measured {
		t.Fatalf("measured session usage = %+v", measuredSession.Session.Usage)
	}
	if len(measuredSession.Session.Usage.ByModel) != 1 {
		t.Fatalf("by_model = %+v", measuredSession.Session.Usage.ByModel)
	}

	var unmeasured struct {
		Session struct {
			Usage usageResponse `json:"usage"`
		} `json:"session"`
	}
	getJSON(t, handler, "/api/v1/sessions/codex:agg-second", &unmeasured)
	if unmeasured.Session.Usage.Source != usageSourceUnrecorded {
		t.Fatalf("unmeasured source = %q, want %q", unmeasured.Session.Usage.Source, usageSourceUnrecorded)
	}
	if unmeasured.Session.Usage.TotalTokens != nil {
		t.Fatalf("unmeasured total_tokens = %d, want null", *unmeasured.Session.Usage.TotalTokens)
	}

	// The aggregate carries its own denominator: one session was measured, and
	// the denominator is the same session count the overview reports.
	var overview struct {
		Usage    usageTotals `json:"usage"`
		Sessions struct {
			InRange int `json:"in_range"`
		} `json:"sessions"`
	}
	getJSON(t, handler, "/api/v1/overview?from=all&include=all", &overview)
	if overview.Usage.KnownSessions != 1 {
		t.Fatalf("usage.known_sessions = %d, want 1", overview.Usage.KnownSessions)
	}
	if overview.Usage.InRange != overview.Sessions.InRange {
		t.Fatalf("usage.in_range = %d, sessions.in_range = %d", overview.Usage.InRange, overview.Sessions.InRange)
	}
	if overview.Usage.TotalTokens != measured {
		t.Fatalf("usage.total_tokens = %d, want %d", overview.Usage.TotalTokens, measured)
	}
}

// §16 accuracy gate for the measurement: one definition of a token total,
// applied at every level. Each harness means something different by its own
// "total" — Codex counts cached input inside input_tokens, Claude Code keeps
// them apart and writes no session total at all — so no harness's total is
// stored and every one is recomputed from the components.
func TestTokenTotalIsAlwaysTheSumOfItsComponents(t *testing.T) {
	db := aggregateFixtureDB(t)
	ctx := context.Background()
	store := eventstore.New(db)

	// The two harness shapes, as their readers hand them over: a Codex session
	// whose input already contains the cached share, and a Claude Code session
	// whose three input counters are disjoint.
	codex := &eventstore.SessionUsage{Source: eventstore.UsageSourceCodex,
		InputTokens: int64Ptr(360), CachedInputTokens: int64Ptr(40),
		CacheWriteTokens: int64Ptr(0), OutputTokens: int64Ptr(60), ReasoningTokens: int64Ptr(15)}
	codex.RecomputeTotal()
	claude := &eventstore.SessionUsage{Source: eventstore.UsageSourceClaude,
		InputTokens: int64Ptr(2), CachedInputTokens: int64Ptr(26249),
		CacheWriteTokens: int64Ptr(13595), OutputTokens: int64Ptr(500)}
	claude.RecomputeTotal()
	if err := store.RecordFileUsage(ctx, "/synthetic/codex.jsonl", "codex:agg-main", codex, "parser/test"); err != nil {
		t.Fatalf("record codex usage: %v", err)
	}
	if err := store.RecordFileUsage(ctx, "/synthetic/claude.jsonl", "codex:agg-second", claude, "parser/test"); err != nil {
		t.Fatalf("record claude usage: %v", err)
	}

	handler := NewServerWithDB(db).Handler()
	perSession := int64(0)
	for _, id := range []string{"codex:agg-main", "codex:agg-second"} {
		var page struct {
			Session struct {
				Usage usageResponse `json:"usage"`
			} `json:"session"`
		}
		getJSON(t, handler, "/api/v1/sessions/"+id, &page)
		usage := page.Session.Usage
		if usage.TotalTokens == nil {
			t.Fatalf("%s has no total", id)
		}
		want := sumOptional(usage.InputTokens, usage.CachedInputTokens, usage.CacheWriteTokens, usage.OutputTokens)
		if *usage.TotalTokens != want {
			t.Errorf("%s total_tokens = %d, components add to %d", id, *usage.TotalTokens, want)
		}
		perSession += *usage.TotalTokens
	}

	var overview struct {
		Usage usageTotals `json:"usage"`
	}
	getJSON(t, handler, "/api/v1/overview?from=all&include=all", &overview)
	if overview.Usage.TotalTokens != perSession {
		t.Errorf("overview total_tokens = %d, sum of the sessions = %d", overview.Usage.TotalTokens, perSession)
	}
	// The aggregate is checkable against its own definition: the four
	// components it publishes add up to the total it publishes.
	aggregate := overview.Usage.InputTokens + overview.Usage.CachedTokens +
		overview.Usage.CacheWriteTokens + overview.Usage.OutputTokens
	if overview.Usage.TotalTokens != aggregate {
		t.Errorf("aggregate total_tokens = %d, its own components add to %d", overview.Usage.TotalTokens, aggregate)
	}
	if overview.Usage.Definition == "" {
		t.Error("usage.definition is empty; the page has no way to say what it is printing")
	}
}

func sumOptional(values ...*int64) int64 {
	var total int64
	for _, value := range values {
		if value != nil {
			total += *value
		}
	}
	return total
}

// §16 accuracy gate: active time is time spent inside the session, so it
// cannot exceed the session's own wall clock. The two used to be derived from
// two different readings of a transcript that was still being written — the
// session was frozen at its first reading while the active time kept growing —
// and 15 local sessions reported more active time than they lasted.
func TestActiveTimeNeverExceedsTheSessionItWasMeasuredIn(t *testing.T) {
	db := aggregateFixtureDB(t)
	ctx := context.Background()
	store := eventstore.New(db)

	// The fixture's sessions record no end, so one is given a span here: an
	// invariant over an empty set proves nothing.
	end := aggregateStart.Add(10 * time.Minute)
	if _, err := store.IngestSession(ctx, adapters.SourceCodex, adapters.SessionMeta{
		SourceSessionID: "agg-main", StartedAt: &aggregateStart, EndedAt: &end, CWD: aggregateProject,
	}); err != nil {
		t.Fatalf("ingest session: %v", err)
	}
	if err := store.RecomputeSessionStats(ctx, "codex:agg-main"); err != nil {
		t.Fatalf("recompute stats: %v", err)
	}
	usage := &eventstore.SessionUsage{Source: eventstore.UsageSourceClaude, ActiveMS: int64Ptr(90_000)}
	if err := store.RecordFileUsage(ctx, "/synthetic/span.jsonl", "codex:agg-main", usage, "parser/test"); err != nil {
		t.Fatalf("record usage: %v", err)
	}

	rows, err := db.QueryContext(ctx, `
		SELECT st.session_id, st.duration_ms, u.active_ms
		FROM session_stats st JOIN session_usage u ON u.session_id = st.session_id
		WHERE u.active_ms IS NOT NULL AND st.duration_ms IS NOT NULL`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	checked := 0
	for rows.Next() {
		var id string
		var duration, active int64
		if err := rows.Scan(&id, &duration, &active); err != nil {
			t.Fatalf("scan: %v", err)
		}
		checked++
		if active > duration {
			t.Errorf("%s: active_ms %d exceeds duration_ms %d", id, active, duration)
		}
	}
	if checked == 0 {
		t.Fatal("no session had both a duration and an active time; the invariant was not exercised")
	}
}

// The data page reads /stats and the overview reads /overview. When only one
// of them carried the measurement, the data page printed "token 未记录" while
// the overview printed a token total on the same screen.
func TestStatsAndOverviewReportTheSameMeasurement(t *testing.T) {
	db := aggregateFixtureDB(t)
	ctx := context.Background()
	store := eventstore.New(db)
	usage := &eventstore.SessionUsage{Source: eventstore.UsageSourceCodex,
		InputTokens: int64Ptr(100), CachedInputTokens: int64Ptr(20), OutputTokens: int64Ptr(30),
		ByModel: []eventstore.ModelUsage{{Model: "fixture-model", Turns: 2, TotalTokens: int64Ptr(150)}}}
	usage.RecomputeTotal()
	if err := store.RecordFileUsage(ctx, "/synthetic/stats.jsonl", "codex:agg-main", usage, "parser/test"); err != nil {
		t.Fatalf("record usage: %v", err)
	}

	handler := NewServerWithDB(db).Handler()
	var stats struct {
		Usage   usageTotals       `json:"usage"`
		ByModel []modelUsageTotal `json:"by_model"`
	}
	getJSON(t, handler, "/api/v1/stats", &stats)
	var overview struct {
		Usage usageTotals `json:"usage"`
	}
	getJSON(t, handler, "/api/v1/overview?from=all&include=all", &overview)

	if stats.Usage.TotalTokens != overview.Usage.TotalTokens {
		t.Errorf("stats.usage.total_tokens = %d, overview.usage.total_tokens = %d",
			stats.Usage.TotalTokens, overview.Usage.TotalTokens)
	}
	if stats.Usage.TokenSessions != overview.Usage.TokenSessions {
		t.Errorf("stats token_sessions = %d, overview = %d", stats.Usage.TokenSessions, overview.Usage.TokenSessions)
	}
	if stats.Usage.Definition == "" {
		t.Error("stats.usage.definition is empty")
	}
	if len(stats.ByModel) != 1 || stats.ByModel[0].Model != "fixture-model" {
		t.Errorf("stats.by_model = %+v", stats.ByModel)
	}
}
