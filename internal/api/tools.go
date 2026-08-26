package api

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
)

// aggregateFilter is the range-and-dimension filter the aggregate endpoints
// share. Every condition is written against the sessions alias `s`, so the
// same filter can be appended to any query that joins sessions.
type aggregateFilter struct {
	conditions []string
	args       []any
	From       *string
	To         *string
}

func (f aggregateFilter) where() (string, []any) {
	if len(f.conditions) == 0 {
		return "", nil
	}
	return " AND " + strings.Join(f.conditions, " AND "), f.args
}

func (f aggregateFilter) rangeSpec() map[string]any {
	return map[string]any{"from": f.From, "to": f.To}
}

// sessionJoin is the join the filter needs to reach the sessions table, and
// nothing when the filter is empty. Every condition is written against the
// alias `s`, so with no conditions there is nothing to join to — and skipping
// it is what keeps the unfiltered aggregate from walking sessions once per
// projection row.
func (f aggregateFilter) sessionJoin(alias string) string {
	if len(f.conditions) == 0 {
		return ""
	}
	return " JOIN sessions s ON s.id = " + alias + ".session_id"
}

func parseAggregateFilter(r *http.Request) (aggregateFilter, error) {
	values := r.URL.Query()
	var filter aggregateFilter
	if projects := values["project"]; len(projects) > 0 {
		parts := make([]string, 0, len(projects))
		for _, project := range projects {
			if project == unrecordedKey {
				parts = append(parts, "s.project_key IS NULL")
				continue
			}
			parts = append(parts, "s.project_key = ?")
			filter.args = append(filter.args, project)
		}
		filter.conditions = append(filter.conditions, "("+strings.Join(parts, " OR ")+")")
	}
	if harnesses := values["harness"]; len(harnesses) > 0 {
		placeholders := make([]string, len(harnesses))
		for index, harness := range harnesses {
			placeholders[index] = "?"
			filter.args = append(filter.args, harness)
		}
		filter.conditions = append(filter.conditions, "s.source IN ("+strings.Join(placeholders, ", ")+")")
	}
	window, err := rangeWindow(values, "")
	if err != nil {
		return aggregateFilter{}, err
	}
	if window.From != nil {
		filter.conditions = append(filter.conditions, "s.started_at >= ?")
		filter.args = append(filter.args, *window.From)
		filter.From = window.From
	}
	if window.To != nil {
		filter.conditions = append(filter.conditions, "s.started_at <= ?")
		filter.args = append(filter.args, *window.To)
		filter.To = window.To
	}
	return filter, nil
}

// hasTable reports whether a table exists. The session hierarchy projections
// land in a separate migration, so every endpoint that reads them degrades to
// an empty result instead of failing while that migration is not applied.
func (s *Server) hasTable(ctx context.Context, table string) (bool, error) {
	var name string
	err := s.db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("api: inspect table %s: %w", table, err)
	}
	return true, nil
}

// The two names a counting scope can have. They are what /overview reports as
// scope.key, so a reader branches on one word rather than on two booleans.
const (
	scopeMainNonEmpty = "main_non_empty"
	scopeAll          = "all"
)

// mainSessionScope is the default counting rule of the second stage: a
// subagent thread and an empty session are not what the user means by "a
// session". When the hierarchy columns are not there yet the rule cannot be
// applied at all, so it is dropped rather than guessed.
func (s *Server) mainSessionScope(ctx context.Context) (string, error) {
	hasThread, err := s.hasColumn(ctx, "sessions", "thread_kind")
	if err != nil || !hasThread {
		return "", err
	}
	hasEmpty, err := s.hasColumn(ctx, "session_stats", "is_empty")
	if err != nil {
		return "", err
	}
	// A session whose thread kind was never recorded is not known to be a
	// subagent, so it stays counted: missing is not the same as excluded.
	// This is the same rule the session list applies for thread=main.
	scope := " AND (s.thread_kind IS NULL OR s.thread_kind = 'main')"
	if hasEmpty {
		scope += " AND COALESCE(st.is_empty, 0) = 0"
	}
	return scope, nil
}

type toolUsage struct {
	ToolName string `json:"tool_name"`
	Harness  string `json:"harness"`
	Calls    int    `json:"calls"`
	Sessions int    `json:"sessions"`
	// KnownOutcomes is how many of those calls have a paired result that
	// recorded an outcome at all. Failures is counted out of it, not out of
	// Calls: a call whose result the source never recorded is neither.
	KnownOutcomes int `json:"known_outcomes"`
	Failures      int `json:"failures"`
}

type programUsage struct {
	Program string `json:"program"`
	Calls   int    `json:"calls"`
	// KnownOutcomes is how many of those calls recorded an outcome at all.
	// Failures is counted out of it, not out of Calls: a command whose exit
	// code the source never recorded is neither a success nor a failure.
	KnownOutcomes int `json:"known_outcomes"`
	Sessions      int `json:"sessions"`
	Failures      int `json:"failures"`
	// ExpectedExits are the nonzero exits this program documents as an answer
	// rather than as a failure — `rg` exiting 1 means nothing matched. They are
	// known outcomes, and they are not failures.
	ExpectedExits int `json:"expected_exits"`
	// Family groups the versioned names of one program — python, python3,
	// python3.12 — so a page can add them up. The recorded name is kept as it
	// was: the grouping is offered here, never applied in storage.
	Family string `json:"family"`
}

// programFamilies are the prefixes whose versioned names name one program.
var programFamilies = []string{"python", "pip", "node", "ruby", "perl", "php", "gcc", "clang", "llvm"}

// programFamily is the name the versioned variants of a program share, or the
// program itself when it has none.
func programFamily(program string) string {
	trimmed := strings.TrimRight(program, "0123456789.")
	for _, family := range programFamilies {
		if trimmed == family {
			return family
		}
	}
	return program
}

func (s *Server) handleTools(w http.ResponseWriter, r *http.Request) {
	filter, err := parseAggregateFilter(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	tools, err := s.toolUsage(ctx, filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	programs, err := s.topPrograms(ctx, filter, 200)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tools": tools, "programs": programs,
		"range": filter.rangeSpec(), "data_version": s.dataVersion(),
	})
}

// toolUsage reads the per-session tool projection instead of scanning every
// tool-call payload. Failures come from the result each call was paired with,
// so a tool that is not the shell is covered too, and known_outcomes says how
// many of the calls the source actually reported an outcome for. A tool the
// source did not name is counted under the explicit unrecorded key rather than
// dropped.
func (s *Server) toolUsage(ctx context.Context, filter aggregateFilter) ([]toolUsage, error) {
	out := make([]toolUsage, 0)
	has, err := s.hasTable(ctx, "tool_call_stats")
	if err != nil || !has {
		return out, err
	}
	where, args := filter.where()
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.tool_name, t.harness, SUM(t.calls), COUNT(DISTINCT t.session_id),
		       SUM(t.known_outcomes), SUM(t.failures)
		FROM tool_call_stats t`+filter.sessionJoin("t")+`
		WHERE 1 = 1`+where+`
		GROUP BY t.tool_name, t.harness
		ORDER BY 3 DESC, 1`, args...)
	if err != nil {
		return nil, fmt.Errorf("api: tool usage: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item toolUsage
		if err := rows.Scan(&item.ToolName, &item.Harness, &item.Calls, &item.Sessions,
			&nullableInt{&item.KnownOutcomes}, &nullableInt{&item.Failures}); err != nil {
			return nil, fmt.Errorf("api: scan tool usage: %w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// topPrograms reads the command projection. failures counts a command the
// source recorded as failing; a command with no recorded exit code is not
// counted as either a success or a failure.
func (s *Server) topPrograms(ctx context.Context, filter aggregateFilter, limit int) ([]programUsage, error) {
	out := make([]programUsage, 0)
	has, err := s.hasTable(ctx, "session_commands")
	if err != nil || !has {
		return out, err
	}
	where, args := filter.where()
	rows, err := s.db.QueryContext(ctx, `
		SELECT COALESCE(NULLIF(TRIM(COALESCE(c.program, '')), ''), '`+unrecordedKey+`') AS program,
		       COUNT(*),
		       SUM(CASE WHEN c.exit_code IS NOT NULL OR c.is_error IS NOT NULL THEN 1 ELSE 0 END),
		       COUNT(DISTINCT c.session_id),
		       SUM(CASE WHEN c.expected_exit = 0
		                 AND (c.is_error = 1 OR (c.exit_code IS NOT NULL AND c.exit_code != 0)) THEN 1 ELSE 0 END),
		       SUM(c.expected_exit)
		FROM session_commands c`+filter.sessionJoin("c")+`
		WHERE 1 = 1`+where+`
		GROUP BY program
		ORDER BY 2 DESC, 1
		LIMIT ?`, append(append([]any{}, args...), limit)...)
	if err != nil {
		return nil, fmt.Errorf("api: top programs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item programUsage
		if err := rows.Scan(&item.Program, &item.Calls, &nullableInt{&item.KnownOutcomes},
			&item.Sessions, &nullableInt{&item.Failures}, &nullableInt{&item.ExpectedExits}); err != nil {
			return nil, fmt.Errorf("api: scan top program: %w", err)
		}
		item.Family = programFamily(item.Program)
		out = append(out, item)
	}
	return out, rows.Err()
}

type hotFile struct {
	Path     string  `json:"path"`
	Sessions int     `json:"sessions"`
	Reads    int     `json:"reads"`
	Edits    int     `json:"edits"`
	Writes   int     `json:"writes"`
	Deletes  int     `json:"deletes"`
	LastAt   *string `json:"last_at"`
}

// hotFiles reads the file projection. pathPrefix keeps a project page to the
// files under the project's own working directory; excludePrefix drops the
// scratch directories that are not part of any project. outside counts what
// the prefix rule excluded, so the number is reported rather than lost.
func (s *Server) hotFiles(ctx context.Context, filter aggregateFilter, pathPrefix, excludePrefix string, limit int) ([]hotFile, int, error) {
	out := make([]hotFile, 0)
	has, err := s.hasTable(ctx, "session_files")
	if err != nil || !has {
		return out, 0, err
	}
	where, args := filter.where()
	scope := ""
	scopeArgs := make([]any, 0, 2)
	if pathPrefix != "" {
		scope += " AND f.path LIKE ? ESCAPE '\\'"
		scopeArgs = append(scopeArgs, likePrefix(pathPrefix))
	}
	if excludePrefix != "" {
		scope += " AND f.path NOT LIKE ? ESCAPE '\\'"
		scopeArgs = append(scopeArgs, likePrefix(excludePrefix))
	}
	var outside int
	if scope != "" {
		outsideArgs := append(append([]any{}, scopeArgs...), args...)
		if err := s.db.QueryRowContext(ctx, `
			SELECT COUNT(DISTINCT f.path)
			FROM session_files f`+filter.sessionJoin("f")+`
			WHERE NOT (1 = 1`+scope+`)`+where, outsideArgs...).Scan(&outside); err != nil {
			return nil, 0, fmt.Errorf("api: count files outside scope: %w", err)
		}
	}
	listArgs := append(append([]any{}, scopeArgs...), args...)
	rows, err := s.db.QueryContext(ctx, `
		SELECT f.path, COUNT(DISTINCT f.session_id),
		       SUM(CASE WHEN f.action = 'read' THEN 1 ELSE 0 END),
		       SUM(CASE WHEN f.action = 'edit' THEN 1 ELSE 0 END),
		       SUM(CASE WHEN f.action = 'write' THEN 1 ELSE 0 END),
		       SUM(CASE WHEN f.action = 'delete' THEN 1 ELSE 0 END),
		       MAX(f.occurred_at)
		FROM session_files f`+filter.sessionJoin("f")+`
		WHERE 1 = 1`+scope+where+`
		GROUP BY f.path
		ORDER BY SUM(CASE WHEN f.action IN ('edit', 'write') THEN 1 ELSE 0 END) DESC,
		         COUNT(DISTINCT f.session_id) DESC, f.path
		LIMIT ?`, append(listArgs, limit)...)
	if err != nil {
		return nil, 0, fmt.Errorf("api: hot files: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item hotFile
		var lastAt sql.NullString
		if err := rows.Scan(&item.Path, &item.Sessions, &nullableInt{&item.Reads}, &nullableInt{&item.Edits},
			&nullableInt{&item.Writes}, &nullableInt{&item.Deletes}, &lastAt); err != nil {
			return nil, 0, fmt.Errorf("api: scan hot file: %w", err)
		}
		if lastAt.Valid {
			value := lastAt.String
			item.LastAt = &value
		}
		out = append(out, item)
	}
	return out, outside, rows.Err()
}

// likePrefix escapes the LIKE wildcards a real path can contain, so a project
// directory called "a_b" does not also match "axb".
func likePrefix(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value) + "%"
}
