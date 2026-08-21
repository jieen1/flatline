// Package tracking implements the P3 Opportunity & Participation Tracker
// (system design v0.4 §3.1, §6, §7; roadmap P3).
//
// It owns three concerns:
//
//   - deterministic task-shape classification (what counts as a "same-kind
//     task"), with a versioned rule so classification results can be drilled
//     down to their basis (roadmap P3 acceptance: 形状归类规则文档化);
//   - idempotent recording of opportunities and participations into the
//     existing opportunities / participations tables;
//   - explainable baseline queries: every rate is returned together with its
//     numerator, denominator, window and rule version (ADR-4, ADR-8).
//
// Evidence discipline (AGENTS.md §2):
//   - participation_signal (what happened) is orthogonal to observation_level
//     (how we know it); both are closed canonical enums and non-canonical
//     values are rejected;
//   - unknown is never treated as zero: a window with no opportunities yields
//     a nil rate ("no baseline"), not 0;
//   - all writes are idempotent (derived layer, ADR-10: the tracker output can
//     be recomputed from canonical events + asset versions).
package tracking
