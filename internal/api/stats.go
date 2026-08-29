package api

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"time"
)

type statsResponse struct {
	AssetCount         int            `json:"asset_count"`
	VersionCount       int            `json:"version_count"`
	SessionCount       int            `json:"session_count"`
	EventCount         int            `json:"event_count"`
	OpportunityCount   int            `json:"opportunity_count"`
	ParticipationCount int            `json:"participation_count"`
	StateCounts        map[string]int `json:"state_counts"`
	SourceCounts       map[string]int `json:"source_counts"`
	ObservationLevels  map[string]int `json:"observation_levels"`
	ActivityByDay      map[string]int `json:"activity_by_day"`
	LastEventAt        *time.Time     `json:"last_event_at,omitempty"`
	// Usage is the whole store's measurement, read the same way the overview
	// reads its window's. Without it the data page said "token 未记录" while
	// the overview printed 25.9B — one system, two answers.
	Usage   usageTotals       `json:"usage"`
	ByModel []modelUsageTotal `json:"by_model"`
	// Rescued states the archive fact (P17-3): sessions whose source
	// transcript is no longer on disk — harnesses delete them after their
	// retention window — while this store still holds their history.
	Rescued rescuedResponse `json:"rescued_transcripts"`
}

type rescuedResponse struct {
	Sessions int    `json:"sessions"`
	Files    int    `json:"files"`
	Note     string `json:"note"`
	NoteEN   string `json:"note_en"`
}

const rescuedNote = "已抢救 = 登记过的转写文件如今在磁盘上已不存在（harness 按保留期删除，Claude Code 默认 30 天），而本库仍保有其完整事件历史。逐个文件按当前磁盘状态判定；文件还在的不算。"

const rescuedNoteEN = "Rescued means a registered transcript file no longer exists on disk — harnesses delete them after their retention window, 30 days by default for Claude Code — while this store still holds its full event history. Each file is checked against the disk as it is now; a file still present does not count."

// countRescued stats every registered transcript path against the disk. The
// sweep runs once per data version thanks to the response cache; a path that
// cannot be stat-ed for any reason counts as gone, because it is not readable
// as a source either way.
func (s *Server) countRescued(ctx context.Context) (rescuedResponse, error) {
	out := rescuedResponse{Note: rescuedNote, NoteEN: rescuedNoteEN}
	rows, err := s.db.QueryContext(ctx,
		`SELECT path, COALESCE(session_id, '') FROM native_files`)
	if err != nil {
		return out, fmt.Errorf("api: list native files: %w", err)
	}
	defer rows.Close()
	sessions := make(map[string]struct{})
	for rows.Next() {
		var path, sessionID string
		if err := rows.Scan(&path, &sessionID); err != nil {
			return out, err
		}
		if _, err := os.Stat(path); err == nil {
			continue
		}
		out.Files++
		if sessionID != "" {
			sessions[sessionID] = struct{}{}
		}
	}
	out.Sessions = len(sessions)
	return out, rows.Err()
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats := statsResponse{StateCounts: make(map[string]int), SourceCounts: make(map[string]int), ObservationLevels: make(map[string]int), ActivityByDay: make(map[string]int)}
	queries := []struct {
		target *int
		query  string
	}{
		{&stats.AssetCount, `SELECT COUNT(*) FROM assets`},
		{&stats.VersionCount, `SELECT COUNT(*) FROM asset_versions`},
		{&stats.SessionCount, `SELECT COUNT(*) FROM sessions`},
		{&stats.EventCount, `SELECT COUNT(*) FROM events`},
		{&stats.OpportunityCount, `SELECT COUNT(*) FROM opportunities WHERE superseded_at IS NULL`},
		{&stats.ParticipationCount, `SELECT COUNT(*) FROM participations WHERE superseded_at IS NULL`},
	}
	for _, item := range queries {
		if err := s.db.QueryRowContext(r.Context(), item.query).Scan(item.target); err != nil {
			http.Error(w, "stats query failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if rescued, err := s.countRescued(r.Context()); err != nil {
		http.Error(w, "rescue count failed: "+err.Error(), http.StatusInternalServerError)
		return
	} else {
		stats.Rescued = rescued
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT state, COUNT(*) FROM vital_states WHERE ended_at IS NULL GROUP BY state ORDER BY state`)
	if err != nil {
		http.Error(w, "state stats query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	for rows.Next() {
		var state string
		var count int
		if err := rows.Scan(&state, &count); err != nil {
			rows.Close()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		stats.StateCounts[state] = count
	}
	if !closeRows(w, rows) {
		return
	}
	rows, err = s.db.QueryContext(r.Context(), `SELECT source, COUNT(*) FROM sessions GROUP BY source ORDER BY source`)
	if err != nil {
		http.Error(w, "source stats query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	for rows.Next() {
		var source string
		var count int
		if err := rows.Scan(&source, &count); err != nil {
			rows.Close()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		stats.SourceCounts[source] = count
	}
	if !closeRows(w, rows) {
		return
	}
	rows, err = s.db.QueryContext(r.Context(), `SELECT observation_level, COUNT(*) FROM participations WHERE superseded_at IS NULL GROUP BY observation_level ORDER BY observation_level`)
	if err != nil {
		http.Error(w, "observation stats query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	for rows.Next() {
		var level string
		var count int
		if err := rows.Scan(&level, &count); err != nil {
			rows.Close()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		stats.ObservationLevels[level] = count
	}
	if !closeRows(w, rows) {
		return
	}
	rows, err = s.db.QueryContext(r.Context(), `
		SELECT substr(occurred_at, 1, 10), COUNT(*)
		FROM events
		WHERE occurred_at IS NOT NULL AND occurred_at <> ''
		GROUP BY substr(occurred_at, 1, 10)
		ORDER BY substr(occurred_at, 1, 10)`)
	if err != nil {
		http.Error(w, "daily activity stats query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	for rows.Next() {
		var day string
		var count int
		if err := rows.Scan(&day, &count); err != nil {
			rows.Close()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if day != "" {
			stats.ActivityByDay[day] = count
		}
	}
	if !closeRows(w, rows) {
		return
	}
	var last sql.NullString
	if err := s.db.QueryRowContext(r.Context(), `SELECT MAX(occurred_at) FROM events WHERE occurred_at IS NOT NULL AND occurred_at <> ''`).Scan(&last); err != nil {
		http.Error(w, "last event query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if last.Valid && last.String != "" {
		parsed, err := time.Parse(time.RFC3339Nano, last.String)
		if err != nil {
			http.Error(w, "last event timestamp is invalid: "+err.Error(), http.StatusInternalServerError)
			return
		}
		stats.LastEventAt = &parsed
	}
	usage, err := s.aggregateUsage(r.Context(), "", nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	stats.Usage = usage
	byModel, err := s.aggregateModelUsage(r.Context(), "", nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	stats.ByModel = byModel
	writeJSON(w, http.StatusOK, stats)
}
