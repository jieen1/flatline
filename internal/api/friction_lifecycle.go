package api

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// A friction signature's lifecycle is decided only by the times of the records
// already stored — never by whether anything was fixed.
//
//	new    first recorded inside the window
//	active recorded inside the window, first recorded before it
//	quiet  nothing inside the window, but at least two sessions in history
//	once   only ever one session
//
// quiet is not "fixed": it says nothing happened lately, which is also what a
// window with no session of that shape looks like. That is why a quiet element
// carries project_sessions_last_7d.
const (
	FrictionStatusNew    = "new"
	FrictionStatusActive = "active"
	FrictionStatusQuiet  = "quiet"
	FrictionStatusOnce   = "once"
)

// defaultFrictionWindowDays is the recency window every lifecycle rule reads.
const defaultFrictionWindowDays = 7

const maxFrictionWindowDays = 365

func frictionWindowDays(raw string) int {
	if strings.EqualFold(strings.TrimSpace(raw), rangeAll) {
		return 0
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return defaultFrictionWindowDays
	}
	return effectiveWindowDays(value)
}

func effectiveWindowDays(days int) int {
	if days <= 0 {
		return defaultFrictionWindowDays
	}
	if days > maxFrictionWindowDays {
		return maxFrictionWindowDays
	}
	return days
}

// frictionWindowCutoff is the instant the window opens, in the same format
// occurred_at is stored in, so the comparison is a plain string comparison.
func frictionWindowCutoff(days int) string {
	return time.Now().UTC().AddDate(0, 0, -effectiveWindowDays(days)).Format(time.RFC3339Nano)
}

// lifecycleBounds makes the selected record range the lifecycle range too.
// The lifecycle scan itself still needs history outside these bounds to tell
// "new" from "active" and "quiet". The bounds only decide which records count
// as happening in the selected window.
//
// Older callers that do not provide a range retain the endpoint's historical
// seven-day default; an explicit zero window is the all-time choice.
func lifecycleBounds(filters frictionFilters) (string, string, int) {
	if filters.From != "" {
		return filters.From, filters.To, lifecycleSpanDays(filters.From, filters.To)
	}
	if filters.To != "" {
		return "", filters.To, 0
	}
	if filters.WindowExplicit {
		if filters.Window <= 0 {
			return "", "", 0
		}
		return frictionWindowCutoff(filters.Window), "", effectiveWindowDays(filters.Window)
	}
	if filters.Window > 0 {
		return frictionWindowCutoff(filters.Window), "", effectiveWindowDays(filters.Window)
	}
	return frictionWindowCutoff(defaultFrictionWindowDays), "", defaultFrictionWindowDays
}

func lifecycleWindow(filters frictionFilters) (string, int) {
	from, _, days := lifecycleBounds(filters)
	return from, days
}

func lifecycleSpanDays(from, to string) int {
	start, err := time.Parse(time.RFC3339Nano, from)
	if err != nil {
		return 0
	}
	end := time.Now().UTC()
	if to != "" {
		parsed, parseErr := time.Parse(time.RFC3339Nano, to)
		if parseErr != nil {
			return 0
		}
		end = parsed
	}
	if end.Before(start) {
		return 0
	}
	days := int(math.Round(end.Sub(start).Hours() / 24))
	if days < 1 {
		return 1
	}
	return days
}

func frictionStatus(group frictionGroupResponse, cutoff string) string {
	if cutoff != "" && group.FirstOccurredAt != "" && group.FirstOccurredAt >= cutoff {
		return FrictionStatusNew
	}
	if group.CountLastWindow > 0 {
		if cutoff == "" && group.SessionCount < 2 {
			return FrictionStatusOnce
		}
		return FrictionStatusActive
	}
	if group.SessionCount >= 2 {
		return FrictionStatusQuiet
	}
	return FrictionStatusOnce
}

// wholeDaysBetween is the span a signature has been around, in whole days. It
// is 0 when either bound is not recorded, which is the same as "one day".
func wholeDaysBetween(first, last string) int {
	if first == "" || last == "" {
		return 0
	}
	from, err := time.Parse(time.RFC3339Nano, first)
	if err != nil {
		return 0
	}
	to, err := time.Parse(time.RFC3339Nano, last)
	if err != nil {
		return 0
	}
	if to.Before(from) {
		return 0
	}
	return int(to.Sub(from).Hours() / 24)
}

// attachQuietProjectSessions fills project_sessions_last_7d on the quiet
// elements: which projects each quiet signature was recorded in comes from the
// records already loaded, and how many sessions each project ran inside the
// window is one small query.
func (s *Server) attachQuietProjectSessions(ctx context.Context, set frictionSet, groups []frictionGroupResponse, group, cutoff, upper string) error {
	if group != "signature" {
		return nil
	}
	quiet := false
	for index := range groups {
		if groups[index].Status == FrictionStatusQuiet && groups[index].Signature != "" {
			quiet = true
			break
		}
	}
	if !quiet {
		return nil
	}
	projectsBySignature := set.signatureProjectKeys()
	sessionsByProject, err := s.projectSessionsInWindow(ctx, cutoff, upper)
	if err != nil {
		return err
	}
	for index := range groups {
		if groups[index].Status != FrictionStatusQuiet {
			continue
		}
		total := 0
		for project := range projectsBySignature[groups[index].Signature] {
			total += sessionsByProject[project]
		}
		value := total
		groups[index].ProjectSessionsLastWindow = &value
	}
	return nil
}

func (s *Server) projectSessionsInWindow(ctx context.Context, cutoff, upper string) (map[string]int, error) {
	query := `
		SELECT COALESCE(s.project_key, '` + unrecordedKey + `') AS project_key, COUNT(*)
		FROM sessions s
		WHERE 1 = 1`
	args := make([]any, 0, 2)
	if cutoff != "" {
		query += ` AND s.started_at >= ?`
		args = append(args, cutoff)
	}
	if upper != "" {
		query += ` AND s.started_at <= ?`
		args = append(args, upper)
	}
	query += ` GROUP BY project_key`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("api: count project sessions in window: %w", err)
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var project string
		var count int
		if err := rows.Scan(&project, &count); err != nil {
			return nil, fmt.Errorf("api: scan project sessions in window: %w", err)
		}
		out[project] = count
	}
	return out, rows.Err()
}

// frictionLifecycle is the three-column summary of the signature lifecycle:
// how many signatures sit in each state, and the five biggest of each. The
// counts cover every signature under the filters, not only the ones listed.
type frictionLifecycle struct {
	WindowDays int                     `json:"window_days"`
	New        int                     `json:"new"`
	Active     int                     `json:"active"`
	Quiet      int                     `json:"quiet"`
	Once       int                     `json:"once"`
	TopNew     []frictionGroupResponse `json:"top_new"`
	TopActive  []frictionGroupResponse `json:"top_active"`
	TopQuiet   []frictionGroupResponse `json:"top_quiet"`
}

// lifecycleScanLimit bounds the signature scan behind the lifecycle counts.
// Every response that carries them says how many signatures it read.
const lifecycleScanLimit = 5000

const lifecycleTopN = 5

func (s *Server) frictionLifecycle(ctx context.Context, set frictionSet, filters frictionFilters) (frictionLifecycle, error) {
	// Time filtering determines what happened in the selected window, but it
	// must not erase the history needed to classify a signature as active or
	// quiet. Keep the other filters and reload the complete matching history.
	historyFilters := filters
	historyFilters.From = ""
	historyFilters.To = ""
	historySet, err := s.loadFrictionSet(ctx, historyFilters)
	if err != nil {
		return frictionLifecycle{}, err
	}
	scan := filters
	scan.Group = "signature"
	scan.Sort = "sessions"
	scan.Limit = lifecycleScanLimit
	scan.Offset = 0
	cutoff, upper, windowDays := lifecycleBounds(filters)
	groups := page(historySet.groups(scan, cutoff, upper), 0, lifecycleScanLimit)
	out := frictionLifecycle{WindowDays: windowDays,
		TopNew: make([]frictionGroupResponse, 0), TopActive: make([]frictionGroupResponse, 0),
		TopQuiet: make([]frictionGroupResponse, 0)}
	for _, group := range groups {
		switch group.Status {
		case FrictionStatusNew:
			out.New++
			if len(out.TopNew) < lifecycleTopN {
				out.TopNew = append(out.TopNew, group)
			}
		case FrictionStatusActive:
			out.Active++
			if len(out.TopActive) < lifecycleTopN {
				out.TopActive = append(out.TopActive, group)
			}
		case FrictionStatusQuiet:
			out.Quiet++
			if len(out.TopQuiet) < lifecycleTopN {
				out.TopQuiet = append(out.TopQuiet, group)
			}
		default:
			out.Once++
		}
	}
	// Only a quiet element carries the project-session count, and only the
	// ones actually listed need it.
	if err := s.attachQuietProjectSessions(ctx, historySet, out.TopQuiet, "signature", cutoff, upper); err != nil {
		return frictionLifecycle{}, err
	}
	return out, nil
}
