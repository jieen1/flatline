package history

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"

	"flatline/internal/adapters"
	"flatline/internal/eventstore"
	"flatline/internal/ingest"
)

// dsh writes one zstd-compressed JSONL file per session under
// <root>/<project-slug>/session-<uuid>/session.jsonl.zstd. The file is the
// unit of change, so the ordinary size/mtime fingerprint applies (§19.3).
const dshSessionFile = "session.jsonl.zstd"

// dshMaxLine bounds one decoded record. A single dsh record can hold a whole
// tool output, so the scanner needs far more than bufio's default.
const dshMaxLine = 64 << 20

type dshRecord struct {
	Type      string          `json:"type"`
	Time      int64           `json:"time"`
	TimeAlt   int64           `json:"time0"`
	Seq       *int64          `json:"seq"`
	Data      json.RawMessage `json:"data"`
	SessionID string          `json:"id"`

	CWD             string `json:"cwd"`
	CreatedAt       int64  `json:"createdAt"`
	AgentPreset     string `json:"agentPreset"`
	DelegationDepth *int   `json:"delegationDepth"`
}

func (r dshRecord) at() int64 {
	if r.Time > 0 {
		return r.Time
	}
	return r.TimeAlt
}

type dshContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type dshToolResultBlock struct {
	Type       string       `json:"type"`
	ToolCallID string       `json:"toolCallId"`
	IsError    *bool        `json:"isError"`
	Content    []dshContent `json:"content"`
}

// readDSH reads one dsh session file into a normalized session.
func readDSH(file string, index assetIndex, projectRoot string) (Session, int, bool, string) {
	var sessionID, cwd, model, title, taskText string
	var contextWindow *int64
	var started, ended *time.Time
	var messages []normalizedMessage
	thread := threadInfo{Originator: string(adapters.SourceDSH)}
	var inputTokens, outputTokens, cachedTokens int64
	var assistantTurns, userTurns int64
	evidence := 0

	err := eachZstdJSONLine(file, func(line int, raw []byte) error {
		var record dshRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return fmt.Errorf("line %d: %w", line, err)
		}
		if at := millisToTime(record.at()); at != nil {
			started, ended = extendBounds(started, ended, at)
		}
		ref := fmt.Sprintf("line-%d", line)
		if record.Seq != nil {
			ref = fmt.Sprintf("seq-%d", *record.Seq)
		}
		at := formatMillis(record.at())

		switch record.Type {
		case "session":
			sessionID = record.SessionID
			cwd = record.CWD
			thread.AgentRole = strings.TrimSpace(record.AgentPreset)
			// delegationDepth is the only hierarchy fact dsh records. It says
			// a session was delegated but never names the delegator, so the
			// parent stays unrecorded instead of being guessed.
			if record.DelegationDepth != nil {
				if *record.DelegationDepth > 0 {
					thread.Kind = threadKindSubagent
				} else {
					thread.Kind = threadKindMain
				}
			}
			if at := millisToTime(record.CreatedAt); at != nil {
				started, ended = extendBounds(started, ended, at)
			}
		case "session/title":
			var data struct {
				Title string `json:"title"`
			}
			if json.Unmarshal(record.Data, &data) == nil && title == "" {
				title, _ = boundText(data.Title, maxSessionTitle)
			}
		case "request/context":
			var data struct {
				Model         string `json:"model"`
				ContextWindow int64  `json:"contextWindow"`
			}
			if json.Unmarshal(record.Data, &data) == nil {
				if model == "" {
					model = data.Model
				}
				if contextWindow == nil && data.ContextWindow > 0 {
					contextWindow = int64Ptr(data.ContextWindow)
				}
			}
		case "user/message":
			var data struct {
				Role    string       `json:"role"`
				ID      string       `json:"id"`
				Content []dshContent `json:"content"`
			}
			if json.Unmarshal(record.Data, &data) != nil {
				return nil
			}
			userTurns++
			for _, block := range data.Content {
				if block.Type != "text" {
					continue
				}
				text, truncated := boundText(block.Text, maxTranscriptText)
				if text == "" || isGeneratedNoise(text) {
					continue
				}
				if taskText == "" && meaningfulTaskText(text) {
					taskText, _ = boundText(text, maxTaskText)
				}
				messages = append(messages, normalizedMessage{ID: firstNonEmpty(data.ID, ref),
					Timestamp: at, Role: "user", Kind: "message", Text: text, Truncated: truncated})
			}
		case "assistant/message":
			var data struct {
				Message struct {
					ID      string       `json:"id"`
					Content []dshContent `json:"content"`
				} `json:"message"`
				Usage struct {
					InputTokens     int64 `json:"inputTokens"`
					OutputTokens    int64 `json:"outputTokens"`
					CacheReadTokens int64 `json:"cacheReadTokens"`
				} `json:"usage"`
			}
			if json.Unmarshal(record.Data, &data) != nil {
				return nil
			}
			assistantTurns++
			inputTokens += data.Usage.InputTokens
			outputTokens += data.Usage.OutputTokens
			cachedTokens += data.Usage.CacheReadTokens
			for _, block := range data.Message.Content {
				// tool-call blocks repeat what the tool/call record already
				// carries, and reasoning is not transcript text.
				if block.Type != "text" {
					continue
				}
				text, truncated := boundText(block.Text, maxTranscriptText)
				if text == "" || isGeneratedNoise(text) {
					continue
				}
				messages = append(messages, normalizedMessage{ID: firstNonEmpty(data.Message.ID, ref),
					Timestamp: at, Role: "assistant", Kind: "message", Text: text, Truncated: truncated})
			}
		case "tool/call":
			var data struct {
				CallID    string `json:"callId"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}
			if json.Unmarshal(record.Data, &data) != nil {
				return nil
			}
			input, truncated := boundText(data.Arguments, maxToolPayload)
			invocations := index.invocationsInText(input)
			evidence += len(invocations)
			messages = append(messages, normalizedMessage{ID: firstNonEmpty(data.CallID, ref),
				Timestamp: at, Role: "assistant", Kind: "tool_call", ToolName: data.Name,
				CallID: data.CallID, ToolInput: input, Truncated: truncated, Invocations: invocations})
		case "tool/result":
			var data struct {
				Error   json.RawMessage `json:"error"`
				Message struct {
					ID     string `json:"id"`
					Source struct {
						CallID string `json:"callId"`
					} `json:"source"`
					Content []dshToolResultBlock `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal(record.Data, &data) != nil {
				return nil
			}
			callID := data.Message.Source.CallID
			var texts []string
			var failed *bool
			for _, block := range data.Message.Content {
				if callID == "" {
					callID = block.ToolCallID
				}
				if block.IsError != nil {
					value := *block.IsError
					failed = &value
				}
				for _, inner := range block.Content {
					if inner.Type == "text" && inner.Text != "" {
						texts = append(texts, inner.Text)
					}
				}
			}
			if len(data.Error) > 0 && string(data.Error) != "null" {
				value := true
				failed = &value
				texts = append(texts, string(data.Error))
			}
			output, truncated := boundText(strings.Join(texts, "\n"), maxToolPayload)
			// dsh records no process exit status anywhere, so exit_code comes
			// only from a status line the tool printed into its own output.
			isError, exitCode := normalizeToolFailure(output, failed, nil)
			messages = append(messages, normalizedMessage{ID: firstNonEmpty(callID, data.Message.ID, ref),
				Timestamp: at, Role: "tool", Kind: "tool_result", CallID: callID,
				ToolOutput: output, Truncated: truncated, IsError: isError, ExitCode: exitCode})
		case "turn/end":
			var data struct {
				Reason struct {
					Kind string `json:"kind"`
				} `json:"reason"`
			}
			if json.Unmarshal(record.Data, &data) != nil {
				return nil
			}
			// Only a turn that did not complete is recorded. A completed turn
			// produces no record, and its absence is never read as a failure.
			if data.Reason.Kind == "" || data.Reason.Kind == "completed" {
				return nil
			}
			messages = append(messages, normalizedMessage{ID: ref, Timestamp: at,
				Role: "system", Kind: "message", AbortReason: data.Reason.Kind})
		}
		return nil
	})
	warning := warn(file, err)

	if sessionID == "" {
		sessionID = filepath.Base(filepath.Dir(file))
	}
	if projectRoot != "" && (cwd == "" || !within(projectRoot, cwd)) {
		return Session{}, 0, false, warning
	}
	if title == "" && taskText != "" {
		title, _ = boundText(taskText, maxSessionTitle)
	}

	usage := &eventstore.SessionUsage{
		InputTokens:       positiveInt64(inputTokens),
		CachedInputTokens: positiveInt64(cachedTokens),
		OutputTokens:      positiveInt64(outputTokens),
		AssistantTurns:    int64Ptr(assistantTurns),
		UserTurns:         int64Ptr(userTurns),
		ContextWindow:     contextWindow,
		Source:            UsageSourceDSH,
	}
	// dsh keeps inputTokens and cacheReadTokens apart (A5 field matrix §dsh)
	// and emits neither a cache-write nor a reasoning count, so the components
	// map straight across and the total follows TokenTotalRule.
	usage.RecomputeTotal()
	if model != "" && usage.TotalTokens != nil {
		usage.ByModel = []eventstore.ModelUsage{{
			Model: model, Turns: assistantTurns,
			InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, TotalTokens: usage.TotalTokens,
		}}
	}
	if usage.TotalTokens == nil {
		usage.Source = eventstore.UsageSourceUnrecorded
	}

	// dsh records the session-record schema version, not a harness version, so
	// harness_version stays unrecorded rather than carrying a misleading "0".
	// dsh records no cost anywhere in the transcript.
	raw, marshalErr := marshalNormalized(sessionID, cwd, "", model, title, taskText,
		started, ended, thread, usage, nil, messages)
	if marshalErr != nil {
		return Session{}, 0, false, fmt.Sprintf("%s: normalize: %v", file, marshalErr)
	}
	return Session{Input: ingest.SessionInput{
		Raw: adapters.RawSession{Source: adapters.SourceDSH, SessionID: sessionID,
			RawJSON: raw, SourcePath: file},
		TaskTags:            nativeTaskTags(taskText, cwd),
		OpportunityAssetIDs: nativeOpportunityAssetIDs(taskText, messages, index),
		Usage:               usage,
		ParserVersion:       ParserVersion,
	}, SourcePath: file}, evidence, true, warning
}

// eachZstdJSONLine streams one zstd-compressed JSONL file. Decoding happens in
// pure Go (ADR-19); nothing is written back and the file is opened read-only.
func eachZstdJSONLine(file string, fn func(line int, raw []byte) error) error {
	input, err := os.Open(file)
	if err != nil {
		return err
	}
	defer input.Close()
	decoder, err := zstd.NewReader(input)
	if err != nil {
		return fmt.Errorf("zstd: %w", err)
	}
	defer decoder.Close()

	scanner := bufio.NewScanner(decoder)
	scanner.Buffer(make([]byte, 0, 64*1024), dshMaxLine)
	line := 0
	for scanner.Scan() {
		line++
		raw := scanner.Bytes()
		if len(strings.TrimSpace(string(raw))) == 0 {
			continue
		}
		if err := fn(line, raw); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// dshFiles lists every session file under the configured roots in path order.
func dshFiles(primary string, extra []string) ([]string, error) {
	roots := make([]string, 0, 1+len(extra))
	seenRoots := make(map[string]struct{}, 1+len(extra))
	for _, root := range append([]string{primary}, extra...) {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		root = filepath.Clean(root)
		if _, seen := seenRoots[root]; seen {
			continue
		}
		seenRoots[root] = struct{}{}
		roots = append(roots, root)
	}
	var files []string
	seenFiles := make(map[string]struct{})
	for _, root := range roots {
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			continue
		}
		err = filepath.Walk(root, func(path string, entry os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if entry.IsDir() || filepath.Base(path) != dshSessionFile {
				return nil
			}
			if _, seen := seenFiles[path]; seen {
				return nil
			}
			seenFiles[path] = struct{}{}
			files = append(files, path)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(files)
	return files, nil
}
