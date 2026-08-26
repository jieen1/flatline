package api

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"flatline/internal/adapters"
	"flatline/internal/eventstore"
	"flatline/internal/history"
	"flatline/internal/runtime"
)

// RefreshTrigger is the daemon's manual refresh entry point. The health-only
// server and the tests leave it unset; the API then reports that no daemon is
// attached rather than pretending a pass was started.
type RefreshTrigger interface {
	RequestRefresh() bool
}

// handleIngestRefresh asks the daemon for one refresh pass. It answers 409
// while an import is already running, because the daemon runs its passes
// serially and a second request would not start anything.
func (s *Server) handleIngestRefresh(w http.ResponseWriter, r *http.Request) {
	trigger, ok := s.status.(RefreshTrigger)
	if !ok {
		http.Error(w, "no daemon is attached to this API", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if !trigger.RequestRefresh() {
		writeJSON(w, http.StatusConflict, map[string]any{
			"running": true, "import": s.progress(),
			"message": "an import is already running; its progress is at /api/v1/ingest/status",
		})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"started": true, "import": s.progress(),
		"message": "a refresh pass was queued; poll /api/v1/ingest/status for progress",
	})
}

func (s *Server) progress() runtime.ImportProgress {
	if s.status == nil {
		return runtime.ImportProgress{Phase: runtime.PhaseIdle}
	}
	return s.status.Progress()
}

// handleIngestHealth reports what the daemon knows about its own store. It is
// never cached: the file sizes and the running import change without the data
// version moving.
func (s *Server) handleIngestHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	w.Header().Set("Cache-Control", "no-store")
	schemaVersion, err := s.db.SchemaVersionOf(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	dbBytes, walBytes := s.storeBytes(ctx)
	counts, err := s.healthCounts(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	unrecorded, err := s.unrecordedCounts(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	progress := s.progress()
	warnings := progress.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"schema_version": schemaVersion,
		"db_bytes":       dbBytes,
		"wal_bytes":      walBytes,
		"last_import": map[string]any{
			"phase": progress.Phase, "started_at": progress.StartedAt, "finished_at": progress.FinishedAt,
			"files_seen": progress.FilesSeen, "files_read": progress.FilesRead,
			"files_skipped": progress.FilesSkipped, "sessions_ingested": progress.SessionsIngested,
			"last_error": progress.LastError,
		},
		"warnings": warnings, "unrecorded": unrecorded, "counts": counts,
		"sources":      s.sourceStatuses(ctx),
		"data_version": s.dataVersion(),
	})
}

// storeBytes reports the on-disk size of the database and its write-ahead log.
// A size that cannot be read is reported as null, not as zero.
func (s *Server) storeBytes(ctx context.Context) (*int64, *int64) {
	var path string
	if err := s.db.QueryRowContext(ctx, `SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&path); err != nil || path == "" {
		return nil, nil
	}
	return fileBytes(path), fileBytes(path + "-wal")
}

func fileBytes(path string) *int64 {
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	size := info.Size()
	return &size
}

func (s *Server) healthCounts(ctx context.Context) (map[string]any, error) {
	counts := map[string]any{}
	for name, query := range map[string]string{
		"sessions": `SELECT COUNT(*) FROM sessions`,
		"events":   `SELECT COUNT(*) FROM events`,
		"friction": `SELECT COUNT(*) FROM friction_records`,
		"assets":   `SELECT COUNT(*) FROM assets WHERE archived_at IS NULL`,
	} {
		var value int
		if err := s.db.QueryRowContext(ctx, query).Scan(&value); err != nil {
			return nil, fmt.Errorf("api: health count %s: %w", name, err)
		}
		counts[name] = value
	}
	hasThread, err := s.hasColumn(ctx, "sessions", "thread_kind")
	if err != nil {
		return nil, err
	}
	if hasThread {
		// main_sessions counts what the session list counts under thread=main:
		// a session whose thread kind was never recorded is not known to be a
		// subagent. unrecorded_thread_sessions is that subset, reported so the
		// number is visible rather than absorbed.
		var main, subagent, unrecorded int
		if err := s.db.QueryRowContext(ctx, `
			SELECT SUM(CASE WHEN thread_kind IS NULL OR thread_kind = 'main' THEN 1 ELSE 0 END),
			       SUM(CASE WHEN thread_kind = 'subagent' THEN 1 ELSE 0 END),
			       SUM(CASE WHEN thread_kind IS NULL THEN 1 ELSE 0 END)
			FROM sessions`).Scan(&nullableInt{&main}, &nullableInt{&subagent}, &nullableInt{&unrecorded}); err != nil {
			return nil, fmt.Errorf("api: health thread counts: %w", err)
		}
		counts["main_sessions"], counts["subagent_sessions"], counts["unrecorded_thread_sessions"] = main, subagent, unrecorded
	}
	hasEmpty, err := s.hasColumn(ctx, "session_stats", "is_empty")
	if err != nil {
		return nil, err
	}
	if hasEmpty {
		var empty int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM session_stats WHERE is_empty = 1`).Scan(&empty); err != nil {
			return nil, fmt.Errorf("api: health empty sessions: %w", err)
		}
		counts["empty_sessions"] = empty
	}
	for name, table := range map[string]string{"commands": "session_commands", "files": "session_files"} {
		has, err := s.hasTable(ctx, table)
		if err != nil {
			return nil, err
		}
		if !has {
			continue
		}
		var value int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&value); err != nil {
			return nil, fmt.Errorf("api: health count %s: %w", table, err)
		}
		counts[name] = value
	}
	return counts, nil
}

// unrecordedCounts is the explicit account of what the sources did not record.
// It exists so a blank field on a page can be checked against a number.
func (s *Server) unrecordedCounts(ctx context.Context) (map[string]int, error) {
	out := map[string]int{}
	var withoutTitle, withoutCWD, withoutModel, withoutStart int
	if err := s.db.QueryRowContext(ctx, `
		SELECT SUM(CASE WHEN title IS NULL OR title = '' THEN 1 ELSE 0 END),
		       SUM(CASE WHEN cwd IS NULL OR cwd = '' THEN 1 ELSE 0 END),
		       SUM(CASE WHEN model IS NULL OR model = '' THEN 1 ELSE 0 END),
		       SUM(CASE WHEN started_at IS NULL OR started_at = '' THEN 1 ELSE 0 END)
		FROM sessions`).Scan(&nullableInt{&withoutTitle}, &nullableInt{&withoutCWD},
		&nullableInt{&withoutModel}, &nullableInt{&withoutStart}); err != nil {
		return nil, fmt.Errorf("api: unrecorded session fields: %w", err)
	}
	out["sessions_without_title"] = withoutTitle
	out["sessions_without_cwd"] = withoutCWD
	out["sessions_without_model"] = withoutModel
	out["sessions_without_started_at"] = withoutStart
	var frictionWithoutTool, frictionWithoutCategory int
	if err := s.db.QueryRowContext(ctx, `
		SELECT SUM(CASE WHEN tool_name IS NULL OR tool_name = '' THEN 1 ELSE 0 END),
		       SUM(CASE WHEN category IS NULL OR category = '' THEN 1 ELSE 0 END)
		FROM friction_records`).Scan(&nullableInt{&frictionWithoutTool}, &nullableInt{&frictionWithoutCategory}); err != nil {
		return nil, fmt.Errorf("api: unrecorded friction fields: %w", err)
	}
	out["friction_without_tool"] = frictionWithoutTool
	out["friction_without_category"] = frictionWithoutCategory
	return out, nil
}

// sourceStatuses reports one row per source the daemon looked at: where it
// looked, whether anything was there, and how many sessions are stored. The
// probe half comes from the last discovery pass and the stored half from the
// database, so a source that has files on disk but nothing ingested is visible
// as exactly that rather than as a missing row.
func (s *Server) sourceStatuses(ctx context.Context) []map[string]any {
	stored := map[string]struct {
		sessions int
		lastSeen *string
	}{}
	rows, err := s.db.QueryContext(ctx, `
		SELECT source, COUNT(*), MAX(started_at) FROM sessions GROUP BY source`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var source string
			var count int
			var lastSeen sql.NullString
			if err := rows.Scan(&source, &count, &lastSeen); err != nil {
				continue
			}
			entry := struct {
				sessions int
				lastSeen *string
			}{sessions: count}
			if lastSeen.Valid {
				value := lastSeen.String
				entry.lastSeen = &value
			}
			stored[source] = entry
		}
	}

	// The configured registry is the other half: it says which roots the user
	// named and whether one is turned off. The probe half says what the daemon
	// actually found there this run. They are keyed on (kind, root), so one
	// harness with two roots — a local one and a directory copied from another
	// machine — is two rows, not one.
	configured := map[string]eventstore.Source{}
	registered, err := eventstore.New(s.db).ListSources(ctx)
	if err == nil {
		for _, item := range registered {
			configured[item.Kind+"\x00"+item.Root] = item
		}
	}

	// One root is one row. The registry row is the row — it carries the name
	// the user gave the root and the switch that turns it off — and the probe
	// of the same root is folded into it. Before the first scan of a run that
	// used to produce two rows for the same directory: a registry row and a
	// rootless "not_scanned" row standing in for the stored sessions.
	out := make([]map[string]any, 0, len(configured)+len(stored))
	byRoot := make(map[string]map[string]any, len(configured))
	byKind := make(map[string][]map[string]any, len(configured))
	place := func(kind, root string, row map[string]any) {
		out = append(out, row)
		if root != "" {
			byRoot[kind+"\x00"+normalizedRoot(root)] = row
		}
		byKind[kind] = append(byKind[kind], row)
	}
	for _, source := range configured {
		row := sourceRow(source.Kind, source.Root, "configured", nil, nil, "", "",
			source.Sessions, source.LastSessionAt)
		applyConfiguredSource(row, source)
		place(source.Kind, source.Root, row)
	}
	for _, item := range history.SourceStatuses() {
		if row := matchSourceRow(byRoot, byKind, item.Kind, item.Root); row != nil {
			applyProbedSource(row, item)
			continue
		}
		place(item.Kind, item.Root, sourceRow(item.Kind, item.Root, item.Status, item.Sessions,
			item.LastSeenAt, item.Detail, item.Error, stored[item.Kind].sessions, stored[item.Kind].lastSeen))
	}
	// A source with stored sessions and no row at all means the daemon has not
	// scanned since restart and the root is not registered either. Reporting it
	// as not_found would read as "gone".
	for source, entry := range stored {
		if len(byKind[source]) > 0 {
			continue
		}
		place(source, "", sourceRow(source, "", "not_scanned", nil, nil, "", "", entry.sessions, entry.lastSeen))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i]["kind"].(string) != out[j]["kind"].(string) {
			return out[i]["kind"].(string) < out[j]["kind"].(string)
		}
		return out[i]["root"].(string) < out[j]["root"].(string)
	})
	return out
}

// matchSourceRow finds the row a probe result belongs to: the same root first,
// and failing that the kind's only row — a probe that reports no root at all
// cannot be told apart from the one root that kind has.
func matchSourceRow(byRoot map[string]map[string]any, byKind map[string][]map[string]any, kind, root string) map[string]any {
	if root != "" {
		if row, ok := byRoot[kind+"\x00"+normalizedRoot(root)]; ok {
			return row
		}
	}
	rows := byKind[kind]
	if len(rows) != 1 {
		return nil
	}
	existing, _ := rows[0]["root"].(string)
	if root == "" || existing == "" {
		return rows[0]
	}
	return nil
}

// normalizedRoot is the path two records of the same directory agree on: the
// real path when it resolves, cleaned either way.
func normalizedRoot(root string) string {
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(root)
}

// applyProbedSource folds what this run found at a root into the row the
// registry owns. The probe answers "what is there now"; it never renames the
// root or overrides what the user configured.
func applyProbedSource(row map[string]any, item history.SourceStatus) {
	row["status"] = item.Status
	row["sessions"] = item.Sessions
	row["last_seen_at"] = item.LastSeenAt
	if current, _ := row["root"].(string); current == "" && item.Root != "" {
		row["root"] = item.Root
	}
	if item.Detail != "" {
		row["detail"] = item.Detail
	}
	if item.Error != "" {
		row["error"] = item.Error
	}
}

func applyConfiguredSource(row map[string]any, source eventstore.Source) {
	row["source_id"], row["configured"] = source.ID, true
	row["label"], row["machine_label"], row["enabled"] = source.Label, source.MachineLabel, source.Enabled
	row["stored_sessions"] = source.Sessions
	if source.LastSessionAt != nil {
		row["last_session_at"] = source.LastSessionAt
	}
}

func sourceRow(kind, root, status string, found *int, lastSeen *time.Time, detail, failure string, storedSessions int, storedLastSeen *string) map[string]any {
	row := map[string]any{
		"configured":      false,
		"kind":            kind,
		"display_name":    adapters.Source(kind).DisplayName(),
		"root":            root,
		"status":          status,
		"sessions":        found,
		"stored_sessions": storedSessions,
		"last_seen_at":    lastSeen,
		"last_session_at": storedLastSeen,
	}
	if detail != "" {
		row["detail"] = detail
	}
	if failure != "" {
		row["error"] = failure
	}
	return row
}
