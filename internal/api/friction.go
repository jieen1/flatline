package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"flatline/internal/adapters"
	"flatline/internal/friction"
)

// frictionUnrecordedKey marks a dimension the source history does not record.
// It is a filter value, never a substitute for zero.
const frictionUnrecordedKey = "__unrecorded__"

const frictionBaseCTE = `
WITH friction_base AS (
	SELECT
		fr.id AS record_id,
		s.id AS session_id,
		s.source AS harness,
		s.source_session_id,
		s.title AS session_title,
		s.task_text,
		NULLIF(TRIM(s.cwd), '') AS cwd,
		COALESCE(s.project_key, '__unrecorded__') AS project_key,
		fr.source_event_id,
		fr.event_type,
		fr.observation_level,
		fr.friction_kind AS record_kind,
		NULLIF(TRIM(COALESCE(fr.tool_name, '')), '') AS tool_name,
		NULLIF(TRIM(COALESCE(fr.category, '')), '') AS category,
		NULLIF(TRIM(COALESCE(fr.category_rule, '')), '') AS category_rule,
		NULLIF(TRIM(COALESCE(fr.category_rule_en, '')), '') AS category_rule_en,
		NULLIF(TRIM(COALESCE(fr.signature, '')), '') AS signature,
		fr.is_error,
		fr.exit_code,
		fr.payload_json,
		fr.locator_json,
		fr.occurred_at,
		ev.id AS event_id,
		s.id || char(31) || fr.event_type || char(31) || fr.source_event_id AS event_key
	FROM friction_records fr
	JOIN sessions s ON s.id = fr.session_id
	LEFT JOIN events ev ON ev.session_id = fr.session_id AND ev.source_event_id = fr.source_event_id
	UNION ALL
	SELECT
		e.id AS record_id,
		s.id AS session_id,
		s.source AS harness,
		s.source_session_id,
		s.title AS session_title,
		s.task_text,
		NULLIF(TRIM(s.cwd), '') AS cwd,
		COALESCE(s.project_key, '__unrecorded__') AS project_key,
		COALESCE(e.source_event_id, 'event:' || CAST(e.id AS TEXT)) AS source_event_id,
		e.event_type,
		e.observation_level,
		'asset_violation' AS record_kind,
		NULL AS tool_name,
		NULL AS category,
		NULL AS category_rule,
		NULL AS category_rule_en,
		NULL AS signature,
		NULL AS is_error,
		NULL AS exit_code,
		COALESCE(e.payload_json, '') AS payload_json,
		COALESCE(e.locator_json, '') AS locator_json,
		COALESCE(e.occurred_at, '') AS occurred_at,
		e.id AS event_id,
		s.id || char(31) || e.event_type || char(31) || COALESCE(e.source_event_id, 'event:' || CAST(e.id AS TEXT)) AS event_key
	FROM events e
	JOIN sessions s ON s.id = e.session_id
	WHERE e.event_type = 'asset_violation'
), friction_classified AS (
	-- One source event can be two kinds of friction at once: a tool result that
	-- both reports is_error and carries a non-zero exit code. The kinds are
	-- flags on the one row rather than one row per kind, because a union of
	-- four branches makes SQLite walk friction_base — bounded payloads and all
	-- — four times for every question the page asks.
	SELECT *,
		CASE WHEN record_kind = 'tool_error' AND is_error = 1 THEN 1 ELSE 0 END AS is_tool_error,
		CASE WHEN record_kind = 'tool_error' AND exit_code IS NOT NULL AND exit_code != 0 THEN 1 ELSE 0 END AS is_nonzero_exit,
		CASE WHEN record_kind = 'user_interrupt' THEN 1 ELSE 0 END AS is_user_interrupt,
		CASE WHEN record_kind = 'asset_violation' THEN 1 ELSE 0 END AS is_asset_violation
	FROM friction_base
), friction_filtered AS (
	SELECT * FROM friction_classified
	WHERE (is_tool_error = 1 OR is_nonzero_exit = 1 OR is_user_interrupt = 1 OR is_asset_violation = 1)`

type frictionFilters struct {
	Project   string
	Harness   string
	Kind      string
	Category  string
	Tool      string
	Signature string
	Query     string
	// From and To are already-parsed timestamp bounds; "" is no bound. They
	// are compared against occurred_at, which is stored in the same form.
	From   string
	To     string
	Sort   string
	Group  string
	Limit  int
	Offset int
	// Window is the recency window, in days, the lifecycle status of a
	// signature is decided by. Zero means an explicitly unbounded lifecycle
	// when WindowExplicit is true; otherwise it means the default.
	Window         int
	WindowExplicit bool
}

type frictionCountResponse struct {
	Key          string `json:"key"`
	Label        string `json:"label,omitempty"`
	Rule         string `json:"rule,omitempty"`
	RuleEN       string `json:"rule_en,omitempty"`
	Count        int    `json:"count"`
	SessionCount int    `json:"session_count"`
}

type frictionSummaryResponse struct {
	TotalEvents         int `json:"total_events"`
	ToolErrorCount      int `json:"tool_error_count"`
	NonzeroExitCount    int `json:"nonzero_exit_count"`
	AssetViolationCount int `json:"asset_violation_count"`
	UserInterruptCount  int `json:"user_interrupt_count"`
	ToolUnrecordedCount int `json:"tool_unrecorded_count"`
	SessionCount        int `json:"session_count"`
	ProjectCount        int `json:"project_count"`
	RecurringSignatures int `json:"recurring_signatures"`
	// ExpectedExitCount is how many records in scope carry an exit code the
	// program documents as an answer rather than as a failure — `rg` exiting 1
	// means nothing matched. By default those records are left out of every
	// number above and this is what the exclusion removed, so the page can say
	// "已排除 N 条预期非零退出" instead of dropping them silently. Asking for
	// category=expected_exit shows them instead, and then this is their count.
	ExpectedExitCount int                     `json:"expected_exit_count"`
	ByCategory        []frictionCountResponse `json:"by_category"`
	ByTool            []frictionCountResponse `json:"by_tool"`
	// ByHarness counts the same records per harness, under the filter that is
	// in effect, so the harness selector shows what selecting it would give.
	ByHarness  []frictionCountResponse `json:"by_harness"`
	ByHintKind []frictionHintKindCount `json:"by_hint_kind"`
	// CoverageGaps are the recurring signatures whose mechanism is a harness
	// rule that no rule the user wrote mentions. See coverage.go for the test
	// and for what it deliberately does not claim.
	CoverageGaps      []frictionCoverageGap `json:"coverage_gaps"`
	CoverageGapNote   string                `json:"coverage_gap_note"`
	CoverageGapNoteEN string                `json:"coverage_gap_note_en"`
	Complete          bool                  `json:"complete"`
}

// frictionHintKindCount is the distribution of recurring friction over the
// closed set of hint kinds. Signatures matched by no rule are reported under
// the explicit unrecorded key; records with no signature at all are outside
// this distribution, because a record with no category has no mechanism to
// name.
type frictionHintKindCount struct {
	Kind       string `json:"kind"`
	Signatures int    `json:"signatures"`
	Count      int    `json:"count"`
	// SessionCount is the number of distinct sessions across this kind's
	// signatures. It is not the sum of theirs: one session that hit three
	// signatures of the same kind is one session, not three.
	SessionCount int `json:"session_count"`
}

type frictionPaginationResponse struct {
	Offset  int  `json:"offset"`
	Limit   int  `json:"limit"`
	Total   int  `json:"total"`
	HasMore bool `json:"has_more"`
}

type frictionGroupResponse struct {
	Key                 string  `json:"key"`
	GroupBy             string  `json:"group_by"`
	Label               string  `json:"label"`
	ProjectKey          string  `json:"project_key,omitempty"`
	ProjectLabel        string  `json:"project_label,omitempty"`
	CWD                 *string `json:"cwd,omitempty"`
	Harness             string  `json:"harness,omitempty"`
	Category            string  `json:"category,omitempty"`
	CategoryRule        string  `json:"category_rule,omitempty"`
	CategoryRuleEN      string  `json:"category_rule_en,omitempty"`
	ToolName            string  `json:"tool_name,omitempty"`
	Signature           string  `json:"signature,omitempty"`
	SampleLine          string  `json:"sample_line,omitempty"`
	FrictionCount       int     `json:"friction_count"`
	Count               int     `json:"count"`
	ToolErrorCount      int     `json:"tool_error_count"`
	NonzeroExitCount    int     `json:"nonzero_exit_count"`
	AssetViolationCount int     `json:"asset_violation_count"`
	UserInterruptCount  int     `json:"user_interrupt_count"`
	SessionCount        int     `json:"session_count"`
	ProjectCount        int     `json:"project_count"`
	FirstOccurredAt     string  `json:"first_occurred_at,omitempty"`
	LastOccurredAt      string  `json:"last_occurred_at,omitempty"`
	// Hint names the mechanism behind a recurring signature, or is null when
	// no rule in the closed dictionary matches. Only signature groups carry it.
	Hint *friction.Hint `json:"hint"`
	// Status is where this signature sits in time; see frictionStatus.
	Status string `json:"status,omitempty"`
	// The two window counts are named for the default 7-day window; the window
	// actually applied is reported as window_days on the response envelope.
	SessionsLastWindow int `json:"sessions_last_7d"`
	CountLastWindow    int `json:"count_last_7d"`
	// DaysActive is the whole days between the first and the last record.
	DaysActive int `json:"days_active"`
	// ProjectSessionsLastWindow is how many sessions ran in the same projects
	// inside the window. A quiet signature carries it because "it stopped
	// happening" and "no session of that shape ran" are different facts.
	ProjectSessionsLastWindow *int `json:"project_sessions_last_7d,omitempty"`
	// Brief is the evidence pack for handing this signature to the user's own
	// agent (ADR-21). Only signature groups carry it.
	Brief *signatureBrief `json:"brief,omitempty"`
	// Watch is the live status of the user's fix verification for this
	// signature, or null when none is active.
	Watch *frictionWatchBadge `json:"watch,omitempty"`
}

// frictionWatchBadge is what the friction page needs to show that a signature
// is under fix verification, without the watch's whole record.
type frictionWatchBadge struct {
	ID              int64  `json:"id"`
	Status          string `json:"status"`
	CreatedAt       string `json:"created_at"`
	WindowDays      int    `json:"window_days"`
	WindowCount     int    `json:"window_count"`
	ProjectSessions int    `json:"project_sessions_in_window"`
}

type frictionProjectOptionResponse struct {
	Key   string  `json:"key"`
	Label string  `json:"label"`
	CWD   *string `json:"cwd,omitempty"`
}

type frictionEventResponse struct {
	ID              int64           `json:"id"`
	EventID         *int64          `json:"event_id,omitempty"`
	SessionID       string          `json:"session_id"`
	Source          string          `json:"harness"`
	SourceSessionID string          `json:"source_session_id"`
	SessionTitle    *string         `json:"session_title,omitempty"`
	TaskText        *string         `json:"task_text,omitempty"`
	ProjectKey      string          `json:"project_key"`
	ProjectLabel    string          `json:"project_label"`
	CWD             *string         `json:"cwd,omitempty"`
	SourceEventID   string          `json:"source_event_id"`
	FrictionKinds   []string        `json:"friction_kinds"`
	Category        string          `json:"category,omitempty"`
	CategoryRule    string          `json:"category_rule,omitempty"`
	CategoryRuleEN  string          `json:"category_rule_en,omitempty"`
	Signature       string          `json:"signature,omitempty"`
	EventType       string          `json:"event_type"`
	Observation     string          `json:"observation_level"`
	IsError         *bool           `json:"is_error,omitempty"`
	ExitCode        *int            `json:"exit_code,omitempty"`
	ToolName        string          `json:"tool_name,omitempty"`
	Payload         json.RawMessage `json:"payload"`
	Locator         json.RawMessage `json:"locator"`
	OccurredAt      string          `json:"occurred_at,omitempty"`
}

type frictionOverviewResponse struct {
	Summary           frictionSummaryResponse         `json:"summary"`
	Projects          []frictionProjectOptionResponse `json:"projects"`
	Groups            []frictionGroupResponse         `json:"groups"`
	GroupBy           string                          `json:"group_by"`
	Pagination        frictionPaginationResponse      `json:"pagination"`
	ClassifierVersion string                          `json:"classifier_version"`
	WindowDays        int                             `json:"window_days"`
	Complete          bool                            `json:"complete"`
}

type frictionDetailResponse struct {
	Group            frictionGroupResponse      `json:"group"`
	Summary          frictionSummaryResponse    `json:"summary"`
	Records          []frictionEventResponse    `json:"records"`
	Pagination       frictionPaginationResponse `json:"pagination"`
	Complete         bool                       `json:"complete"`
	RecordsTruncated bool                       `json:"records_truncated"`
}

func (s *Server) handleFriction(w http.ResponseWriter, r *http.Request) {
	filters, err := parseFrictionFilters(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if r.URL.Query().Get("view") == "detail" || r.URL.Query().Get("detail") == "1" {
		if filters.Project == "" || filters.Harness == "" {
			http.Error(w, "friction detail requires project and harness", http.StatusBadRequest)
			return
		}
		response, err := s.frictionDetail(r.Context(), filters)
		if err != nil {
			if err == sql.ErrNoRows {
				http.Error(w, "friction group not found", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, response)
		return
	}
	response, err := s.frictionOverview(r.Context(), filters)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func parseFrictionFilters(r *http.Request) (frictionFilters, error) {
	query := r.URL.Query()
	harness := strings.TrimSpace(query.Get("harness"))
	if harness == "" {
		harness = strings.TrimSpace(query.Get("source"))
	}
	if harness != "" && !adapters.Source(harness).Valid() {
		return frictionFilters{}, fmt.Errorf("unsupported harness %q", harness)
	}
	kind := strings.TrimSpace(query.Get("kind"))
	switch kind {
	case "", "tool_error", "nonzero_exit", "asset_violation", "user_interrupt":
	default:
		return frictionFilters{}, fmt.Errorf("unsupported friction kind %q", kind)
	}
	category := strings.TrimSpace(query.Get("category"))
	if category != "" && category != frictionUnrecordedKey && !frictionKnownCategory(category) {
		return frictionFilters{}, fmt.Errorf("unsupported friction category %q", category)
	}
	group := strings.TrimSpace(query.Get("group"))
	if group == "" {
		group = "project"
	}
	if group != "project" && group != "category" && group != "tool" && group != "signature" {
		return frictionFilters{}, fmt.Errorf("unsupported friction group %q", group)
	}
	sort := strings.TrimSpace(query.Get("sort"))
	if sort == "" {
		// A recurring signature is interesting because it came back in another
		// session, so that is what the signature grouping leads with.
		sort = "count"
		if group == "signature" {
			sort = "sessions"
		}
	}
	if sort != "count" && sort != "recent" && sort != "sessions" {
		return frictionFilters{}, fmt.Errorf("unsupported friction sort %q", sort)
	}
	limit := queryLimit(r, 100)
	if limit > 500 {
		limit = 500
	}
	window, err := rangeWindow(query, "")
	if err != nil {
		return frictionFilters{}, err
	}
	rawWindow := strings.TrimSpace(query.Get("window"))
	return frictionFilters{
		Project:        strings.TrimSpace(query.Get("project")),
		Harness:        harness,
		Kind:           kind,
		Category:       category,
		Tool:           strings.TrimSpace(query.Get("tool")),
		Signature:      strings.TrimSpace(query.Get("signature")),
		Query:          strings.TrimSpace(query.Get("q")),
		From:           boundStamp(window.From),
		To:             boundStamp(window.To),
		Sort:           sort,
		Group:          group,
		Limit:          limit,
		Offset:         queryOffset(r),
		Window:         frictionWindowDays(rawWindow),
		WindowExplicit: rawWindow != "",
	}, nil
}

func frictionKnownCategory(value string) bool {
	for _, category := range friction.Categories {
		if category == value {
			return true
		}
	}
	return false
}

func (s *Server) frictionOverview(ctx context.Context, filters frictionFilters) (frictionOverviewResponse, error) {
	set, err := s.loadFrictionSet(ctx, filters)
	if err != nil {
		return frictionOverviewResponse{}, err
	}
	cutoff, upper, windowDays := lifecycleBounds(filters)
	groupSet := set
	if filters.Group == "signature" {
		// A selected range can contain no record for a quiet signature. Load its
		// history for lifecycle rows, while summary and filter facets stay scoped
		// to the selected range in set.
		historyFilters := filters
		historyFilters.From = ""
		historyFilters.To = ""
		groupSet, err = s.loadFrictionSet(ctx, historyFilters)
		if err != nil {
			return frictionOverviewResponse{}, err
		}
	}
	all := groupSet.groups(filters, cutoff, upper)
	groups := page(all, filters.Offset, filters.Limit)
	if err := s.attachQuietProjectSessions(ctx, groupSet, groups, filters.Group, cutoff, upper); err != nil {
		return frictionOverviewResponse{}, err
	}
	// ADR-21: briefs and their verification badges belong to signature groups
	// only — a category or tool row is not something one rule fixes.
	if filters.Group == "signature" {
		if err := s.attachBriefs(ctx, groups); err != nil {
			return frictionOverviewResponse{}, err
		}
		if err := s.attachWatchStatuses(ctx, groups); err != nil {
			return frictionOverviewResponse{}, err
		}
	}
	// The project filter list has to offer every project, not only the ones
	// left after the project filter itself.
	optionFilters := filters
	optionFilters.Project = ""
	options := set
	if filters.Project != "" {
		if options, err = s.loadFrictionSet(ctx, optionFilters); err != nil {
			return frictionOverviewResponse{}, err
		}
	}
	summary, err := s.frictionSummary(ctx, set)
	if err != nil {
		return frictionOverviewResponse{}, err
	}
	return frictionOverviewResponse{
		Summary: summary, Projects: options.projectOptions(), Groups: groups, GroupBy: filters.Group,
		Pagination: frictionPaginationResponse{
			Offset: filters.Offset, Limit: filters.Limit, Total: len(all),
			HasMore: filters.Offset+len(groups) < len(all),
		},
		ClassifierVersion: friction.ClassifierVersion,
		WindowDays:        windowDays,
		Complete:          true,
	}, nil
}

// signatureGroups is the signature grouping every caller should use: the
// groups plus the one field that needs a second look, so a quiet signature is
// never shown without saying whether the same projects ran at all.
func (s *Server) signatureGroups(ctx context.Context, set frictionSet, filters frictionFilters) ([]frictionGroupResponse, error) {
	cutoff, upper, _ := lifecycleBounds(filters)
	groups := page(set.groups(filters, cutoff, upper), filters.Offset, filters.Limit)
	if err := s.attachQuietProjectSessions(ctx, set, groups, filters.Group, cutoff, upper); err != nil {
		return nil, err
	}
	return groups, nil
}

func (s *Server) frictionDetail(ctx context.Context, filters frictionFilters) (frictionDetailResponse, error) {
	groupFilters := filters
	groupFilters.Kind = ""
	groupFilters.Category = ""
	groupFilters.Tool = ""
	groupFilters.Group = "project"
	groupFilters.Limit = 1
	groupFilters.Offset = 0
	groupSet, err := s.loadFrictionSet(ctx, groupFilters)
	if err != nil {
		return frictionDetailResponse{}, err
	}
	cutoff, upper, _ := lifecycleBounds(filters)
	groups := page(groupSet.groups(groupFilters, cutoff, upper), 0, 1)
	if len(groups) == 0 {
		return frictionDetailResponse{}, sql.ErrNoRows
	}
	summaryFilters := filters
	summaryFilters.Kind = ""
	summarySet, err := s.loadFrictionSet(ctx, summaryFilters)
	if err != nil {
		return frictionDetailResponse{}, err
	}
	countSet, err := s.loadFrictionSet(ctx, filters)
	if err != nil {
		return frictionDetailResponse{}, err
	}
	total := len(countSet.records)
	records, err := s.queryFrictionEvents(ctx, filters)
	if err != nil {
		return frictionDetailResponse{}, err
	}
	summary, err := s.frictionSummary(ctx, summarySet)
	if err != nil {
		return frictionDetailResponse{}, err
	}
	return frictionDetailResponse{
		Group: groups[0], Summary: summary, Records: records,
		Pagination: frictionPaginationResponse{
			Offset: filters.Offset, Limit: filters.Limit, Total: total,
			HasMore: filters.Offset+len(records) < total,
		},
		Complete: true, RecordsTruncated: filters.Offset+len(records) < total,
	}, nil
}

func (s *Server) queryFrictionEvents(ctx context.Context, filters frictionFilters) ([]frictionEventResponse, error) {
	query, args := frictionFilteredQuery(filters)
	query += `
)
SELECT event_key, MIN(record_id) AS record_id, MAX(event_id) AS event_id, session_id, MAX(harness),
	MAX(source_session_id), MAX(session_title), MAX(task_text), MAX(cwd), MAX(project_key),
	MAX(source_event_id), MAX(event_type), MAX(observation_level), MAX(is_error), MAX(exit_code),
	MAX(tool_name), MAX(category), MAX(category_rule), MAX(category_rule_en), MAX(signature),
	MAX(payload_json), MAX(locator_json), MAX(occurred_at) AS occurred_at,
	MAX(is_tool_error), MAX(is_nonzero_exit), MAX(is_asset_violation), MAX(is_user_interrupt)
FROM friction_filtered
GROUP BY event_key
ORDER BY occurred_at IS NULL, occurred_at DESC, record_id DESC
LIMIT ? OFFSET ?`
	args = append(args, filters.Limit, filters.Offset)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("api: query friction events: %w", err)
	}
	defer rows.Close()
	records := make([]frictionEventResponse, 0, filters.Limit)
	for rows.Next() {
		var record frictionEventResponse
		var eventKey sql.NullString
		var harness, sourceSessionID, title, taskText, cwd, projectKey, sourceEventID sql.NullString
		var eventType, observation, toolName, category, categoryRule, categoryRuleEN, signature sql.NullString
		var payload, locator, occurred sql.NullString
		var isError, exitCode, eventID sql.NullInt64
		var toolErrorKind, nonzeroExitKind, assetViolationKind, userInterruptKind int
		if err := rows.Scan(&eventKey, &record.ID, &eventID, &record.SessionID, &harness,
			&sourceSessionID, &title, &taskText, &cwd, &projectKey,
			&sourceEventID, &eventType, &observation, &isError, &exitCode,
			&toolName, &category, &categoryRule, &categoryRuleEN, &signature,
			&payload, &locator, &occurred,
			&toolErrorKind, &nonzeroExitKind, &assetViolationKind, &userInterruptKind); err != nil {
			return nil, fmt.Errorf("api: scan friction event: %w", err)
		}
		_ = eventKey
		if eventID.Valid {
			value := eventID.Int64
			record.EventID = &value
		}
		record.Source = harness.String
		record.SourceSessionID = sourceSessionID.String
		if title.Valid {
			value := title.String
			record.SessionTitle = &value
		}
		if taskText.Valid {
			value := taskText.String
			record.TaskText = &value
		}
		record.ProjectKey = projectKey.String
		record.ProjectLabel = frictionProjectLabel(record.ProjectKey, cwd)
		if cwd.Valid && strings.TrimSpace(cwd.String) != "" {
			value := cwd.String
			record.CWD = &value
		}
		record.SourceEventID = sourceEventID.String
		record.EventType = eventType.String
		record.Observation = observation.String
		record.FrictionKinds = frictionKindNames(map[string]int{
			"tool_error": toolErrorKind, "nonzero_exit": nonzeroExitKind,
			"asset_violation": assetViolationKind, "user_interrupt": userInterruptKind})
		record.Category = category.String
		record.CategoryRule, record.CategoryRuleEN = categoryRule.String, categoryRuleEN.String
		record.Signature = signature.String
		record.IsError, record.ExitCode = optionalBool(isError), optionalInt(exitCode)
		record.Payload = rawJSONOrNull(payload.String)
		record.Locator = rawJSONOrNull(locator.String)
		if occurred.Valid {
			record.OccurredAt = occurred.String
		}
		record.ToolName = toolName.String
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("api: iterate friction events: %w", err)
	}
	return records, nil
}

// optionalBool and optionalInt keep "not recorded" distinct from a recorded
// zero: a NULL column stays nil rather than becoming false or 0.
func optionalBool(value sql.NullInt64) *bool {
	if !value.Valid {
		return nil
	}
	out := value.Int64 != 0
	return &out
}

func optionalInt(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	out := int(value.Int64)
	return &out
}

func frictionFilteredQuery(filters frictionFilters) (string, []any) {
	query := frictionBaseCTE
	args := make([]any, 0, 9)
	if filters.Project != "" {
		query += " AND project_key = ?"
		args = append(args, filters.Project)
	}
	if filters.Harness != "" {
		query += " AND harness = ?"
		args = append(args, filters.Harness)
	}
	if filters.Kind != "" {
		// The kind is one of a closed set checked in parseFrictionFilters.
		query += " AND is_" + filters.Kind + " = 1"
	}
	if filters.Category == frictionUnrecordedKey {
		query += " AND category IS NULL"
	} else if filters.Category != "" {
		query += " AND category = ?"
		args = append(args, filters.Category)
	}
	if filters.Tool == frictionUnrecordedKey {
		query += " AND tool_name IS NULL"
	} else if filters.Tool != "" {
		query += " AND tool_name = ?"
		args = append(args, filters.Tool)
	}
	if filters.Signature == frictionUnrecordedKey {
		query += " AND signature IS NULL"
	} else if filters.Signature != "" {
		query += " AND signature = ?"
		args = append(args, filters.Signature)
	}
	if filters.From != "" {
		query += " AND occurred_at >= ?"
		args = append(args, filters.From)
	}
	if filters.To != "" {
		query += " AND occurred_at <= ?"
		args = append(args, filters.To)
	}
	if filters.Query != "" {
		query += " AND LOWER(COALESCE(project_key, '') || ' ' || COALESCE(cwd, '') || ' ' || COALESCE(harness, '') || ' ' || COALESCE(source_session_id, '') || ' ' || COALESCE(session_title, '') || ' ' || COALESCE(task_text, '') || ' ' || COALESCE(tool_name, '') || ' ' || COALESCE(category, '') || ' ' || COALESCE(source_event_id, '') || ' ' || COALESCE(payload_json, '')) LIKE LOWER(?)"
		args = append(args, "%"+filters.Query+"%")
	}
	return query, args
}

// frictionSignatureLine is the normalized evidence line the signature was
// built from: category|tool|line. It is what the UI shows as the sample.
func frictionSignatureLine(signature string) string {
	parts := strings.SplitN(signature, "|", 3)
	if len(parts) < 3 {
		return ""
	}
	return parts[2]
}

func frictionProjectLabel(projectKey string, cwd sql.NullString) string {
	if cwd.Valid && strings.TrimSpace(cwd.String) != "" {
		value := strings.TrimRight(strings.TrimSpace(cwd.String), "/\\")
		if index := strings.LastIndexAny(value, "/\\"); index >= 0 && index+1 < len(value) {
			return value[index+1:]
		}
		if value != "" {
			return value
		}
	}
	if projectKey == frictionUnrecordedKey || strings.TrimSpace(projectKey) == "" {
		return "项目未记录"
	}
	return projectKey
}

// frictionKindNames lists the kinds one source event carries, in the fixed
// order the closed set defines, so two records of the same kinds always read
// the same way.
func frictionKindNames(flags map[string]int) []string {
	out := make([]string, 0, len(flags))
	for _, kind := range []string{"tool_error", "nonzero_exit", "asset_violation", "user_interrupt"} {
		if flags[kind] != 0 {
			out = append(out, kind)
		}
	}
	return out
}

type frictionRecordResponse struct {
	ID               int64           `json:"id"`
	SourceEventID    string          `json:"source_event_id"`
	FrictionKind     string          `json:"friction_kind"`
	EventType        string          `json:"event_type"`
	ObservationLevel string          `json:"observation_level"`
	IsError          *bool           `json:"is_error,omitempty"`
	ExitCode         *int            `json:"exit_code,omitempty"`
	Payload          json.RawMessage `json:"payload"`
	Locator          json.RawMessage `json:"locator"`
	OccurredAt       string          `json:"occurred_at,omitempty"`
}

type frictionResponse struct {
	Count            int                      `json:"count"`
	Records          []frictionRecordResponse `json:"records"`
	Complete         bool                     `json:"complete"`
	RecordsTruncated bool                     `json:"records_truncated"`
}

func (s *Server) sessionFriction(ctx context.Context, sessionID string, limit int) (*frictionResponse, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM friction_records WHERE session_id = ?) +
			(SELECT COUNT(*) FROM events WHERE session_id = ? AND event_type = 'asset_violation')`, sessionID, sessionID).Scan(&count); err != nil {
		return nil, fmt.Errorf("api: count session friction: %w", err)
	}
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, source_event_id, friction_kind, event_type, observation_level,
		       is_error, exit_code, payload_json, locator_json, occurred_at
		FROM (
			SELECT id, source_event_id, friction_kind, event_type, observation_level,
			       is_error, exit_code, payload_json, locator_json, occurred_at
			FROM friction_records WHERE session_id = ?
			UNION ALL
			SELECT id, COALESCE(source_event_id, ''), 'asset_violation', event_type, observation_level,
			       NULL, NULL, COALESCE(payload_json, ''), COALESCE(locator_json, ''), COALESCE(occurred_at, '')
			FROM events WHERE session_id = ? AND event_type = 'asset_violation'
		)
		ORDER BY occurred_at IS NULL, occurred_at, id
		LIMIT ?`, sessionID, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("api: query session friction: %w", err)
	}
	defer rows.Close()
	records := make([]frictionRecordResponse, 0, limit)
	for rows.Next() {
		var record frictionRecordResponse
		var observation, payload, locator, occurred sql.NullString
		var isError sql.NullInt64
		var exitCode sql.NullInt64
		if err := rows.Scan(&record.ID, &record.SourceEventID, &record.FrictionKind, &record.EventType, &observation, &isError, &exitCode, &payload, &locator, &occurred); err != nil {
			return nil, fmt.Errorf("api: scan session friction: %w", err)
		}
		record.ObservationLevel = observation.String
		record.IsError, record.ExitCode = optionalBool(isError), optionalInt(exitCode)
		record.Payload = rawJSONOrNull(payload.String)
		record.Locator = rawJSONOrNull(locator.String)
		if occurred.Valid {
			record.OccurredAt = occurred.String
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("api: iterate session friction: %w", err)
	}
	return &frictionResponse{Count: count, Records: records, Complete: true, RecordsTruncated: len(records) < count}, nil
}
