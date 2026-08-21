// Package baseline implements the P3 Effective Bundle Resolver and the
// explainable baseline query.
//
// Design basis: system design v0.4 §3.1 (Baseline), §6 (Effective Bundle
// Resolver), §7.1 (EffectiveBundle object), roadmap P3, and ADR-4 (预期来自
// 资产自身历史) / ADR-10 (状态历史可廉价重放).
//
// Two responsibilities:
//
//  1. Effective Bundle Resolver — deterministically resolve, for a session,
//     the asset version vector that was in force at the session's start time,
//     and persist it to effective_bundles so any session can answer "which
//     version was effective then".
//
//  2. Baseline query — compute the explainable rolling-window participation
//     rate and absolute counts for an asset from opportunities and
//     participations, with the numerator, denominator, window, and rule
//     versions all visible.
//
// Evidence discipline (AGENTS.md §2):
//   - No causal claims: the resolver and baseline only state alignment (which
//     version was in force, how many sessions participated), never "caused".
//   - No opaque scores: every ratio is drillable to its numerator and
//     denominator; there is no aggregate quality score.
//   - 缺失 ≠ 零: a session with no recorded start time is not anchored to an
//     invented time; a window with no opportunities yields a nil rate, never
//     0.
//
// Replayability (ADR-10): both the bundle vector and the baseline are pure
// functions of the canonical rows plus the window and rule versions, so the
// derived layer can be recomputed after a resolver or rule upgrade.
package baseline

import "time"

// Replay metadata (ADR-10): carried by every derived row / computation this
// package produces so the derived layer is recomputable.
const (
	// ResolverVersion is written to effective_bundles.resolver_version.
	ResolverVersion = "resolver/1"
	// BaselineVersion labels every baseline computation returned by Query.
	BaselineVersion = "baseline/1"
)

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
