package runtime

import (
	"context"
	"fmt"
	"log"
	"os"

	"flatline/internal/adapters"
	"flatline/internal/eventstore"
	"flatline/internal/history"
)

// PairingReport is what one pairing pass observed. Every field is a count of
// something the pass actually did.
type PairingReport struct {
	Reparse      ReparseReport
	Projected    int
	Candidates   int
	FilesRead    int
	FilesMissing int
	PairsWritten int
	Reprojected  int
}

// BackfillEventPairs is the daemon's catch-up pass over an already-imported
// local history. It runs behind the listener, before the first refresh.
//
// Step 0 is the versioned re-read: every transcript the current parser has not
// read yet is read once more and replayed through the normal ingest path, so a
// record the older parser skipped is inserted now. Ingest is idempotent, so
// nothing already stored is rewritten. It also writes the measurement
// projection, which can only be taken from the full source text.
//
// The remaining three steps link every tool result to the call that produced
// it, cheapest first.
//
//  1. Project every session that has no tool projection yet. That pass already
//     decodes each tool payload, so it records the pairs the stored ids
//     establish on the way through, along with the command, file and tool
//     projections that read them.
//  2. For the transcripts that still hold an unpaired result — the Codex
//     histories whose function_call and function_call_output carry different
//     ids — read the source file once more, read-only, and map the pairs it
//     names back onto the stored events through the source position both
//     already carry. No event is written, and no source file is touched.
//  3. Project those sessions again, so their commands and tool counts pick up
//     the outcomes the recovered pairs just made reachable.
//
// A transcript that is no longer on disk keeps its session unpaired; that is a
// missing record, not an error, and the pass moves on.
func (a *App) BackfillEventPairs(ctx context.Context) (PairingReport, error) {
	if a == nil || a.events == nil {
		return PairingReport{}, fmt.Errorf("runtime: event store is not wired")
	}
	var report PairingReport
	reparsed, err := a.ReparseStaleTranscripts(ctx)
	report.Reparse = reparsed
	a.EndReparse()
	if err != nil {
		return report, err
	}
	if _, err := a.BackfillSubagentIdentity(ctx); err != nil {
		return report, err
	}
	gap, err := a.events.ToolStatsGap(ctx)
	if err != nil {
		return report, err
	}
	if gap > 0 {
		a.SetPairingProgress("projecting", 0, 0, 0)
		projected, err := a.events.RecomputeAllProjections(ctx)
		if err != nil {
			return report, err
		}
		report.Projected = projected
	}

	candidates, err := a.events.SessionsMissingPairs(ctx)
	if err != nil {
		return report, err
	}
	report.Candidates = len(candidates)
	a.SetPairingProgress("reading", report.Candidates, 0, 0)
	repaired := make(map[string]struct{})
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		written, err := a.pairOneTranscript(ctx, candidate)
		if err != nil {
			return report, err
		}
		if written < 0 {
			report.FilesMissing++
		} else {
			report.FilesRead++
			report.PairsWritten += written
			if written > 0 {
				repaired[candidate.SessionID] = struct{}{}
			}
		}
		a.SetPairingProgress("reading", report.Candidates, report.FilesRead+report.FilesMissing, report.PairsWritten)
	}

	if len(repaired) > 0 {
		a.SetPairingProgress("projecting", report.Candidates, report.FilesRead+report.FilesMissing, report.PairsWritten)
		for sessionID := range repaired {
			if err := a.events.RecomputeSessionProjections(ctx, sessionID); err != nil {
				return report, err
			}
			report.Reprojected++
		}
	}
	return report, nil
}

// pairOneTranscript re-reads one file and records what it names. It returns -1
// when the file could not be read at all, which is a missing record rather
// than a failure: that session simply stays unpaired.
func (a *App) pairOneTranscript(ctx context.Context, candidate eventstore.PairCandidate) (int, error) {
	if _, err := os.Stat(candidate.Path); err != nil {
		return -1, nil
	}
	pairs, err := history.PairFile(candidate.Path, adapters.Source(candidate.Source))
	if err != nil {
		// One unreadable transcript must not stop the pass; it stays unpaired
		// and says so by keeping no pairing version.
		log.Printf("pairing: %v", err)
		return -1, nil
	}
	written, err := a.events.RecordReparsePairs(ctx, candidate.SessionID, pairRefs(pairs))
	if err != nil {
		return 0, err
	}
	if err := a.events.StampPairingVersion(ctx, candidate.Path); err != nil {
		return 0, err
	}
	return written, nil
}

func pairRefs(pairs []history.ToolPair) []eventstore.ToolPairRef {
	out := make([]eventstore.ToolPairRef, 0, len(pairs))
	for _, pair := range pairs {
		out = append(out, eventstore.ToolPairRef{ResultRef: pair.ResultRef, CallRef: pair.CallRef, ToolName: pair.ToolName})
	}
	return out
}
