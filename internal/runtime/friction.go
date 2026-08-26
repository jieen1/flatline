package runtime

import (
	"context"
	"log"
)

// ReclassifyFriction recomputes every friction row whose classifier version is
// not the current one. Friction categories are a derived projection, so a rule
// change has to be applied to the whole table, not only to sessions ingested
// after the change (ADR-10). Call it once on daemon start, before the first
// refresh.
func (a *App) ReclassifyFriction(ctx context.Context) (int, error) {
	recomputed, err := a.events.ReclassifyFriction(ctx)
	if err != nil {
		return 0, err
	}
	if recomputed > 0 {
		log.Printf("friction: reclassified %d records", recomputed)
	}
	return recomputed, nil
}

// DeriveMissingFriction records friction for tool results whose outcome the
// harness only printed into the output text. Transcript files whose
// fingerprint has not changed are never re-read, so this startup pass is the
// only way those already-stored events reach the newer rules. It is
// idempotent; a second run finds nothing left to record.
func (a *App) DeriveMissingFriction(ctx context.Context) (int, error) {
	derived, err := a.events.DeriveMissingFriction(ctx)
	if err != nil {
		return 0, err
	}
	if derived > 0 {
		log.Printf("friction: derived %d records from recorded tool output", derived)
	}
	return derived, nil
}
