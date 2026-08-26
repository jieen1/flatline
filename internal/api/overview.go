package api

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
)

type overviewRange struct {
	From *string `json:"from"`
	To   *string `json:"to"`
}

type activityDay struct {
	Sessions int `json:"sessions"`
	Events   int `json:"events"`
	Friction int `json:"friction"`
}

type frictionToolCount struct {
	ToolName string `json:"tool_name"`
	Count    int    `json:"count"`
	Sessions int    `json:"sessions"`
}

type frictionCategoryCount struct {
	Category string `json:"category"`
	Count    int    `json:"count"`
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	rangeSpec, unscoped, args, err := overviewWindow(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	// A subagent thread and a session with nothing in it are not what the
	// overview means by "a session", so they are left out by default. The
	// counts of each are still reported, and include=all counts everything.
	includeAll := r.URL.Query().Get("include") == "all"
	scope, err := s.mainSessionScope(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	where := unscoped
	if scope != "" && !includeAll {
		where = andWhere(unscoped, strings.TrimPrefix(scope, " AND "))
	}
	scoped := scope != "" && !includeAll
	// key is the one-word name of the scope in force, so a caller can branch on
	// it without re-deriving it from the two booleans.
	scopeKey := scopeAll
	if scoped {
		scopeKey = scopeMainNonEmpty
	}
	response := map[string]any{
		"range": rangeSpec, "data_version": s.dataVersion(),
		"scope": map[string]any{
			"key":                scopeKey,
			"main_sessions_only": scoped,
			"excludes_empty":     scoped,
			"note":               "默认只统计 thread_kind='main' 且非空会话；include=all 统计全部",
			"note_en":            "By default only sessions with thread_kind='main' that are not empty are counted; include=all counts every session.",
		},
	}

	totals, err := s.overviewTotals(ctx, where, args, unscoped)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for key, value := range totals {
		response[key] = value
	}

	activity, err := s.overviewActivity(ctx, where, args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response["activity_by_day"] = activity

	projects, err := s.projects(ctx, where, args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(projects) > 8 {
		projects = projects[:8]
	}
	response["top_projects"] = projects

	tools, err := s.topFrictionTools(ctx, where, args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response["top_friction_tools"] = tools

	categories, err := s.topFrictionCategories(ctx, where, args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response["top_friction_categories"] = categories

	// The recurring list and the lifecycle counts read the same records, so
	// they are loaded once and both computed over that one set. The selected
	// overview range is also the lifecycle range; an all-time selection stays
	// all-time instead of silently falling back to the endpoint default.
	window := 0
	if rawWindow := strings.TrimSpace(r.URL.Query().Get("window")); rawWindow != "" {
		window = frictionWindowDays(rawWindow)
	}
	frictionFilters := overviewFrictionFilters(rangeSpec, window, 0)
	frictionSet, err := s.loadFrictionSet(ctx, frictionFilters)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	recurring, err := s.overviewRecurringFriction(ctx, frictionSet, frictionFilters)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response["recurring_friction"] = recurring

	lifecycle, err := s.frictionLifecycle(ctx, frictionSet, frictionFilters)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response["friction_lifecycle"] = lifecycle

	filter := overviewAggregateFilter(rangeSpec)
	programs, err := s.topPrograms(ctx, filter, 8)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response["top_programs"] = programs

	// A scratch directory is not anyone's hot file; it is excluded here and
	// reported as its own count rather than silently dropped.
	files, outside, err := s.hotFiles(ctx, filter, "", "/tmp/", 8)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response["hot_files"], response["scratch_files"] = files, outside

	tags, err := s.topTags(ctx, where, args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response["top_tags"] = tags

	// The measurement carries its own denominator: known_sessions out of the
	// same in_range the session counts above use, so a session that was never
	// measured is visible as a gap rather than as a zero.
	usageAggregate, err := s.aggregateUsage(ctx, where, args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response["usage"] = usageAggregate

	byModel, err := s.aggregateModelUsage(ctx, where, args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response["by_model"] = byModel

	assetCounts, err := s.assetAttention(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response["assets"] = assetCounts

	// The period block answers "what happened in this stretch" in a shape that
	// the stretch before it can be reported in too. compare=1 asks for that
	// previous stretch and the movement between the two.
	periodScope := periodScope{}
	if scope != "" && !includeAll {
		periodScope.scope = scope
	}
	if err := s.attachPeriod(ctx, response, rangeSpec, periodScope, wantsCompare(r.URL.Query().Get("compare"))); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	recentQuery := sessionQuery{sortKey: "recent", limit: 8}
	if rangeSpec.From != nil {
		recentQuery.conditions = append(recentQuery.conditions, sessionCondition{dimension: "date", expr: "s.started_at >= ?", args: []any{*rangeSpec.From}})
	}
	if rangeSpec.To != nil {
		recentQuery.conditions = append(recentQuery.conditions, sessionCondition{dimension: "date", expr: "s.started_at <= ?", args: []any{*rangeSpec.To}})
	}
	if scoped {
		recentQuery.conditions = append(recentQuery.conditions,
			sessionCondition{dimension: "thread", expr: "(s.thread_kind IS NULL OR s.thread_kind = 'main')"},
			sessionCondition{dimension: "empty", expr: "COALESCE(st.is_empty, 0) = 0"})
	}
	recent, _, err := s.querySessions(ctx, recentQuery)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response["recent_sessions"] = recent

	var lastEventAt sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(st.last_event_at) FROM sessions s LEFT JOIN session_stats st ON st.session_id = s.id`+where, args...).Scan(&lastEventAt); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if lastEventAt.Valid {
		response["last_event_at"] = lastEventAt.String
	} else {
		response["last_event_at"] = nil
	}

	writeJSON(w, http.StatusOK, response)
}

// overviewWindow reads from/to in the four forms rangeBound accepts. The
// default window is the last 30 days; from=all removes the lower bound.
func overviewWindow(r *http.Request) (overviewRange, string, []any, error) {
	spec, err := rangeWindow(r.URL.Query(), overviewDefaultFrom)
	if err != nil {
		return overviewRange{}, "", nil, err
	}
	where, args := "", make([]any, 0, 2)
	if spec.From != nil {
		where = andWhere(where, "s.started_at >= ?")
		args = append(args, *spec.From)
	}
	if spec.To != nil {
		where = andWhere(where, "s.started_at <= ?")
		args = append(args, *spec.To)
	}
	return spec, where, args, nil
}

// overviewRecurringFriction is the friction that came back in another session.
// It reuses the friction endpoint's own grouping so the two pages cannot
// disagree.
func (s *Server) overviewRecurringFriction(ctx context.Context, set frictionSet, filters frictionFilters) ([]frictionGroupResponse, error) {
	filters.Limit = 5
	return s.signatureGroups(ctx, set, filters)
}

func overviewFrictionFilters(rangeSpec overviewRange, window, limit int) frictionFilters {
	filters := frictionFilters{Sort: "sessions", Group: "signature", Limit: limit, Window: window,
		WindowExplicit: rangeSpec.From == nil && rangeSpec.To == nil}
	if rangeSpec.From != nil {
		filters.From = *rangeSpec.From
	}
	if rangeSpec.To != nil {
		filters.To = *rangeSpec.To
	}
	return filters
}

func overviewAggregateFilter(rangeSpec overviewRange) aggregateFilter {
	filter := aggregateFilter{From: rangeSpec.From, To: rangeSpec.To}
	if rangeSpec.From != nil {
		filter.conditions = append(filter.conditions, "s.started_at >= ?")
		filter.args = append(filter.args, *rangeSpec.From)
	}
	if rangeSpec.To != nil {
		filter.conditions = append(filter.conditions, "s.started_at <= ?")
		filter.args = append(filter.args, *rangeSpec.To)
	}
	return filter
}

func (s *Server) overviewTotals(ctx context.Context, where string, args []any, unscoped string) (map[string]any, error) {
	var inRange, events, messages, toolCalls, friction, toolError, nonzeroExit int
	var sessionsWithFriction, knownDuration, totalDuration int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       SUM(COALESCE(st.event_count, 0)), SUM(COALESCE(st.message_count, 0)),
		       SUM(COALESCE(st.tool_call_count, 0)), SUM(COALESCE(st.friction_count, 0)),
		       SUM(COALESCE(st.tool_error_count, 0)), SUM(COALESCE(st.nonzero_exit_count, 0)),
		       SUM(CASE WHEN COALESCE(st.friction_count, 0) > 0 THEN 1 ELSE 0 END),
		       SUM(CASE WHEN st.duration_ms IS NULL THEN 0 ELSE 1 END),
		       SUM(COALESCE(st.duration_ms, 0))
		FROM sessions s LEFT JOIN session_stats st ON st.session_id = s.id`+where, args...).
		Scan(&inRange, &nullableInt{&events}, &nullableInt{&messages}, &nullableInt{&toolCalls},
			&nullableInt{&friction}, &nullableInt{&toolError}, &nullableInt{&nonzeroExit},
			&nullableInt{&sessionsWithFriction}, &nullableInt{&knownDuration}, &nullableInt{&totalDuration})
	if err != nil {
		return nil, fmt.Errorf("api: overview totals: %w", err)
	}

	byHarness := make(map[string]int)
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.source, COUNT(*) FROM sessions s
		LEFT JOIN session_stats st ON st.session_id = s.id`+where+` GROUP BY s.source`, args...)
	if err != nil {
		return nil, fmt.Errorf("api: overview harness split: %w", err)
	}
	for rows.Next() {
		var source string
		var count int
		if err := rows.Scan(&source, &count); err != nil {
			rows.Close()
			return nil, err
		}
		byHarness[source] = count
	}
	rows.Close()

	var totalSessions, totalProjects, projectsInRange, assetViolations, userInterrupts int
	if err := s.db.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(*) FROM sessions),
		       (SELECT COUNT(DISTINCT COALESCE(project_key, '`+unrecordedKey+`')) FROM sessions)`).
		Scan(&totalSessions, &totalProjects); err != nil {
		return nil, fmt.Errorf("api: overview session totals: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT COALESCE(s.project_key, '`+unrecordedKey+`')) FROM sessions s
		LEFT JOIN session_stats st ON st.session_id = s.id`+where, args...).
		Scan(&projectsInRange); err != nil {
		return nil, fmt.Errorf("api: overview project count: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM events e JOIN sessions s ON s.id = e.session_id
		LEFT JOIN session_stats st ON st.session_id = s.id`+
		andWhere(where, "e.event_type = 'asset_violation'"), args...).
		Scan(&assetViolations); err != nil {
		return nil, fmt.Errorf("api: overview asset violations: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM friction_records f JOIN sessions s ON s.id = f.session_id
		LEFT JOIN session_stats st ON st.session_id = s.id`+
		andWhere(where, "f.friction_kind = 'user_interrupt'"), args...).
		Scan(&userInterrupts); err != nil {
		return nil, fmt.Errorf("api: overview user interrupts: %w", err)
	}

	sessionCounts := map[string]any{"in_range": inRange, "total": totalSessions, "by_harness": byHarness}
	threadCounts, err := s.overviewThreadCounts(ctx, unscoped, args)
	if err != nil {
		return nil, err
	}
	for key, value := range threadCounts {
		sessionCounts[key] = value
	}

	return map[string]any{
		"sessions": sessionCounts,
		"projects": map[string]any{"in_range": projectsInRange, "total": totalProjects},
		"events":   events, "messages": messages, "tool_calls": toolCalls,
		"duration": map[string]any{"known_sessions": knownDuration, "total_ms": totalDuration},
		"friction": map[string]any{
			"total": friction, "tool_error": toolError, "nonzero_exit": nonzeroExit,
			"asset_violation": assetViolations, "user_interrupt": userInterrupts,
			"sessions_with_friction": sessionsWithFriction,
		},
	}, nil
}

// overviewThreadCounts reports how many sessions of each kind are in range,
// counted without the default scope. They are what the toggles on the page
// stand for, so they must not themselves be filtered by it.
func (s *Server) overviewThreadCounts(ctx context.Context, unscoped string, args []any) (map[string]int, error) {
	out := map[string]int{}
	hasThread, err := s.hasColumn(ctx, "sessions", "thread_kind")
	if err != nil {
		return out, err
	}
	hasEmpty, err := s.hasColumn(ctx, "session_stats", "is_empty")
	if err != nil {
		return out, err
	}
	if !hasThread && !hasEmpty {
		return out, nil
	}
	var main, subagent, empty int
	if err := s.db.QueryRowContext(ctx, `
		SELECT SUM(CASE WHEN s.thread_kind IS NULL OR s.thread_kind = 'main' THEN 1 ELSE 0 END),
		       SUM(CASE WHEN s.thread_kind = 'subagent' THEN 1 ELSE 0 END),
		       SUM(CASE WHEN COALESCE(st.is_empty, 0) = 1 THEN 1 ELSE 0 END)
		FROM sessions s LEFT JOIN session_stats st ON st.session_id = s.id`+unscoped, args...).
		Scan(&nullableInt{&main}, &nullableInt{&subagent}, &nullableInt{&empty}); err != nil {
		return nil, fmt.Errorf("api: overview thread counts: %w", err)
	}
	out["main"], out["subagent"], out["empty"] = main, subagent, empty
	return out, nil
}

func (s *Server) overviewActivity(ctx context.Context, where string, args []any) (map[string]activityDay, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT substr(s.started_at, 1, 10) AS day, COUNT(*),
		       SUM(COALESCE(st.event_count, 0)), SUM(COALESCE(st.friction_count, 0))
		FROM sessions s LEFT JOIN session_stats st ON st.session_id = s.id`+
		andWhere(where, "s.started_at IS NOT NULL AND s.started_at <> ''")+
		` GROUP BY day ORDER BY day`, args...)
	if err != nil {
		return nil, fmt.Errorf("api: overview activity: %w", err)
	}
	defer rows.Close()
	out := make(map[string]activityDay)
	for rows.Next() {
		var day string
		var item activityDay
		if err := rows.Scan(&day, &item.Sessions, &nullableInt{&item.Events}, &nullableInt{&item.Friction}); err != nil {
			return nil, fmt.Errorf("api: scan overview activity: %w", err)
		}
		out[day] = item
	}
	return out, rows.Err()
}

func (s *Server) topFrictionTools(ctx context.Context, where string, args []any) ([]frictionToolCount, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT COALESCE(NULLIF(f.tool_name, ''), '') AS tool,
		       COUNT(*), COUNT(DISTINCT f.session_id)
		FROM friction_records f JOIN sessions s ON s.id = f.session_id
		LEFT JOIN session_stats st ON st.session_id = s.id`+where+`
		GROUP BY tool HAVING tool <> '' ORDER BY COUNT(*) DESC, tool LIMIT 10`, args...)
	if err != nil {
		return nil, fmt.Errorf("api: overview friction tools: %w", err)
	}
	defer rows.Close()
	out := make([]frictionToolCount, 0)
	for rows.Next() {
		var item frictionToolCount
		if err := rows.Scan(&item.ToolName, &item.Count, &item.Sessions); err != nil {
			return nil, fmt.Errorf("api: scan overview friction tool: %w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// topFrictionCategories counts one source event once, even when it produced
// several friction rows. A record the classifier did not categorise is
// reported under __unrecorded__ rather than dropped, and the whole list is
// empty when the classifier column does not exist yet.
func (s *Server) topFrictionCategories(ctx context.Context, where string, args []any) ([]frictionCategoryCount, error) {
	out := make([]frictionCategoryCount, 0)
	has, err := s.hasColumn(ctx, "friction_records", "category")
	if err != nil || !has {
		return out, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT COALESCE(NULLIF(f.category, ''), '`+unrecordedKey+`') AS category,
		       COUNT(DISTINCT f.session_id || char(31) || f.event_type || char(31) || f.source_event_id)
		FROM friction_records f JOIN sessions s ON s.id = f.session_id
		LEFT JOIN session_stats st ON st.session_id = s.id`+where+`
		GROUP BY category ORDER BY 2 DESC, category LIMIT 10`, args...)
	if err != nil {
		return nil, fmt.Errorf("api: overview friction categories: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item frictionCategoryCount
		if err := rows.Scan(&item.Category, &item.Count); err != nil {
			return nil, fmt.Errorf("api: scan overview friction category: %w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Server) hasColumn(ctx context.Context, table, column string) (bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, fmt.Errorf("api: inspect %s: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *Server) topTags(ctx context.Context, where string, args []any) ([]tagFacet, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.tag, t.kind, COUNT(*)
		FROM session_tags t JOIN sessions s ON s.id = t.session_id
		LEFT JOIN session_stats st ON st.session_id = s.id`+where+`
		GROUP BY t.tag, t.kind ORDER BY COUNT(*) DESC, t.tag LIMIT 12`, args...)
	if err != nil {
		return nil, fmt.Errorf("api: overview tags: %w", err)
	}
	return scanTagFacets(rows, "overview tag")
}

func (s *Server) assetAttention(ctx context.Context) (map[string]int, error) {
	var total, attention int
	err := s.db.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(*) FROM assets WHERE archived_at IS NULL),
		       (SELECT COUNT(*) FROM vital_states
		        WHERE ended_at IS NULL AND (broken_overlay = 1 OR state IN ('silent', 'broken', 'bypassed')))`).
		Scan(&total, &attention)
	if err != nil {
		return nil, fmt.Errorf("api: asset attention: %w", err)
	}
	return map[string]int{"total": total, "attention": attention}, nil
}
