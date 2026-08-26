package runtime

import (
	"context"
	"fmt"
	"log"
	"sort"

	"flatline/internal/adapters"
	"flatline/internal/eventstore"
	"flatline/internal/history"
)

// ReparseReport is what one versioned re-read observed. Every field is a count
// of something the pass actually did.
type ReparseReport struct {
	Files          int
	FilesRead      int
	FilesMissing   int
	FilesSkipped   int
	EventsInserted int
	// EventsRelocated counts the stored events refiled onto the session that
	// actually produced them; SessionsRefiled counts the sessions they came off.
	EventsRelocated int
	SessionsRefiled int
	// EvidenceSuperseded counts the asset_invoked events this parser no longer
	// produces from the same source text, with the participations and
	// opportunities that followed them; EvidenceRestored counts the ones an
	// earlier pass had superseded and this one produced again.
	EvidenceSuperseded      int
	EvidenceParticipations  int
	EvidenceOpportunities   int
	EvidenceRestored        int
	EvidenceSessionsChecked int
	// StaleOpportunities counts the opportunity rows this parser no longer
	// produces at all — the ones whose only basis was a path reference in the
	// task text, which writes no event for the evidence channel to follow.
	StaleOpportunities int
	// HookFrictionLinks counts the hook blocks this pass matched to a
	// registered hook asset.
	HookFrictionLinks int
	Warnings          int
}

// reparseProgressEvery bounds how often the pass republishes its counters;
// every file would mean a lock per file for no extra information.
const reparseProgressEvery = 10

// ReparseStaleTranscripts reads every local transcript the current parser has
// not read yet and replays it through the normal ingest path.
//
// The refresh pass skips a file whose size and mtime have not changed, so a
// parser that starts reading a record the previous one ignored — a Codex
// turn_aborted, a token_count — would never see it on an already-imported
// history. This pass is the one read that fixes that.
//
// It is safe to run on the whole local history because ingest is idempotent:
// events are keyed by source event id, so replaying a transcript inserts only
// the records the older parser missed and rewrites none of the stored ones
// (ADR-17). A file that no longer exists, or whose source is not a plain
// transcript this package can re-read, is left unstamped and skipped.
func (a *App) ReparseStaleTranscripts(ctx context.Context) (ReparseReport, error) {
	var report ReparseReport
	if a == nil || a.events == nil || a.pipeline == nil || a.registry == nil {
		return report, fmt.Errorf("runtime: reparse is not wired")
	}
	candidates, err := a.events.TranscriptsWithStaleParser(ctx, history.ParserVersion)
	if err != nil {
		return report, err
	}
	report.Files = len(candidates)
	if len(candidates) == 0 {
		return report, nil
	}
	assetList, err := a.registry.List(ctx)
	if err != nil {
		return report, fmt.Errorf("runtime: list assets for reparse: %w", err)
	}
	config := history.Config{Assets: assetList}
	a.SetReparseProgress(report.Files, 0, 0)
	// A transcript whose thread identity changed is corrected before it is
	// replayed, so the same records are not stored twice under two sessions.
	relocatedParents := make(map[string]struct{})
	// What this parser produced for each session's asset evidence, and how
	// many of that session's transcripts this pass actually read. A session is
	// only reconciled when all of them were read: superseding on a partial
	// reading would withdraw evidence that another transcript still produces.
	produced := make(map[string]map[string]struct{})
	// The opportunity rows this parser produces for each session, which is a
	// different question from the events: an opportunity can come from a path
	// reference in the task text, and that writes no event.
	producedOpportunities := make(map[string][]eventstore.OpportunityKey)
	filesReadPerSession := make(map[string]int)
	for index, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if !history.ReparsableSource(adapters.Source(candidate.Source)) {
			report.FilesSkipped++
			continue
		}
		session, ok, warning := history.ReadFile(candidate.Path, adapters.Source(candidate.Source), config)
		if warning != "" {
			report.Warnings++
			log.Printf("reparse: %s", warning)
		}
		if !ok {
			report.FilesMissing++
			continue
		}
		parent, moved, err := a.relocateIfMisfiled(ctx, candidate.SessionID, session.Input)
		if err != nil {
			return report, err
		}
		if parent != "" {
			relocatedParents[parent] = struct{}{}
			report.EventsRelocated += moved.Events
		}
		result, err := a.pipeline.Ingest(ctx, session.Input)
		if err != nil {
			// One transcript that cannot be replayed must not stop the pass. It
			// keeps no parser stamp, so the next start tries it again.
			report.Warnings++
			log.Printf("reparse: ingest %s: %v", candidate.Path, err)
			continue
		}
		report.FilesRead++
		report.EventsInserted += result.EventsInserted
		if result.SessionID != "" {
			live, ok := produced[result.SessionID]
			if !ok {
				live = make(map[string]struct{})
				produced[result.SessionID] = live
			}
			for _, id := range result.AssetEventIDs {
				live[id] = struct{}{}
			}
			// Every transcript of a session produces the same opportunity set —
			// it is recorded per session, not per file — so the last read wins
			// rather than accumulating.
			producedOpportunities[result.SessionID] = result.Opportunities
			report.HookFrictionLinks += result.HookFrictionLinks
			filesReadPerSession[result.SessionID]++
		}
		if err := a.events.StampParserVersion(ctx, candidate.Path, history.ParserVersion); err != nil {
			return report, err
		}
		if index%reparseProgressEvery == 0 {
			a.SetReparseProgress(report.Files, report.FilesRead+report.FilesMissing+report.FilesSkipped, report.EventsInserted)
		}
	}
	a.SetReparseProgress(report.Files, report.FilesRead+report.FilesMissing+report.FilesSkipped, report.EventsInserted)
	if err := a.refreshRelocatedParents(ctx, relocatedParents); err != nil {
		return report, err
	}
	report.SessionsRefiled = len(relocatedParents)
	if err := a.supersedeStaleAssetEvidence(ctx, produced, producedOpportunities, filesReadPerSession, &report); err != nil {
		return report, err
	}
	logInjectedSkips("reparse")
	log.Printf("reparse %s: files=%d read=%d missing=%d skipped=%d events_inserted=%d events_relocated=%d sessions_refiled=%d evidence_checked=%d evidence_superseded=%d evidence_restored=%d participations_superseded=%d opportunities_superseded=%d stale_opportunities=%d hook_friction_links=%d warnings=%d",
		history.ParserVersion, report.Files, report.FilesRead, report.FilesMissing, report.FilesSkipped,
		report.EventsInserted, report.EventsRelocated, report.SessionsRefiled,
		report.EvidenceSessionsChecked, report.EvidenceSuperseded, report.EvidenceRestored,
		report.EvidenceParticipations, report.EvidenceOpportunities,
		report.StaleOpportunities, report.HookFrictionLinks, report.Warnings)
	return report, nil
}

// supersedeStaleAssetEvidence reconciles the stored asset evidence of every
// session this pass read in full. A session with a transcript this pass did not
// read is skipped: the unread file may still produce the evidence that is
// missing from what was read, and withdrawing it would be a guess.
func (a *App) supersedeStaleAssetEvidence(ctx context.Context, produced map[string]map[string]struct{}, opportunities map[string][]eventstore.OpportunityKey, filesRead map[string]int, report *ReparseReport) error {
	if len(produced) == 0 {
		return nil
	}
	sessions := make([]string, 0, len(produced))
	for sessionID := range produced {
		sessions = append(sessions, sessionID)
	}
	sort.Strings(sessions)
	for _, sessionID := range sessions {
		total, err := a.events.TranscriptCountForSession(ctx, sessionID)
		if err != nil {
			return err
		}
		if total == 0 || filesRead[sessionID] < total {
			continue
		}
		result, err := a.events.SupersedeAssetEvidence(ctx, sessionID, produced[sessionID])
		if err != nil {
			return err
		}
		report.EvidenceSessionsChecked++
		report.EvidenceSuperseded += result.Events
		report.EvidenceRestored += result.Restored
		report.EvidenceParticipations += result.Participations
		report.EvidenceOpportunities += result.Opportunities
		stale, err := a.events.SupersedeStaleOpportunities(ctx, sessionID, opportunities[sessionID])
		if err != nil {
			return err
		}
		report.StaleOpportunities += stale
	}
	return nil
}

// MeasureMissingUsage fills the measurement projection for transcripts that
// were read by this parser but hold no usage row — a database migrated after
// the session was already ingested. It reads each transcript once, read-only,
// and writes only the derived measurement.
func (a *App) MeasureMissingUsage(ctx context.Context) (int, error) {
	if a == nil || a.events == nil || a.registry == nil {
		return 0, fmt.Errorf("runtime: usage measurement is not wired")
	}
	candidates, err := a.events.TranscriptsMissingUsage(ctx, history.ParserVersion)
	if err != nil {
		return 0, err
	}
	if len(candidates) == 0 {
		return 0, nil
	}
	assetList, err := a.registry.List(ctx)
	if err != nil {
		return 0, fmt.Errorf("runtime: list assets for usage measurement: %w", err)
	}
	config := history.Config{Assets: assetList}
	measured := 0
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return measured, err
		}
		if !history.ReparsableSource(adapters.Source(candidate.Source)) {
			continue
		}
		session, ok, _ := history.ReadFile(candidate.Path, adapters.Source(candidate.Source), config)
		if !ok || session.Input.Usage == nil {
			continue
		}
		if err := a.events.RecordFileUsage(ctx, candidate.Path, candidate.SessionID,
			session.Input.Usage, history.ParserVersion); err != nil {
			return measured, err
		}
		measured++
	}
	return measured, nil
}
