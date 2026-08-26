package api

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// unrecordedKey is the explicit bucket for a dimension the source did not
// record — a session with no working directory, a friction record the
// classifier could not categorise. Such rows are grouped under it, never
// dropped and never rendered as an empty name.
const unrecordedKey = "__unrecorded__"

// projectKeyOf reads the stored grouping key. It is a column rather than an
// expression over cwd because a session run from a harness worktree belongs to
// the repository above it (eventstore.ProjectKeyOf).
func projectKeyOf(projectKey sql.NullString) string {
	if !projectKey.Valid || strings.TrimSpace(projectKey.String) == "" {
		return unrecordedKey
	}
	return projectKey.String
}

func projectLabelOf(key string) string {
	if key == unrecordedKey {
		return "项目未记录"
	}
	base := filepath.Base(filepath.Clean(key))
	if base == "" || base == "." || base == string(filepath.Separator) {
		return key
	}
	return base
}

type projectResponse struct {
	Key            string         `json:"key"`
	Label          string         `json:"label"`
	CWD            string         `json:"cwd,omitempty"`
	Sessions       int            `json:"sessions"`
	FrictionCount  int            `json:"friction_count"`
	FirstStartedAt *string        `json:"first_started_at"`
	LastStartedAt  *string        `json:"last_started_at"`
	Harnesses      map[string]int `json:"harnesses"`
	// IsHomeDir marks a working directory that is the home directory itself or
	// a bare folder directly under it with no git checkout. It is a label on
	// the row, not a grouping rule: these sessions stay exactly where they are.
	IsHomeDir bool `json:"is_home_dir"`
}

// isHomeDir applies the rule above. The home directory is read from the
// environment, and the git check is a single read-only stat; a directory that
// cannot be read is not marked.
func isHomeDir(key string) bool {
	if key == unrecordedKey || strings.TrimSpace(key) == "" {
		return false
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return false
	}
	cwd := filepath.Clean(key)
	home = filepath.Clean(home)
	if cwd == home {
		return true
	}
	if filepath.Dir(cwd) != home {
		return false
	}
	_, err = os.Stat(filepath.Join(cwd, ".git"))
	return err != nil
}

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.projects(r.Context(), "", nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": projects, "data_version": s.dataVersion()})
}

// projects aggregates sessions by their recorded working directory. where/args
// let the overview restrict the same aggregation to a time range.
func (s *Server) projects(ctx context.Context, where string, args []any) ([]projectResponse, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT COALESCE(s.project_key, '`+unrecordedKey+`') AS project_key, s.source,
		       COUNT(*), SUM(COALESCE(st.friction_count, 0)),
		       MIN(NULLIF(s.started_at, '')), MAX(NULLIF(s.started_at, ''))
		FROM sessions s LEFT JOIN session_stats st ON st.session_id = s.id`+where+`
		GROUP BY project_key, s.source`, args...)
	if err != nil {
		return nil, fmt.Errorf("api: aggregate projects: %w", err)
	}
	defer rows.Close()
	byKey := make(map[string]*projectResponse)
	for rows.Next() {
		var key, source string
		var sessions, friction int
		var first, last sql.NullString
		if err := rows.Scan(&key, &source, &sessions, &nullableInt{&friction}, &first, &last); err != nil {
			return nil, fmt.Errorf("api: scan project: %w", err)
		}
		item, ok := byKey[key]
		if !ok {
			item = &projectResponse{Key: key, Label: projectLabelOf(key), Harnesses: make(map[string]int),
				IsHomeDir: isHomeDir(key)}
			if key != unrecordedKey {
				item.CWD = key
			}
			byKey[key] = item
		}
		item.Sessions += sessions
		item.FrictionCount += friction
		item.Harnesses[source] += sessions
		item.FirstStartedAt = earlier(item.FirstStartedAt, first)
		item.LastStartedAt = later(item.LastStartedAt, last)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]projectResponse, 0, len(byKey))
	for _, item := range byKey {
		out = append(out, *item)
	}
	sort.Slice(out, func(i, j int) bool {
		left, right := out[i].LastStartedAt, out[j].LastStartedAt
		if (left == nil) != (right == nil) {
			return right == nil
		}
		if left != nil && *left != *right {
			return *left > *right
		}
		return out[i].Key < out[j].Key
	})
	return out, nil
}

func earlier(current *string, candidate sql.NullString) *string {
	if !candidate.Valid {
		return current
	}
	if current == nil || candidate.String < *current {
		value := candidate.String
		return &value
	}
	return current
}

func later(current *string, candidate sql.NullString) *string {
	if !candidate.Valid {
		return current
	}
	if current == nil || candidate.String > *current {
		value := candidate.String
		return &value
	}
	return current
}

// handleProject is the project page: everything the local record holds about
// one working directory. The key is the recorded cwd, percent-encoded, or the
// explicit unrecorded key for sessions that never named one.
func (s *Server) handleProject(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key == "" {
		key = r.URL.Query().Get("key")
	}
	if strings.TrimSpace(key) == "" {
		http.Error(w, "project key is required", http.StatusBadRequest)
		return
	}
	filter, err := parseAggregateFilter(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	filter = filter.withProject(key)
	ctx := r.Context()
	fail := func(err error) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	header, err := s.projectHeader(ctx, key)
	if err != nil {
		fail(err)
		return
	}
	if header == nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	response := map[string]any{
		"project": header, "range": filter.rangeSpec(), "data_version": s.dataVersion(),
	}

	scope, err := s.mainSessionScope(ctx)
	if err != nil {
		fail(err)
		return
	}
	sessionCounts, duration, err := s.projectSessionCounts(ctx, filter, scope)
	if err != nil {
		fail(err)
		return
	}
	response["sessions"], response["duration"] = sessionCounts, duration

	usageWhere, usageArgs := filter.where()
	usageAggregate, err := s.aggregateUsage(ctx, " WHERE 1 = 1"+usageWhere, usageArgs)
	if err != nil {
		fail(err)
		return
	}
	response["usage"] = usageAggregate

	byModel, err := s.aggregateModelUsage(ctx, " WHERE 1 = 1"+usageWhere, usageArgs)
	if err != nil {
		fail(err)
		return
	}
	response["by_model"] = byModel

	byWeek, err := s.weeklyActivity(ctx, filter, scope)
	if err != nil {
		fail(err)
		return
	}
	response["by_week"] = byWeek

	models, err := s.projectFacet(ctx, filter, "COALESCE(NULLIF(s.model, ''), '"+unrecordedKey+"')")
	if err != nil {
		fail(err)
		return
	}
	response["models"] = models

	response["roles"] = []facetCount{}
	hasRole, err := s.hasColumn(ctx, "sessions", "agent_role")
	if err != nil {
		fail(err)
		return
	}
	if hasRole {
		roles, err := s.projectFacet(ctx, filter, "COALESCE(NULLIF(s.agent_role, ''), '"+unrecordedKey+"')")
		if err != nil {
			fail(err)
			return
		}
		response["roles"] = roles
	}

	tags, err := s.projectTags(ctx, filter)
	if err != nil {
		fail(err)
		return
	}
	response["tags"] = tags

	frictionBlock, err := s.projectFriction(ctx, key, filter, frictionWindowDays(r.URL.Query().Get("window")))
	if err != nil {
		fail(err)
		return
	}
	response["friction"] = frictionBlock

	// A project page shows the files of the project. Everything the sessions
	// touched elsewhere — a scratch directory, another checkout — is counted
	// but not mixed into the hot list.
	pathPrefix := ""
	if key != unrecordedKey {
		pathPrefix = strings.TrimRight(key, "/\\") + "/"
	}
	files, outside, err := s.hotFiles(ctx, filter, pathPrefix, "", 30)
	if err != nil {
		fail(err)
		return
	}
	response["hot_files"], response["outside_project_files"] = files, outside

	programs, err := s.topPrograms(ctx, filter, 20)
	if err != nil {
		fail(err)
		return
	}
	response["top_programs"] = programs

	recent, err := s.projectRecentSessions(ctx, key, r)
	if err != nil {
		fail(err)
		return
	}
	response["recent_sessions"] = recent

	assetCounts, err := s.projectAssets(ctx, filter)
	if err != nil {
		fail(err)
		return
	}
	response["assets"] = assetCounts

	// The project page answers the same "what happened in this stretch"
	// question as the overview, over one working directory, and under the same
	// counting rule — so current.sessions is sessions.main_non_empty, not
	// sessions.in_range.
	response["scope"] = map[string]any{
		"main_sessions_only": scope != "", "excludes_empty": scope != "",
		"note":    "current/previous 只统计 thread_kind='main' 且非空会话；页面顶部的 sessions.in_range 是该项目的全部会话。",
		"note_en": "current and previous count only sessions with thread_kind='main' that are not empty; sessions.in_range at the top of the page is every session of this project.",
	}
	if err := s.attachPeriod(ctx, response, projectWindow(filter),
		periodScope{conditions: []string{projectKeyCondition(key)}, args: projectKeyArgs(key), scope: scope},
		wantsCompare(r.URL.Query().Get("compare"))); err != nil {
		fail(err)
		return
	}

	writeJSON(w, http.StatusOK, response)
}

// projectWindow is the range the project page was asked for, in the shape the
// period summary reads.
func projectWindow(filter aggregateFilter) overviewRange {
	return overviewRange{From: filter.From, To: filter.To}
}

func projectKeyCondition(key string) string {
	if key == unrecordedKey {
		return "s.project_key IS NULL"
	}
	return "s.project_key = ?"
}

func projectKeyArgs(key string) []any {
	if key == unrecordedKey {
		return nil
	}
	return []any{key}
}

func (f aggregateFilter) withProject(key string) aggregateFilter {
	out := aggregateFilter{From: f.From, To: f.To}
	out.conditions = append(out.conditions, f.conditions...)
	out.args = append(out.args, f.args...)
	if key == unrecordedKey {
		out.conditions = append(out.conditions, "s.project_key IS NULL")
		return out
	}
	out.conditions = append(out.conditions, "s.project_key = ?")
	out.args = append(out.args, key)
	return out
}

// projectHeader is the all-time identity of the project; it deliberately
// ignores the range filter so the page can say how long the project has been
// worked on at all.
func (s *Server) projectHeader(ctx context.Context, key string) (map[string]any, error) {
	condition := "s.project_key = ?"
	args := []any{key}
	if key == unrecordedKey {
		condition, args = "s.project_key IS NULL", nil
	}
	var sessions, worktreeSessions, worktrees int
	var first, last sql.NullString
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), MIN(NULLIF(s.started_at, '')), MAX(NULLIF(s.started_at, '')),
		       SUM(CASE WHEN s.worktree IS NOT NULL THEN 1 ELSE 0 END),
		       COUNT(DISTINCT s.worktree)
		FROM sessions s WHERE `+condition, args...).
		Scan(&sessions, &first, &last, &nullableInt{&worktreeSessions}, &worktrees); err != nil {
		return nil, fmt.Errorf("api: project header: %w", err)
	}
	if sessions == 0 {
		return nil, nil
	}
	harnesses, err := s.projectHeaderCounts(ctx, condition, args, "s.source")
	if err != nil {
		return nil, err
	}
	originators := map[string]int{}
	hasOriginator, err := s.hasColumn(ctx, "sessions", "originator")
	if err != nil {
		return nil, err
	}
	if hasOriginator {
		originators, err = s.projectHeaderCounts(ctx, condition, args, "COALESCE(NULLIF(s.originator, ''), '"+unrecordedKey+"')")
		if err != nil {
			return nil, err
		}
	}
	header := map[string]any{
		"key": key, "label": projectLabelOf(key), "sessions": sessions,
		"first_started_at": optionalString(first), "last_started_at": optionalString(last),
		"harnesses": harnesses, "originators": originators,
		"is_home_dir": isHomeDir(key),
		// Sessions the harness ran from a worktree of this repository. They are
		// counted as this project's sessions; this says how many of them ran
		// somewhere other than the checkout itself.
		"worktree_sessions": worktreeSessions, "worktrees": worktrees,
	}
	if key != unrecordedKey {
		header["cwd"] = key
	} else {
		header["cwd"] = nil
	}
	return header, nil
}

func (s *Server) projectHeaderCounts(ctx context.Context, condition string, args []any, expr string) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+expr+` AS bucket, COUNT(*) FROM sessions s WHERE `+condition+` GROUP BY bucket`, args...)
	if err != nil {
		return nil, fmt.Errorf("api: project header counts: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var bucket string
		var count int
		if err := rows.Scan(&bucket, &count); err != nil {
			return nil, fmt.Errorf("api: scan project header count: %w", err)
		}
		out[bucket] = count
	}
	return out, rows.Err()
}

func (s *Server) projectSessionCounts(ctx context.Context, filter aggregateFilter, scope string) (map[string]any, map[string]any, error) {
	where, args := filter.where()
	var inRange, knownDuration, totalDuration int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), SUM(CASE WHEN st.duration_ms IS NULL THEN 0 ELSE 1 END),
		       SUM(COALESCE(st.duration_ms, 0))
		FROM sessions s LEFT JOIN session_stats st ON st.session_id = s.id
		WHERE 1 = 1`+where, args...).
		Scan(&inRange, &nullableInt{&knownDuration}, &nullableInt{&totalDuration}); err != nil {
		return nil, nil, fmt.Errorf("api: project session counts: %w", err)
	}
	counts := map[string]any{"in_range": inRange}
	// main_non_empty is the denominator the period summary of this page counts
	// over, so the two numbers on the page can be checked against each other.
	var mainNonEmpty int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sessions s LEFT JOIN session_stats st ON st.session_id = s.id
		WHERE 1 = 1`+where+scope, args...).Scan(&mainNonEmpty); err != nil {
		return nil, nil, fmt.Errorf("api: project main sessions: %w", err)
	}
	counts["main_non_empty"] = mainNonEmpty
	hasThread, err := s.hasColumn(ctx, "sessions", "thread_kind")
	if err != nil {
		return nil, nil, err
	}
	if hasThread {
		var main, subagent int
		if err := s.db.QueryRowContext(ctx, `
			SELECT SUM(CASE WHEN s.thread_kind IS NULL OR s.thread_kind = 'main' THEN 1 ELSE 0 END),
			       SUM(CASE WHEN s.thread_kind = 'subagent' THEN 1 ELSE 0 END)
			FROM sessions s LEFT JOIN session_stats st ON st.session_id = s.id
			WHERE 1 = 1`+where, args...).Scan(&nullableInt{&main}, &nullableInt{&subagent}); err != nil {
			return nil, nil, fmt.Errorf("api: project thread counts: %w", err)
		}
		counts["main"], counts["subagent"] = main, subagent
	}
	hasEmpty, err := s.hasColumn(ctx, "session_stats", "is_empty")
	if err != nil {
		return nil, nil, err
	}
	if hasEmpty {
		var empty int
		if err := s.db.QueryRowContext(ctx, `
			SELECT SUM(CASE WHEN COALESCE(st.is_empty, 0) = 1 THEN 1 ELSE 0 END)
			FROM sessions s LEFT JOIN session_stats st ON st.session_id = s.id
			WHERE 1 = 1`+where, args...).Scan(&nullableInt{&empty}); err != nil {
			return nil, nil, fmt.Errorf("api: project empty sessions: %w", err)
		}
		counts["empty"] = empty
	}
	return counts, map[string]any{"known_sessions": knownDuration, "total_ms": totalDuration}, nil
}

func (s *Server) projectFacet(ctx context.Context, filter aggregateFilter, expr string) ([]facetCount, error) {
	where, args := filter.where()
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+expr+` AS bucket, COUNT(*)
		FROM sessions s LEFT JOIN session_stats st ON st.session_id = s.id
		WHERE 1 = 1`+where+`
		GROUP BY bucket ORDER BY 2 DESC, 1 LIMIT 50`, args...)
	if err != nil {
		return nil, fmt.Errorf("api: project facet: %w", err)
	}
	return scanFacetCounts(rows, "project facet")
}

func (s *Server) projectTags(ctx context.Context, filter aggregateFilter) ([]tagFacet, error) {
	where, args := filter.where()
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.tag, t.kind, COUNT(*)
		FROM session_tags t JOIN sessions s ON s.id = t.session_id
		WHERE 1 = 1`+where+`
		GROUP BY t.tag, t.kind ORDER BY 3 DESC, t.tag LIMIT 30`, args...)
	if err != nil {
		return nil, fmt.Errorf("api: project tags: %w", err)
	}
	return scanTagFacets(rows, "project tag")
}

// projectFriction reuses the friction endpoint's own aggregation so the
// project page and the friction page cannot disagree about a count.
func (s *Server) projectFriction(ctx context.Context, key string, filter aggregateFilter, window int) (map[string]any, error) {
	filters := frictionFilters{Project: key, Sort: "sessions", Group: "signature", Limit: 10, Window: window}
	if filter.From != nil {
		filters.From = *filter.From
	}
	if filter.To != nil {
		filters.To = *filter.To
	}
	set, err := s.loadFrictionSet(ctx, filters)
	if err != nil {
		return nil, err
	}
	summary := set.summary()
	recurring, err := s.signatureGroups(ctx, set, filters)
	if err != nil {
		return nil, err
	}
	lifecycle, err := s.frictionLifecycle(ctx, set, filters)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"total": summary.TotalEvents, "sessions": summary.SessionCount,
		"recurring_signatures": summary.RecurringSignatures,
		"expected_exit_count":  summary.ExpectedExitCount,
		"by_category":          summary.ByCategory, "by_tool": summary.ByTool,
		"by_hint_kind": summary.ByHintKind,
		"recurring":    recurring,
		"lifecycle":    lifecycle,
	}, nil
}

func (s *Server) projectRecentSessions(ctx context.Context, key string, r *http.Request) ([]sessionResponse, error) {
	query, err := parseSessionQuery(r)
	if err != nil {
		return nil, err
	}
	query.conditions = append(query.conditions, projectCondition(key))
	query.sortKey, query.limit, query.offset, query.deep = "recent", 8, 0, false
	items, _, err := s.querySessions(ctx, query)
	return items, err
}

func projectCondition(key string) sessionCondition {
	if key == unrecordedKey {
		return sessionCondition{dimension: "project", expr: "s.project_key IS NULL"}
	}
	return sessionCondition{dimension: "project", expr: "s.project_key = ?", args: []any{key}}
}

// projectAssets counts the assets that actually participated in this project's
// sessions, not the whole local asset inventory.
func (s *Server) projectAssets(ctx context.Context, filter aggregateFilter) (map[string]int, error) {
	where, args := filter.where()
	var total, attention int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT av.asset_id),
		       COUNT(DISTINCT CASE WHEN EXISTS (
		           SELECT 1 FROM vital_states v
		           WHERE v.asset_id = av.asset_id AND v.ended_at IS NULL
		             AND (v.broken_overlay = 1 OR v.state IN ('silent', 'broken', 'bypassed'))
		       ) THEN av.asset_id END)
		FROM participations p
		JOIN asset_versions av ON av.id = p.asset_version_id
		JOIN sessions s ON s.id = p.session_id
		WHERE p.superseded_at IS NULL`+where, args...).Scan(&total, &attention); err != nil {
		return nil, fmt.Errorf("api: project assets: %w", err)
	}
	return map[string]int{"total": total, "attention": attention}, nil
}
