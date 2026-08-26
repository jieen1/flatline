# dsh (DeepSeek Harness) Adapter Field Matrix

What the dsh reader (`internal/history/dsh.go`) takes from
`~/.dsh/sessions/<project-slug>/session-<uuid>/session.jsonl.zstd`, and what it
refuses to invent. Measured against the local tree on 2026-08-23: 14 sessions,
9,826 records.

The file is zstd-compressed JSONL, decoded in pure Go
(`github.com/klauspost/compress/zstd`, ADR-19). Files are opened read-only.

## Record shape

Every line is one record: `{"type": …, "seq"|"seq0": …, "time"|"time0": …, "data": {…}}`.
The first line is the session header and carries its fields at the top level
instead of under `data`.

Local record-type counts: `reasoning-chunks` 4,469, `assistant/chunk` 2,141,
`tool-call-chunks` 1,412, `text-chunks` 367, `tool/call` 250, `tool/result` 250,
`step/start` 225, `step/end` 225, `assistant/message` 217, `user/message` 47,
`turn/start` 16, `turn/end` 16, `session` 14, `session/title` 14,
`request/context` 13, `request/header` 13.

The `*-chunks` and `assistant/chunk` records are streaming deltas of records
that also arrive whole; only the whole records are read, so nothing is counted
twice.

## Session header → session metadata

| Canonical field | Status | Source rule |
| --- | --- | --- |
| source | supported | Fixed `dsh`. |
| source_session_id | supported | Header `id` (`session-<uuid>`); falls back to the directory name. |
| started_at / ended_at | supported | Header `createdAt` and the last record's `time`, epoch ms → UTC. |
| harness_version | **unsupported** | The header's `version` is the record-schema version (`0`), not a harness version. Carrying it would be a misleading "0", so this stays unrecorded. |
| model | supported | `request/context.data.model` (first recorded). |
| cwd | supported | Header `cwd`. |
| title | supported | `session/title.data.title`. `source.kind` may be `fallback`, meaning dsh derived it from the first message — still source-recorded text. |
| task_text | supported | First meaningful `user/message` text, bounded to 240 runes. |
| thread_kind | supported (partial) | Header `delegationDepth`: 0 → `main`, >0 → `subagent`. All 14 local sessions are 0. |
| parent_session_id | **unrecorded** | `delegationDepth` says a session was delegated but never names the delegator. The parent is left empty rather than guessed from the slug or the id. |
| agent_role | supported | Header `agentPreset` (`standard`, `code`, …). |
| agent_nickname | unsupported | dsh records none. |
| originator | supported | Fixed `dsh`. |

## Records → transcript records

| Record type | Becomes | Notes |
| --- | --- | --- |
| `user/message` | `transcript_message` (role user) | Only `content[].type == "text"` blocks. |
| `assistant/message` | `transcript_message` (role assistant) | Only `text` blocks. `reasoning` is model thinking; `tool-call` blocks are dropped because the separate `tool/call` record already carries them — counting both would double every tool call. |
| `tool/call` | `transcript_tool_call` | |
| `tool/result` | `transcript_tool_result` | |
| `turn/end` | `transcript_message` (role system) with `abort_reason` | Only when `reason.kind` is not `completed`. Local: completed 8, aborted 5, error 3. A completed turn writes no record, and that absence is never read as a failure. |
| `step/start`, `step/end`, `request/header`, `llm/retry`, `todo/write`, `permission/preset`, `sandbox/mode`, `approval/policy`, `agent-preset/selected`, `agent/inbox/spliced`, `session/end-seed` | dropped | No canonical event type covers them yet. |

| Canonical field | Status | Source rule |
| --- | --- | --- |
| call_id | supported | `tool/call.data.callId` and `tool/result.data.message.source.callId` (falling back to the block's `toolCallId`). 250 / 250 calls and 250 / 250 results carry one. |
| tool_name | supported | `tool/call.data.name` (`bash` 137, `read` 58, `edit` 33, `grep` 6, `todo_write` 4, `job_output` 4, `run_code` 4, `write` 3, `web_search` 1). |
| tool_input | supported | `tool/call.data.arguments`, already a JSON string, bounded to 8192 runes. |
| tool_output | supported | The `text` blocks nested under `content[].content[]`. |
| is_error | supported | `content[].isError`, plus `data.error` when present (local: one `{"name":"FsError","code":"FS_STALE_VERSION"}`). |
| exit_code | **unrecorded** | dsh records no process exit status anywhere. It is only filled when a tool printed a status line into its own output, via the shared `NormalizeToolFailure` text rules. Local: 0 of 250 results. |
| truncated | supported | Set when the bounded payload was shorter than the source value. |
| sidechain / agent_id | unsupported | dsh has no in-session sidechain marker. |

## Usage (§14/§15)

Summed from `assistant/message.data.usage` across the session, because dsh
records usage per assistant message and keeps no session total.
`usage_source = 'dsh_message_usage'`; 2 of 14 local sessions have one (the
other 12 never completed a model turn).

| session_usage column | Source field | Unrecorded rule |
| --- | --- | --- |
| input_tokens | Σ `usage.inputTokens` | 0 → NULL |
| output_tokens | Σ `usage.outputTokens` | 0 → NULL |
| cached_input_tokens | Σ `usage.cacheReadTokens` | 0 → NULL |
| total_tokens | sum of the three above | NULL when 0 |
| cache_write_tokens | — | **unrecorded**: dsh emits no cache-write count |
| reasoning_tokens | — | **unrecorded**: dsh emits no reasoning-token count even though it streams reasoning text |
| assistant_turns / user_turns | counted from the message records | always recorded |
| context_window | `request/context.data.contextWindow` | 11 of 14 sessions recorded |
| lines_added / lines_removed / files_changed | — | **unrecorded**: dsh keeps no diff summary |
| cost | — | **unrecorded** |
| by_model | `request/context.data.model` | One row per session |

## Fingerprint

The ordinary `native_files` size/mtime fingerprint on the real file path: dsh
writes one file per session, so the file is the unit of change. Measured: a
second pass read 0 sessions and skipped all 14.

## Not recorded by dsh at all

A harness version, a parent session id for a delegated session, a tool exit
status, cache-write and reasoning token counts, diff line counts, and cost.
All stay NULL.
