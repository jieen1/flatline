package history

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"flatline/internal/eventstore"
)

// ParserVersion changes whenever this package reads a fact it did not read
// before, or whenever a stored fact it writes has to be read again under a new
// rule. A native file stamped with a different version is read once more and
// replayed through the normal ingest path; events are inserted idempotently,
// so a newer parser only adds what an older one missed.
//
// parser/4 and parser/5 are the second kind: the session span now takes the
// latest reading rather than the first, and the measurement roll-up now counts
// a transcript that is on disk under two paths once. Rows written under the old
// rules can only be repaired by reading their transcripts again — the event
// store holds the last stored event, which for a Codex session is not always
// the last record.
const ParserVersion = "parser/8"

// idleBound is how long a gap between two recorded events may be and still
// count as time spent in the session. A longer gap is idle: the session was
// open, but nothing was happening in it.
const idleBound = 10 * time.Minute

// syntheticModel is what Claude Code writes into message.model for a message
// it produced locally rather than from a model. It is not a model, so it does
// not get a bucket.
const syntheticModel = "<synthetic>"

// claudeEndTurn is the stop reason Claude Code writes when the assistant
// finished its reply rather than stopping to run a tool. One of them is one
// assistant turn.
const claudeEndTurn = "end_turn"

// usageAccumulator collects the measurement facts one transcript records. Each
// counter carries its own "was this ever recorded" flag, so a session with no
// token record stores NULL rather than zero.
type usageAccumulator struct {
	source string

	input, cachedInput, cacheWrite, output, reasoning int64
	tokensKnown                                       bool

	contextWindow int64
	contextKnown  bool

	assistantTurns, userTurns int64

	linesAdded, linesRemoved int64
	changesKnown             bool
	files                    map[string]struct{}

	// claudeMessages holds one entry per assistant message. Claude Code writes
	// the same message.id out again on every follow-up record, and a streaming
	// model's usage block grows as it writes: on this machine one subagent
	// transcript sums to 14,655 output tokens by its messages' first records
	// and 161,497 by their last. The last record for an id is the final count,
	// so entries are replaced, not added up. seenTurns dedupes the record that
	// closes the same message.
	claudeMessages map[string]*claudeMessageUsage
	claudeOrder    []string
	seenTurns      map[string]struct{}

	models     map[string]*modelBucket
	modelOrder []string

	activeMS int64
	lastAt   time.Time
	hasLast  bool
}

// claudeMessageUsage is one assistant message's final usage.
type claudeMessageUsage struct {
	model                                           string
	input, cacheRead, cacheWrite, output, reasoning int64
}

type modelBucket struct {
	turns                int64
	input, output, total int64
	tokensKnown          bool
}

func newUsageAccumulator() *usageAccumulator {
	return &usageAccumulator{
		files:          make(map[string]struct{}),
		claudeMessages: make(map[string]*claudeMessageUsage),
		seenTurns:      make(map[string]struct{}),
		models:         make(map[string]*modelBucket),
	}
}

// observe advances the active-time clock. Records arrive in the order the
// source wrote them; a timestamp that goes backwards is ignored rather than
// subtracted.
func (u *usageAccumulator) observe(at *time.Time) {
	if at == nil {
		return
	}
	if u.hasLast {
		if gap := at.Sub(u.lastAt); gap > 0 && gap <= idleBound {
			u.activeMS += gap.Milliseconds()
		}
	}
	if !u.hasLast || at.After(u.lastAt) {
		u.lastAt, u.hasLast = *at, true
	}
}

func (u *usageAccumulator) bucket(model string) *modelBucket {
	model = strings.TrimSpace(model)
	if model == "" || model == syntheticModel {
		return nil
	}
	item, ok := u.models[model]
	if !ok {
		item = &modelBucket{}
		u.models[model] = item
		u.modelOrder = append(u.modelOrder, model)
	}
	return item
}

// claudeUsage is the usage block Claude Code writes on every assistant record.
type claudeUsage struct {
	InputTokens              *int64 `json:"input_tokens"`
	CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int64 `json:"cache_read_input_tokens"`
	OutputTokens             *int64 `json:"output_tokens"`
	OutputTokensDetails      *struct {
		ThinkingTokens *int64 `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
}

// addClaudeUsage records one assistant message's usage, keyed on the message it
// belongs to. Claude Code repeats the same message.id across several records,
// so adding every record up multiplies the session's tokens; a streaming
// model's usage block also grows across those records, so the last one is the
// message's final count and it replaces the earlier ones.
func (u *usageAccumulator) addClaudeUsage(messageID, model string, usage *claudeUsage) {
	if usage == nil {
		return
	}
	if messageID == "" {
		// A record with no message id cannot be deduplicated against anything;
		// it is its own message.
		messageID = fmt.Sprintf("record-%d", len(u.claudeOrder))
	}
	entry, ok := u.claudeMessages[messageID]
	if !ok {
		entry = &claudeMessageUsage{}
		u.claudeMessages[messageID] = entry
		u.claudeOrder = append(u.claudeOrder, messageID)
	}
	entry.model = strings.TrimSpace(model)
	entry.input = value(usage.InputTokens)
	entry.cacheRead = value(usage.CacheReadInputTokens)
	entry.cacheWrite = value(usage.CacheCreationInputTokens)
	entry.output = value(usage.OutputTokens)
	entry.reasoning = 0
	if usage.OutputTokensDetails != nil {
		entry.reasoning = value(usage.OutputTokensDetails.ThinkingTokens)
	}
}

// foldClaudeMessages turns the per-message final counts into the session
// totals and the per-model split.
//
// Claude Code keeps the three input counters apart: message.usage.input_tokens
// is the input that was not served from cache, and cache_read_input_tokens and
// cache_creation_input_tokens are separate from it (one local record reads
// input 2 / cache_read 26,249 / cache_creation 13,595). They therefore map
// straight onto the stored components, and the total follows TokenTotalRule.
func (u *usageAccumulator) foldClaudeMessages() {
	if len(u.claudeMessages) == 0 {
		return
	}
	u.source = eventstore.UsageSourceClaude
	u.tokensKnown = true
	for _, id := range u.claudeOrder {
		entry := u.claudeMessages[id]
		u.input += entry.input
		u.cachedInput += entry.cacheRead
		u.cacheWrite += entry.cacheWrite
		u.output += entry.output
		u.reasoning += entry.reasoning
		if item := u.bucket(entry.model); item != nil {
			item.tokensKnown = true
			item.input += entry.input
			item.output += entry.output
			item.total += entry.input + entry.cacheRead + entry.cacheWrite + entry.output
		}
	}
}

// codexTokens is one of the two usage blocks Codex writes in a token_count
// event: the running total, and the last turn's share of it.
type codexTokens struct {
	InputTokens           *int64 `json:"input_tokens"`
	CachedInputTokens     *int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens *int64 `json:"cache_write_input_tokens"`
	OutputTokens          *int64 `json:"output_tokens"`
	ReasoningOutputTokens *int64 `json:"reasoning_output_tokens"`
	TotalTokens           *int64 `json:"total_tokens"`
}

type codexTokenInfo struct {
	TotalTokenUsage    *codexTokens `json:"total_token_usage"`
	LastTokenUsage     *codexTokens `json:"last_token_usage"`
	ModelContextWindow *int64       `json:"model_context_window"`
}

// codexUncachedInput is the part of a Codex input count that was not served
// from cache.
//
// Codex reports input_tokens as the whole input of the turn and names the
// cached share inside it: every one of the 171,680 local usage blocks has
// cached_input_tokens <= input_tokens, and total_tokens == input_tokens +
// output_tokens holds on all of them but the 1,593 that record no components
// at all. cache_write_input_tokens is 0 in every local block, so nothing here
// can show whether it too sits inside input_tokens; it is subtracted on the
// same assumption as the cached share, which is the reading that keeps the
// components adding up to Codex's own total.
func codexUncachedInput(usage *codexTokens) int64 {
	uncached := value(usage.InputTokens) - value(usage.CachedInputTokens) - value(usage.CacheWriteInputTokens)
	if uncached < 0 {
		return 0
	}
	return uncached
}

// setCodexTotals replaces the session totals with the running total Codex just
// wrote. The field is cumulative, so the last record is the session's total
// and summing the records would multiply it. The total itself is not stored:
// it is recomputed from the components by TokenTotalRule.
func (u *usageAccumulator) setCodexTotals(info codexTokenInfo, model string) {
	if info.ModelContextWindow != nil {
		u.contextWindow, u.contextKnown = *info.ModelContextWindow, true
	}
	if info.TotalTokenUsage != nil {
		usage := info.TotalTokenUsage
		u.source = eventstore.UsageSourceCodex
		u.tokensKnown = true
		u.input = codexUncachedInput(usage)
		u.cachedInput = value(usage.CachedInputTokens)
		u.cacheWrite = value(usage.CacheWriteInputTokens)
		u.output = value(usage.OutputTokens)
		u.reasoning = value(usage.ReasoningOutputTokens)
	}
	// The per-turn block is the only one that can be attributed to the model
	// the turn ran on, so the model split is built out of it.
	if info.LastTokenUsage != nil {
		if item := u.bucket(model); item != nil {
			usage := info.LastTokenUsage
			item.tokensKnown = true
			item.input += codexUncachedInput(usage)
			item.output += value(usage.OutputTokens)
			item.total += value(usage.InputTokens) + value(usage.OutputTokens)
		}
	}
}

// addClaudeAssistantTurn counts one finished assistant reply, once: the record
// that closes a message is repeated the same way its usage block is.
func (u *usageAccumulator) addClaudeAssistantTurn(messageID, model string) {
	if messageID != "" {
		if _, seen := u.seenTurns[messageID]; seen {
			return
		}
		u.seenTurns[messageID] = struct{}{}
	}
	u.assistantTurns++
	if item := u.bucket(model); item != nil {
		item.turns++
	}
}

func (u *usageAccumulator) addAssistantTurn(model string) {
	u.assistantTurns++
	if item := u.bucket(model); item != nil {
		item.turns++
	}
}

func (u *usageAccumulator) addUserTurn() { u.userTurns++ }

// recordChange adds one tool call's edit. The counts are taken from the full
// recorded input, before the bounded copy that goes into the event payload:
// a 20 KB patch is measured in full and stored truncated.
func (u *usageAccumulator) recordChange(added, removed int64, files []string) {
	if added == 0 && removed == 0 && len(files) == 0 {
		return
	}
	u.changesKnown = true
	u.linesAdded += added
	u.linesRemoved += removed
	for _, file := range files {
		if file = strings.TrimSpace(file); file != "" {
			u.files[file] = struct{}{}
		}
	}
}

// result is the measurement row this transcript produced. A transcript with no
// token record still produces a row: the turns, the edits and the active time
// are recorded facts, and usage_source says the tokens were not.
func (u *usageAccumulator) result() *eventstore.SessionUsage {
	u.foldClaudeMessages()
	out := &eventstore.SessionUsage{Source: eventstore.UsageSourceUnrecorded}
	if u.tokensKnown {
		out.Source = u.source
		out.InputTokens = known(u.input)
		out.CachedInputTokens = known(u.cachedInput)
		out.CacheWriteTokens = known(u.cacheWrite)
		out.OutputTokens = known(u.output)
		out.ReasoningTokens = known(u.reasoning)
	}
	out.RecomputeTotal()
	if u.contextKnown {
		out.ContextWindow = known(u.contextWindow)
	}
	out.AssistantTurns = known(u.assistantTurns)
	out.UserTurns = known(u.userTurns)
	if u.changesKnown {
		out.LinesAdded = known(u.linesAdded)
		out.LinesRemoved = known(u.linesRemoved)
		out.FilesChanged = known(int64(len(u.files)))
	}
	if u.hasLast {
		out.ActiveMS = known(u.activeMS)
	}
	for _, model := range u.modelOrder {
		item := u.models[model]
		entry := eventstore.ModelUsage{Model: model, Turns: item.turns}
		if item.tokensKnown {
			entry.InputTokens = known(item.input)
			entry.OutputTokens = known(item.output)
			entry.TotalTokens = known(item.total)
		}
		out.ByModel = append(out.ByModel, entry)
	}
	sort.SliceStable(out.ByModel, func(i, j int) bool { return out.ByModel[i].Model < out.ByModel[j].Model })
	return out
}

func value(item *int64) int64 {
	if item == nil {
		return 0
	}
	return *item
}

func known(item int64) *int64 { return &item }

// usageMap is the measurement block the normalized session carries, so a
// reader for a new harness produces the same shape and the adapter fixtures
// exercise it.
func usageMap(usage *eventstore.SessionUsage) map[string]any {
	if usage == nil {
		return nil
	}
	out := map[string]any{"source": usage.Source}
	for key, item := range map[string]*int64{
		"input_tokens": usage.InputTokens, "cached_input_tokens": usage.CachedInputTokens,
		"cache_write_tokens": usage.CacheWriteTokens, "output_tokens": usage.OutputTokens,
		"reasoning_tokens": usage.ReasoningTokens, "total_tokens": usage.TotalTokens,
		"assistant_turns": usage.AssistantTurns, "user_turns": usage.UserTurns,
		"lines_added": usage.LinesAdded, "lines_removed": usage.LinesRemoved,
		"files_changed": usage.FilesChanged, "active_ms": usage.ActiveMS,
		"context_window": usage.ContextWindow,
	} {
		if item != nil {
			out[key] = *item
		}
	}
	if len(usage.ByModel) > 0 {
		models := make([]any, 0, len(usage.ByModel))
		for _, model := range usage.ByModel {
			entry := map[string]any{"model": model.Model, "turns": model.Turns}
			for key, item := range map[string]*int64{
				"input_tokens": model.InputTokens, "output_tokens": model.OutputTokens,
				"total_tokens": model.TotalTokens,
			} {
				if item != nil {
					entry[key] = *item
				}
			}
			models = append(models, entry)
		}
		out["by_model"] = models
	}
	return out
}

// changeCounts measures one tool call's edit from its full recorded input.
// Only the fields the tool itself names as the text being written are read; a
// call that names none produces no change.
func changeCounts(toolName string, input map[string]any) (int64, int64, []string) {
	switch toolName {
	case "Edit":
		return editCounts(input, stringField(input, "file_path"))
	case "MultiEdit":
		path := stringField(input, "file_path")
		var added, removed int64
		var files []string
		edits, _ := input["edits"].([]any)
		for _, raw := range edits {
			edit, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			a, r, f := editCounts(edit, path)
			added, removed = added+a, removed+r
			files = append(files, f...)
		}
		return added, removed, files
	case "Write":
		content := stringField(input, "content")
		path := stringField(input, "file_path")
		if content == "" && path == "" {
			return 0, 0, nil
		}
		return lineCount(content), 0, filesOf(path)
	case "NotebookEdit":
		source := stringField(input, "new_source")
		path := stringField(input, "notebook_path")
		if source == "" && path == "" {
			return 0, 0, nil
		}
		return lineCount(source), 0, filesOf(path)
	default:
		return 0, 0, nil
	}
}

// editCounts reads one replacement as a diff hunk: the text that went away is
// counted as removed lines and the text that replaced it as added lines, the
// same convention apply_patch's own +/- lines follow.
func editCounts(input map[string]any, path string) (int64, int64, []string) {
	oldText := stringField(input, "old_string")
	newText := stringField(input, "new_string")
	if oldText == "" && newText == "" {
		return 0, 0, nil
	}
	return lineCount(newText), lineCount(oldText), filesOf(path)
}

func filesOf(path string) []string {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	return []string{path}
}

func stringField(input map[string]any, key string) string {
	value, _ := input[key].(string)
	return value
}

func lineCount(text string) int64 {
	if text == "" {
		return 0
	}
	return int64(strings.Count(text, "\n") + 1)
}

const (
	patchBegin      = "*** Begin Patch"
	patchEnd        = "*** End Patch"
	patchUpdateFile = "*** Update File:"
	patchAddFile    = "*** Add File:"
	patchDeleteFile = "*** Delete File:"
)

// patchChanges counts the added and removed lines of every file section in an
// apply_patch body, wherever it appears in a recorded tool input — Codex
// writes the patch inside an exec script, so the body arrives as a shell or
// JavaScript string literal with its newlines escaped.
func patchChanges(input string) (int64, int64, []string) {
	if !strings.Contains(input, patchBegin) {
		return 0, 0, nil
	}
	body := input[strings.Index(input, patchBegin):]
	if end := strings.Index(body, patchEnd); end >= 0 {
		body = body[:end]
	}
	if strings.Count(body, `\n`) > strings.Count(body, "\n") {
		body = strings.ReplaceAll(body, `\n`, "\n")
	}
	var added, removed int64
	var files []string
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if path, ok := patchFileHeader(trimmed); ok {
			files = append(files, path)
			continue
		}
		if strings.HasPrefix(trimmed, "***") || strings.HasPrefix(trimmed, "@@") {
			continue
		}
		switch {
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
		}
	}
	return added, removed, files
}

func patchFileHeader(line string) (string, bool) {
	for _, prefix := range []string{patchUpdateFile, patchAddFile, patchDeleteFile} {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		path := strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, prefix)), `"'`)
		if path == "" {
			return "", false
		}
		return path, true
	}
	return "", false
}

// decodeClaudeUsage reads the usage block off a raw Claude Code record without
// re-decoding the message content, which is the expensive part.
func decodeClaudeUsage(raw json.RawMessage) *claudeUsage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var usage claudeUsage
	if json.Unmarshal(raw, &usage) != nil {
		return nil
	}
	return &usage
}

// injectedSkips tallies the harness-injected blocks the reader left out of the
// transcript, by the prefix that identified each one. The daemon logs the tally
// after every pass so the exclusion is a reported number rather than a silent
// edit of the record.
var injectedSkips = struct {
	mu    sync.Mutex
	byTag map[string]int
}{byTag: make(map[string]int)}

func noteInjectedSkip(tag string) {
	injectedSkips.mu.Lock()
	injectedSkips.byTag[tag]++
	injectedSkips.mu.Unlock()
}

// InjectedSkipCounts is what the readers have skipped so far, by prefix.
func InjectedSkipCounts() map[string]int {
	injectedSkips.mu.Lock()
	defer injectedSkips.mu.Unlock()
	out := make(map[string]int, len(injectedSkips.byTag))
	for tag, count := range injectedSkips.byTag {
		out[tag] = count
	}
	return out
}

// MessageUsage is one assistant message's own token record, as the source
// wrote it on that message. It exists so the transcript can carry what each
// turn cost instead of only the session total.
type MessageUsage struct {
	Input       int64 `json:"input_tokens"`
	CachedInput int64 `json:"cached_input_tokens"`
	CacheWrite  int64 `json:"cache_write_tokens"`
	Output      int64 `json:"output_tokens"`
	Reasoning   int64 `json:"reasoning_tokens"`
	Total       int64 `json:"total_tokens"`
}

// UsageForMessage returns one assistant message's final usage. It answers
// false for a message the source attached no usage to — missing stays missing,
// and the caller records the denominator rather than filling a zero. Call it
// only after result(), which folds the per-message entries to their final
// values (a streaming message's usage grows across its follow-up records).
func (u *usageAccumulator) UsageForMessage(messageID string) (MessageUsage, bool) {
	entry, found := u.claudeMessages[messageID]
	if !found || entry == nil {
		return MessageUsage{}, false
	}
	out := MessageUsage{
		Input:       entry.input,
		CachedInput: entry.cacheRead,
		CacheWrite:  entry.cacheWrite,
		Output:      entry.output,
		Reasoning:   entry.reasoning,
	}
	out.Total = out.Input + out.CachedInput + out.CacheWrite + out.Output
	return out, true
}
