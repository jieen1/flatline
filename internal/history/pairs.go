package history

import (
	"encoding/json"
	"fmt"
	"strings"

	"flatline/internal/adapters"
)

// ToolPair is one call/result link read out of a native transcript. The refs
// are the source positions the parser records in every event's locator, so a
// pair read today lines up with events written by an earlier parser run
// without either of them being rewritten.
type ToolPair struct {
	ResultRef string
	CallRef   string
	ToolName  string
}

// PairFile re-reads one transcript for the sole purpose of linking tool
// results to the calls that produced them. It opens the file read-only, keeps
// nothing but the ids, and never writes anywhere.
//
// It exists because a harness can record the two sides under different ids:
// Codex names a function_call fc_… / ctc_… and its function_call_output
// call_… / fco_… / ctco_…, and an older parser stored only the first of those
// per event. The events are append-only, so the link is recovered here and
// recorded beside them.
func PairFile(path string, source adapters.Source) ([]ToolPair, error) {
	switch source {
	case adapters.SourceClaudeCode:
		return pairClaude(path)
	case adapters.SourceCodex:
		return pairCodex(path)
	default:
		// Every other reader writes call_id on both sides of a tool call, so
		// the session projection pairs them from the stored events and there is
		// nothing left for a re-read to recover. Reading the file as Codex
		// anyway would map the wrong ids onto real events.
		return nil, nil
	}
}

// pairKeyed accumulates the two halves and joins them on the id the harness
// uses for both.
type pairKeyed struct {
	callRefs  map[string]string
	callNames map[string]string
	results   []ToolPair
}

func newPairKeyed() *pairKeyed {
	return &pairKeyed{callRefs: make(map[string]string), callNames: make(map[string]string)}
}

func (p *pairKeyed) call(callID, ref, toolName string) {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return
	}
	if _, seen := p.callRefs[callID]; seen {
		return
	}
	p.callRefs[callID] = ref
	p.callNames[callID] = strings.TrimSpace(toolName)
}

func (p *pairKeyed) result(callID, ref string) {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return
	}
	p.results = append(p.results, ToolPair{ResultRef: ref, CallRef: callID})
}

func (p *pairKeyed) pairs() []ToolPair {
	out := make([]ToolPair, 0, len(p.results))
	for _, item := range p.results {
		callRef, ok := p.callRefs[item.CallRef]
		if !ok {
			continue
		}
		out = append(out, ToolPair{ResultRef: item.ResultRef, CallRef: callRef, ToolName: p.callNames[item.CallRef]})
	}
	return out
}

// codexPairRecord decodes only the fields the pairing needs. The recorded
// output and input are skipped rather than decoded, which is what keeps a pass
// over gigabytes of transcript affordable.
type codexPairRecord struct {
	Type    string `json:"type"`
	Payload struct {
		Type   string `json:"type"`
		ID     string `json:"id"`
		CallID string `json:"call_id"`
		Name   string `json:"name"`
	} `json:"payload"`
}

func pairCodex(path string) ([]ToolPair, error) {
	keyed := newPairKeyed()
	err := eachJSONLine(path, func(line int, raw []byte) error {
		var record codexPairRecord
		if json.Unmarshal(raw, &record) != nil {
			return nil
		}
		if record.Type != "response_item" && record.Type != "response_item_event" {
			return nil
		}
		// The same ref rule readCodex uses, so both produce the same locator.
		ref := firstNonEmpty(record.Payload.ID, record.Payload.CallID, fmt.Sprintf("line-%d", line))
		switch record.Payload.Type {
		case "custom_tool_call", "function_call":
			keyed.call(record.Payload.CallID, ref, record.Payload.Name)
		case "custom_tool_call_output", "function_call_output":
			keyed.result(record.Payload.CallID, ref)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("history: pair %s: %w", path, err)
	}
	return keyed.pairs(), nil
}

type claudePairRecord struct {
	UUID    string `json:"uuid"`
	Message struct {
		ID      string          `json:"id"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type claudePairBlock struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	ToolUseID string `json:"tool_use_id"`
}

func pairClaude(path string) ([]ToolPair, error) {
	keyed := newPairKeyed()
	err := eachJSONLine(path, func(line int, raw []byte) error {
		var record claudePairRecord
		if json.Unmarshal(raw, &record) != nil {
			return nil
		}
		if len(record.Message.Content) == 0 {
			return nil
		}
		blocks, err := claudeContentBlocks(record.Message.Content)
		if err != nil {
			return nil
		}
		// The same ref rule readClaude uses, so both produce the same locator.
		messageRef := firstNonEmpty(record.UUID, record.Message.ID, fmt.Sprintf("line-%d", line))
		for blockIndex, rawBlock := range blocks {
			var block claudePairBlock
			if json.Unmarshal(rawBlock, &block) != nil {
				return nil
			}
			ref := fmt.Sprintf("%s-%d", messageRef, blockIndex)
			switch block.Type {
			case "tool_use":
				keyed.call(block.ID, ref, block.Name)
			case "tool_result":
				keyed.result(block.ToolUseID, ref)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("history: pair %s: %w", path, err)
	}
	return keyed.pairs(), nil
}
