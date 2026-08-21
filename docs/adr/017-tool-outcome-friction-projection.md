# ADR-17: Tool outcome friction as an evidence projection

- 状态：accepted
- 日期：2026-08-21
- 决策者：Flatline maintainers

## 背景

Native Claude Code transcripts can mark a `tool_result` with `is_error`, and
Codex tool output can carry an explicit non-zero exit result. These facts are
currently bounded into transcript events, but the existing UI only searches
payload fields on the currently loaded event page. A failed tool result can
therefore be present in the source history while the session still displays
“未记录摩擦”.

Canonical transcript events are append-only. Updating old event payloads when
the parser learns a new source field would violate that boundary and would
make replay behavior dependent on mutable history.

## 决策

Record explicit tool outcome failures in a daemon-owned, append-only
`friction_records` projection keyed by `(session_id, source_event_id)`. The
projection stores only bounded evidence already present in the canonical
transcript event: failure kind, bounded output, explicit error flag or exit
code, locator, and timestamp. It never modifies or deletes the source history
or the canonical event row.

The projection is populated during the normal ingest replay, so restarting the
daemon can backfill unchanged native files after a parser upgrade. The API
returns a session-wide count and bounded list independently of the paged event
ledger. Missing outcome metadata remains “未记录”; ordinary text mentioning
the word “error” is not a failure by itself.

Tool execution failure and `asset_violation` remain separate evidence kinds.
Both may appear under the session friction view, but only explicit tool
outcomes create a `tool_error` projection record.

## 后果

- Existing transcript event counts and payloads remain append-only and stable.
- Session friction can be complete even when the visible event ledger is
  paged or lazily loaded.
- A bounded derived table and API query are required, plus replay tests for
  both Claude Code and Codex source shapes.
- The projection is evidence, not a causal diagnosis and does not alter asset
  health state by itself.
