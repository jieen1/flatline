package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
)

type weekActivity struct {
	Week       string `json:"week"`
	Sessions   int    `json:"sessions"`
	DurationMS int64  `json:"duration_ms"`
	ToolCalls  int    `json:"tool_calls"`
	Friction   int    `json:"friction"`
}

func (s *Server) handleTimeStats(w http.ResponseWriter, r *http.Request) {
	filter, err := parseAggregateFilter(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	offset, err := tzOffsetMinutes(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	scope, err := s.mainSessionScope(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	hourWeekday, byWeekday, err := s.hourWeekday(ctx, filter, scope, offset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	byWeek, err := s.weeklyActivity(ctx, filter, scope)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"hour_weekday": hourWeekday, "by_week": byWeek, "by_day_of_week": byWeekday,
		"tz_offset_minutes": offset, "range": filter.rangeSpec(),
		"scope":        "main_sessions_excluding_empty",
		"data_version": s.dataVersion(),
	})
}

// tzOffsetMinutes reads the viewer's offset east of UTC, which is what turns a
// stored UTC timestamp into the hour the person actually worked. A browser
// sends -new Date().getTimezoneOffset(). Missing means UTC.
func tzOffsetMinutes(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("tz_offset_minutes")
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < -14*60 || value > 14*60 {
		return 0, fmt.Errorf("invalid tz_offset_minutes %q", raw)
	}
	return value, nil
}

// hourWeekday returns 7 rows of 24 counts with Monday first, plus the weekday
// totals. Sessions with no recorded start time are not placed in any cell.
func (s *Server) hourWeekday(ctx context.Context, filter aggregateFilter, scope string, offset int) ([][]int, []int, error) {
	grid := make([][]int, 7)
	for index := range grid {
		grid[index] = make([]int, 24)
	}
	byWeekday := make([]int, 7)
	where, args := filter.where()
	shift := strconv.Itoa(offset) + " minutes"
	rows, err := s.db.QueryContext(ctx, `
		SELECT (CAST(strftime('%w', s.started_at, ?) AS INTEGER) + 6) % 7 AS weekday,
		       CAST(strftime('%H', s.started_at, ?) AS INTEGER) AS hour, COUNT(*)
		FROM sessions s LEFT JOIN session_stats st ON st.session_id = s.id
		WHERE s.started_at IS NOT NULL AND s.started_at <> ''`+where+scope+`
		GROUP BY weekday, hour`, append([]any{shift, shift}, args...)...)
	if err != nil {
		return nil, nil, fmt.Errorf("api: hour weekday: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var weekday, hour, count int
		if err := rows.Scan(&weekday, &hour, &count); err != nil {
			return nil, nil, fmt.Errorf("api: scan hour weekday: %w", err)
		}
		if weekday < 0 || weekday > 6 || hour < 0 || hour > 23 {
			continue
		}
		grid[weekday][hour] += count
		byWeekday[weekday] += count
	}
	return grid, byWeekday, rows.Err()
}

// weeklyActivity buckets by the Monday that starts each week.
func (s *Server) weeklyActivity(ctx context.Context, filter aggregateFilter, scope string) ([]weekActivity, error) {
	where, args := filter.where()
	rows, err := s.db.QueryContext(ctx, `
		SELECT date(s.started_at, 'weekday 0', '-6 days') AS week, COUNT(*),
		       SUM(COALESCE(st.duration_ms, 0)), SUM(COALESCE(st.tool_call_count, 0)),
		       SUM(COALESCE(st.friction_count, 0))
		FROM sessions s LEFT JOIN session_stats st ON st.session_id = s.id
		WHERE s.started_at IS NOT NULL AND s.started_at <> ''`+where+scope+`
		GROUP BY week HAVING week IS NOT NULL ORDER BY week`, args...)
	if err != nil {
		return nil, fmt.Errorf("api: weekly activity: %w", err)
	}
	defer rows.Close()
	out := make([]weekActivity, 0)
	for rows.Next() {
		var item weekActivity
		var duration int
		if err := rows.Scan(&item.Week, &item.Sessions, &nullableInt{&duration},
			&nullableInt{&item.ToolCalls}, &nullableInt{&item.Friction}); err != nil {
			return nil, fmt.Errorf("api: scan weekly activity: %w", err)
		}
		item.DurationMS = int64(duration)
		out = append(out, item)
	}
	return out, rows.Err()
}
