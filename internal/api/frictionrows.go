package api

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"flatline/internal/friction"
)

// The friction page asks eight questions of the same set of records: the
// totals, two breakdowns, the hint distribution, the recurring count, the
// group list, the group count and the lifecycle. Asking them in SQL meant
// evaluating the same CTE once per question — a join over friction_records,
// sessions and events, plus a union with the asset-violation events, seven
// times for one request.
//
// The set is small (thousands of records, not millions) and the columns the
// aggregates read are small too, so it is loaded once, without the bounded
// payload, and every aggregate is computed over it in Go. The record list of
// the detail view still reads payloads straight from SQL: that one needs them.

// friction kinds, as a bit per kind, so one source event that is both a tool
// error and a nonzero exit stays one record carrying two kinds.
const (
	kindToolError uint8 = 1 << iota
	kindNonzeroExit
	kindAssetViolation
	kindUserInterrupt
)

// frictionRecord is one source event's friction, reduced to what the
// aggregates read.
type frictionRecord struct {
	sessionID      string
	projectKey     string
	cwd            string
	harness        string
	category       string
	categoryRule   string
	categoryRuleEN string
	toolName       string
	signature      string
	occurredAt     string
	kinds          uint8
}

// frictionSet is one filtered load, plus the count of what the default
// exclusion took out.
type frictionSet struct {
	records []frictionRecord
	// ExpectedExit is how many records were left out because the program
	// documents that exit code as an answer rather than as a failure. The
	// number is reported so the exclusion is visible instead of silent.
	ExpectedExit int
}

// loadFrictionSet reads the filtered friction records once. Records the
// classifier read as an expected nonzero exit are counted and then dropped,
// unless the caller asked for exactly that category.
func (s *Server) loadFrictionSet(ctx context.Context, filters frictionFilters) (frictionSet, error) {
	query, args := frictionFilteredQuery(filters)
	query += `
)
SELECT MIN(session_id), MAX(project_key), MAX(COALESCE(cwd, '')), MAX(harness),
       MAX(COALESCE(category, '')), MAX(COALESCE(category_rule, '')), MAX(COALESCE(category_rule_en, '')),
       MAX(COALESCE(tool_name, '')), MAX(COALESCE(signature, '')),
       MAX(COALESCE(occurred_at, '')),
       MAX(is_tool_error), MAX(is_nonzero_exit), MAX(is_asset_violation), MAX(is_user_interrupt)
FROM friction_filtered
GROUP BY event_key`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return frictionSet{}, fmt.Errorf("api: load friction records: %w", err)
	}
	defer rows.Close()
	out := frictionSet{records: make([]frictionRecord, 0, 512)}
	keepExpected := filters.Category == friction.CategoryExpectedExit
	for rows.Next() {
		var item frictionRecord
		var toolError, nonzeroExit, assetViolation, userInterrupt int
		if err := rows.Scan(&item.sessionID, &item.projectKey, &item.cwd, &item.harness,
			&item.category, &item.categoryRule, &item.categoryRuleEN, &item.toolName, &item.signature, &item.occurredAt,
			&toolError, &nonzeroExit, &assetViolation, &userInterrupt); err != nil {
			return frictionSet{}, fmt.Errorf("api: scan friction record: %w", err)
		}
		for bit, present := range map[uint8]int{
			kindToolError: toolError, kindNonzeroExit: nonzeroExit,
			kindAssetViolation: assetViolation, kindUserInterrupt: userInterrupt,
		} {
			if present != 0 {
				item.kinds |= bit
			}
		}
		if friction.IsExpectedExit(item.category) {
			out.ExpectedExit++
			if !keepExpected {
				continue
			}
		}
		out.records = append(out.records, item)
	}
	if err := rows.Err(); err != nil {
		return frictionSet{}, fmt.Errorf("api: iterate friction records: %w", err)
	}
	return out, nil
}

// summary computes the whole friction summary in one pass over the records.
func (set frictionSet) summary() frictionSummaryResponse {
	out := frictionSummaryResponse{Complete: true, ExpectedExitCount: set.ExpectedExit}
	sessions := make(map[string]struct{})
	projects := make(map[string]struct{})
	categories := newDimension()
	tools := newDimension()
	harnesses := newDimension()
	signatures := newDimension()
	for _, record := range set.records {
		out.TotalEvents++
		if record.kinds&kindToolError != 0 {
			out.ToolErrorCount++
		}
		if record.kinds&kindNonzeroExit != 0 {
			out.NonzeroExitCount++
		}
		if record.kinds&kindAssetViolation != 0 {
			out.AssetViolationCount++
		}
		if record.kinds&kindUserInterrupt != 0 {
			out.UserInterruptCount++
		}
		if record.toolName == "" {
			out.ToolUnrecordedCount++
		}
		sessions[record.sessionID] = struct{}{}
		projects[record.projectKey] = struct{}{}
		categories.add(orUnrecorded(record.category), record.sessionID,
			friction.Rule{Text: record.categoryRule, EN: record.categoryRuleEN})
		tools.add(orUnrecorded(record.toolName), record.sessionID, friction.Rule{})
		harnesses.add(orUnrecorded(record.harness), record.sessionID, friction.Rule{})
		if record.signature != "" {
			signatures.add(record.signature, record.sessionID, friction.Rule{})
		}
	}
	out.SessionCount = len(sessions)
	out.ProjectCount = len(projects)
	out.ByCategory = categories.counts(200)
	out.ByTool = tools.counts(200)
	out.ByHarness = harnesses.counts(50)
	for _, item := range signatures.items {
		if len(item.sessions) >= 2 {
			out.RecurringSignatures++
		}
	}
	out.ByHintKind = hintKinds(signatures)
	return out
}

// hintKinds maps every signature through the closed hint dictionary. A
// signature no rule matches is reported under the unrecorded key rather than
// dropped, and the session count is the number of distinct sessions across
// that kind's signatures — not the sum of theirs, which counts a session once
// per signature it hit.
func hintKinds(signatures *dimension) []frictionHintKindCount {
	totals := make(map[string]*frictionHintKindCount)
	sessions := make(map[string]map[string]struct{})
	for _, item := range signatures.items {
		kind := frictionUnrecordedKey
		if hint := friction.LookupHint(item.key); hint != nil {
			kind = hint.Kind
		}
		entry, ok := totals[kind]
		if !ok {
			entry = &frictionHintKindCount{Kind: kind}
			totals[kind] = entry
			sessions[kind] = make(map[string]struct{})
		}
		entry.Signatures++
		entry.Count += item.count
		for session := range item.sessions {
			sessions[kind][session] = struct{}{}
		}
	}
	out := make([]frictionHintKindCount, 0, len(totals))
	for kind, entry := range totals {
		entry.SessionCount = len(sessions[kind])
		out = append(out, *entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

// dimension accumulates one grouping key's records and the sessions they came
// from, so a session that hit the same key twice is still one session.
type dimension struct {
	index map[string]int
	items []*dimensionItem
}

type dimensionItem struct {
	key      string
	rule     friction.Rule
	count    int
	sessions map[string]struct{}
}

func newDimension() *dimension {
	return &dimension{index: make(map[string]int)}
}

func (d *dimension) add(key, sessionID string, rule friction.Rule) *dimensionItem {
	position, ok := d.index[key]
	if !ok {
		position = len(d.items)
		d.index[key] = position
		d.items = append(d.items, &dimensionItem{key: key, sessions: make(map[string]struct{})})
	}
	item := d.items[position]
	item.count++
	item.sessions[sessionID] = struct{}{}
	if item.rule.Text == "" && rule.Text != "" {
		item.rule = rule
	}
	return item
}

func (d *dimension) counts(limit int) []frictionCountResponse {
	out := make([]frictionCountResponse, 0, len(d.items))
	for _, item := range d.items {
		out = append(out, frictionCountResponse{Key: item.key, Rule: item.rule.Text, RuleEN: item.rule.EN,
			Count: item.count, SessionCount: len(item.sessions)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Key < out[j].Key
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func orUnrecorded(value string) string {
	if strings.TrimSpace(value) == "" {
		return frictionUnrecordedKey
	}
	return value
}

// groupAccumulator builds one grouping of the loaded records.
type groupAccumulator struct {
	group  string
	cutoff string
	upper  string
	index  map[string]int
	items  []*groupState
}

type groupState struct {
	key            string
	projectKey     string
	cwd            string
	harness        string
	category       string
	categoryRule   string
	categoryRuleEN string
	toolName       string
	signature      string

	frictionCount    int
	toolError        int
	nonzeroExit      int
	assetViolation   int
	userInterrupt    int
	sessions         map[string]struct{}
	projects         map[string]struct{}
	windowSessions   map[string]struct{}
	windowCount      int
	first, last      string
	signatureIsGroup bool
}

func newGroupAccumulator(group, cutoff, upper string) *groupAccumulator {
	return &groupAccumulator{group: group, cutoff: cutoff, upper: upper, index: make(map[string]int)}
}

// groupKey is the grouping key of one record, and the label parts that go with
// it. A dimension the source did not record groups under the explicit
// unrecorded key rather than being dropped.
func (a *groupAccumulator) groupKey(record frictionRecord) string {
	switch a.group {
	case "category":
		return orUnrecorded(record.category)
	case "tool":
		return orUnrecorded(record.toolName)
	case "signature":
		return orUnrecorded(record.signature)
	default:
		return record.projectKey + "\x1f" + record.harness
	}
}

func (a *groupAccumulator) add(record frictionRecord) {
	key := a.groupKey(record)
	position, ok := a.index[key]
	if !ok {
		position = len(a.items)
		a.index[key] = position
		a.items = append(a.items, &groupState{key: key,
			sessions: make(map[string]struct{}), projects: make(map[string]struct{}),
			windowSessions: make(map[string]struct{})})
	}
	item := a.items[position]
	switch a.group {
	case "category":
		item.category = orUnrecorded(record.category)
		if item.categoryRule == "" {
			item.categoryRule, item.categoryRuleEN = record.categoryRule, record.categoryRuleEN
		}
	case "tool":
		item.toolName = orUnrecorded(record.toolName)
	case "signature":
		item.signature = orUnrecorded(record.signature)
		if record.category > item.category {
			item.category = record.category
		}
		if record.categoryRule > item.categoryRule {
			item.categoryRule, item.categoryRuleEN = record.categoryRule, record.categoryRuleEN
		}
		if record.toolName > item.toolName {
			item.toolName = record.toolName
		}
	default:
		item.projectKey = record.projectKey
		item.harness = record.harness
		if record.cwd > item.cwd {
			item.cwd = record.cwd
		}
	}
	item.frictionCount++
	if record.kinds&kindToolError != 0 {
		item.toolError++
	}
	if record.kinds&kindNonzeroExit != 0 {
		item.nonzeroExit++
	}
	if record.kinds&kindAssetViolation != 0 {
		item.assetViolation++
	}
	if record.kinds&kindUserInterrupt != 0 {
		item.userInterrupt++
	}
	item.sessions[record.sessionID] = struct{}{}
	item.projects[record.projectKey] = struct{}{}
	if record.occurredAt != "" {
		if item.first == "" || record.occurredAt < item.first {
			item.first = record.occurredAt
		}
		if record.occurredAt > item.last {
			item.last = record.occurredAt
		}
		inWindow := (a.cutoff == "" || record.occurredAt >= a.cutoff) && (a.upper == "" || record.occurredAt <= a.upper)
		if inWindow {
			item.windowCount++
			item.windowSessions[record.sessionID] = struct{}{}
		}
	}
}

// groups turns the loaded records into the group list the API returns, in the
// order the requested sort defines.
func (set frictionSet) groups(filters frictionFilters, cutoff, upper string) []frictionGroupResponse {
	accumulator := newGroupAccumulator(filters.Group, cutoff, upper)
	for _, record := range set.records {
		accumulator.add(record)
	}
	out := make([]frictionGroupResponse, 0, len(accumulator.items))
	for _, item := range accumulator.items {
		group := frictionGroupResponse{
			GroupBy: filters.Group, FrictionCount: item.frictionCount, Count: item.frictionCount,
			ToolErrorCount: item.toolError, NonzeroExitCount: item.nonzeroExit,
			AssetViolationCount: item.assetViolation, UserInterruptCount: item.userInterrupt,
			SessionCount: len(item.sessions), ProjectCount: len(item.projects),
			FirstOccurredAt: item.first, LastOccurredAt: item.last,
			SessionsLastWindow: len(item.windowSessions), CountLastWindow: item.windowCount,
		}
		switch filters.Group {
		case "signature":
			group.Signature = item.signature
			group.Category, group.CategoryRule, group.CategoryRuleEN = item.category, item.categoryRule, item.categoryRuleEN
			group.ToolName = item.toolName
			group.SampleLine = frictionSignatureLine(group.Signature)
			group.Key = group.Signature
			group.Label = group.SampleLine
			if group.Label == "" {
				group.Label = group.Signature
			}
		case "category":
			group.Category, group.CategoryRule, group.CategoryRuleEN = item.category, item.categoryRule, item.categoryRuleEN
			group.Key, group.Label = group.Category, group.Category
		case "tool":
			group.ToolName = item.toolName
			group.Key, group.Label = group.ToolName, group.ToolName
		default:
			group.ProjectKey = item.projectKey
			group.Harness = item.harness
			group.ProjectLabel = frictionProjectLabel(item.projectKey, sql.NullString{String: item.cwd, Valid: item.cwd != ""})
			group.Key = item.key
			group.Label = group.ProjectLabel
			if item.cwd != "" {
				value := item.cwd
				group.CWD = &value
			}
		}
		group.DaysActive = wholeDaysBetween(group.FirstOccurredAt, group.LastOccurredAt)
		if filters.Group == "signature" {
			group.Hint = friction.LookupHint(group.Signature)
			group.Status = frictionStatus(group, cutoff)
		}
		out = append(out, group)
	}
	sortFrictionGroups(out, filters, cutoff)
	return out
}

// sortFrictionGroups applies the documented order: the signature view leads
// with what is still happening, the others with how much or how recent.
func sortFrictionGroups(groups []frictionGroupResponse, filters frictionFilters, cutoff string) {
	isActive := func(group frictionGroupResponse) bool {
		return group.Status == FrictionStatusActive
	}
	sort.SliceStable(groups, func(i, j int) bool {
		left, right := groups[i], groups[j]
		switch {
		case filters.Sort == "recent":
			if (left.LastOccurredAt == "") != (right.LastOccurredAt == "") {
				return right.LastOccurredAt == ""
			}
			if left.LastOccurredAt != right.LastOccurredAt {
				return left.LastOccurredAt > right.LastOccurredAt
			}
			if left.FrictionCount != right.FrictionCount {
				return left.FrictionCount > right.FrictionCount
			}
		case filters.Sort == "sessions" && filters.Group == "signature":
			if isActive(left) != isActive(right) {
				return isActive(left)
			}
			if left.SessionsLastWindow != right.SessionsLastWindow {
				return left.SessionsLastWindow > right.SessionsLastWindow
			}
			fallthrough
		case filters.Sort == "sessions":
			if left.SessionCount != right.SessionCount {
				return left.SessionCount > right.SessionCount
			}
			if left.FrictionCount != right.FrictionCount {
				return left.FrictionCount > right.FrictionCount
			}
			if (left.LastOccurredAt == "") != (right.LastOccurredAt == "") {
				return right.LastOccurredAt == ""
			}
			if left.LastOccurredAt != right.LastOccurredAt {
				return left.LastOccurredAt > right.LastOccurredAt
			}
		default:
			if left.FrictionCount != right.FrictionCount {
				return left.FrictionCount > right.FrictionCount
			}
			if (left.LastOccurredAt == "") != (right.LastOccurredAt == "") {
				return right.LastOccurredAt == ""
			}
			if left.LastOccurredAt != right.LastOccurredAt {
				return left.LastOccurredAt > right.LastOccurredAt
			}
		}
		return left.Key < right.Key
	})
}

// page returns the requested slice of a group list.
func page(groups []frictionGroupResponse, offset, limit int) []frictionGroupResponse {
	if offset >= len(groups) {
		return make([]frictionGroupResponse, 0)
	}
	end := offset + limit
	if limit <= 0 || end > len(groups) {
		end = len(groups)
	}
	return groups[offset:end]
}

// projectOptions is the project filter list of the friction page: every
// project in the loaded set, in key order.
func (set frictionSet) projectOptions() []frictionProjectOptionResponse {
	byKey := make(map[string]string)
	for _, record := range set.records {
		if record.cwd > byKey[record.projectKey] {
			byKey[record.projectKey] = record.cwd
		}
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]frictionProjectOptionResponse, 0, len(keys))
	for _, key := range keys {
		cwd := byKey[key]
		option := frictionProjectOptionResponse{Key: key,
			Label: frictionProjectLabel(key, sql.NullString{String: cwd, Valid: cwd != ""})}
		if cwd != "" {
			value := cwd
			option.CWD = &value
		}
		out = append(out, option)
	}
	return out
}

// signatureProjectKeys lists the projects each signature was recorded in.
func (set frictionSet) signatureProjectKeys() map[string]map[string]struct{} {
	out := make(map[string]map[string]struct{})
	for _, record := range set.records {
		if record.signature == "" {
			continue
		}
		projects, ok := out[record.signature]
		if !ok {
			projects = make(map[string]struct{})
			out[record.signature] = projects
		}
		projects[record.projectKey] = struct{}{}
	}
	return out
}
