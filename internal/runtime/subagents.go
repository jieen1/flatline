package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"flatline/internal/eventstore"
	"flatline/internal/history"
	"flatline/internal/ingest"
)

// A Claude Code subagent writes its own transcript, but every record in it
// carries the parent's sessionId, so an earlier reader filed the subagent's
// work under the parent. A Codex subagent is its own session. That made "how
// many tool calls did this session run" mean two different things depending on
// which harness wrote it.
//
// The reader now treats a subagent transcript as its own thread. For a history
// that was already imported, the stored events still name the parent, so the
// re-read pass corrects the filing before it replays the file — see
// eventstore.RelocateEvents for what exactly is changed and what is not.

// relocateIfMisfiled moves the already-stored events of one transcript onto the
// session the current reader says produced it, and reports the session they
// came from so the caller can rebuild its projections.
func (a *App) relocateIfMisfiled(ctx context.Context, storedSessionID string, input ingest.SessionInput) (string, eventstore.RelocateReport, error) {
	var report eventstore.RelocateReport
	parsedSessionID := string(input.Raw.Source) + ":" + input.Raw.SessionID
	if storedSessionID == "" || storedSessionID == parsedSessionID {
		return "", report, nil
	}
	meta, events, err := a.pipeline.Parse(input.Raw)
	if err != nil {
		return "", report, fmt.Errorf("runtime: parse for relocation %s: %w", input.Raw.SourcePath, err)
	}
	// The session row has to exist before an event can point at it.
	if _, err := a.events.IngestSession(ctx, input.Raw.Source, meta); err != nil {
		return "", report, err
	}
	newIDs := make(map[string]string, len(events))
	for _, event := range events {
		if event.Locator.RawRef != "" && event.SourceEventID != "" {
			newIDs[event.Locator.RawRef] = event.SourceEventID
		}
	}
	report, err = a.events.RelocateEvents(ctx, storedSessionID, parsedSessionID, newIDs)
	if err != nil {
		return "", report, err
	}
	if err := a.events.ReattachNativeFile(ctx, input.Raw.SourcePath, parsedSessionID); err != nil {
		return "", report, err
	}
	return storedSessionID, report, nil
}

// refreshRelocatedParents rebuilds everything the correction invalidated on the
// sessions the events were moved off: their counts, their command/file/tool
// projections, and the friction rows those projections feed.
func (a *App) refreshRelocatedParents(ctx context.Context, parents map[string]struct{}) error {
	for sessionID := range parents {
		if err := a.events.RecomputeSessionProjections(ctx, sessionID); err != nil {
			return err
		}
		if err := a.events.RecomputeSessionStats(ctx, sessionID); err != nil {
			return err
		}
		if err := a.events.RollUpSessionUsage(ctx, sessionID, history.ParserVersion); err != nil {
			return err
		}
	}
	return nil
}

// BackfillSubagentIdentity fills in what a Claude Code subagent thread was
// launched for. The parent's Agent call names the role and the description;
// its own result names the agent id. Both are already stored, so the link is
// the harness's own record rather than an inference. A subagent whose launch
// call is not in the store keeps both fields NULL.
func (a *App) BackfillSubagentIdentity(ctx context.Context) (int, error) {
	if a == nil || a.events == nil {
		return 0, fmt.Errorf("runtime: event store is not wired")
	}
	pending, err := a.events.SubagentsMissingIdentity(ctx)
	if err != nil {
		return 0, err
	}
	filled := 0
	for _, item := range pending {
		if err := ctx.Err(); err != nil {
			return filled, err
		}
		parent := item.Source
		agentID := item.Path
		input, ok, err := a.events.LaunchInput(ctx, parent, agentID)
		if err != nil {
			return filled, err
		}
		if !ok {
			continue
		}
		role, nickname := launchIdentity(input)
		if role == "" && nickname == "" {
			continue
		}
		if err := a.events.SetSubagentIdentity(ctx, item.SessionID, role, nickname); err != nil {
			return filled, err
		}
		filled++
	}
	if filled > 0 {
		log.Printf("subagents: filled role/nickname from the parent launch call sessions=%d", filled)
	}
	return filled, nil
}

// launchIdentity reads the two fields the Agent tool names: which kind of
// agent was asked for, and what it was asked to do.
func launchIdentity(toolInput string) (string, string) {
	var decoded struct {
		SubagentType string `json:"subagent_type"`
		Description  string `json:"description"`
	}
	if json.Unmarshal([]byte(toolInput), &decoded) != nil {
		return "", ""
	}
	return strings.TrimSpace(decoded.SubagentType), strings.TrimSpace(decoded.Description)
}
