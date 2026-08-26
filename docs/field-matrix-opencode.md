# opencode Adapter Field Matrix

What the opencode reader (`internal/history/opencode.go`) takes from
`~/.local/share/opencode/opencode.db`, and what it refuses to invent. Measured
against the local database on 2026-08-23: 51 sessions, 13,182 messages,
53,455 parts.

The database is opened `mode=ro` with a busy timeout and is never written.

## Session row → session metadata

| Canonical field | Status | Source column / rule |
| --- | --- | --- |
| source | supported | Fixed `opencode`. |
| source_session_id | supported | `session.id` (`ses_…`). |
| started_at / ended_at | supported | `session.time_created` / `session.time_updated`, epoch ms → UTC. |
| harness_version | supported | `session.version` (e.g. `1.18.18`). |
| model | supported | `session.model` JSON → `.id`. `.providerID` is not stored. |
| cwd | supported | `session.directory`. |
| title | supported | `session.title`, **except** the placeholder `New session - <timestamp>`, which is not evidence of what the session was about; the reader falls back to the first meaningful user text, and to nothing if there is none. |
| task_text | supported | First user `text` part that is not generated noise, bounded to 240 runes. |
| parent_session_id | supported | `session.parent_id` → `opencode:<parent_id>`. 18 of 51 local sessions have one. |
| thread_kind | supported | `subagent` when `parent_id` is set, otherwise `main`. |
| agent_role | supported | `session.agent` (`build`, `explore`, `general`, `opencode-loop-local`). |
| agent_nickname | unsupported | opencode records no per-agent nickname. |
| originator | supported | Fixed `opencode`. |

## Parts → transcript records

| Part type | Becomes | Notes |
| --- | --- | --- |
| `text` | `transcript_message` | Role from the owning `message.data.role`. Generated noise is dropped. |
| `tool` | `transcript_tool_call` **and** `transcript_tool_result` | One part holds the call and its outcome, so it becomes two records sharing one `call_id`. |
| `reasoning` | dropped | Model thinking, not transcript text. |
| `step-start` / `step-finish` | dropped | Per-step token counts; the session row already holds the totals. |
| `patch` / `compaction` | dropped | No canonical event type covers them yet. |

| Canonical field | Status | Source rule |
| --- | --- | --- |
| call_id | supported | `part.data.callID`. 13,237 / 13,237 tool calls and 13,236 / 13,236 results carry one, which is what §13's pairing joins on. |
| tool_name | supported | `part.data.tool` (`bash`, `read`, `edit`, `grep`, `write`, …; lower-case, unlike Claude Code). |
| tool_input | supported | `state.input`, JSON-encoded and bounded to 8192 runes. |
| tool_output | supported | `state.output`, or `state.error` when the status is `error`. |
| is_error | supported | `state.status`: `error` → true, `completed` → false. A `running` tool gets **no result record at all** — an unfinished tool is unrecorded, not a success. Local: 42 error, 1 running, 13,194 completed. |
| exit_code | supported (partial) | `state.metadata.exit`, which only the `bash` tool records. 9,619 of 13,236 results carry one; the rest are NULL. |
| abort_reason | supported | `state.metadata.interrupted` → `interrupted`. |
| truncated | supported | Set when the bounded payload was shorter than the source value. |
| sidechain / agent_id | unsupported | opencode has no in-session sidechain marker. |

## Usage (§14/§15)

Read from the session row, not re-derived from parts (§19.5).
`usage_source = 'opencode_session'`; 37 of 51 local sessions have one.

| session_usage column | Source column | Unrecorded rule |
| --- | --- | --- |
| input_tokens | `tokens_input` | 0 → NULL: the column defaults to 0, so a 0 cannot be told apart from "never ran a turn". |
| cached_input_tokens | `tokens_cache_read` | same |
| cache_write_tokens | `tokens_cache_write` | same |
| output_tokens | `tokens_output` | same |
| reasoning_tokens | `tokens_reasoning` | same |
| total_tokens | sum of the five above | NULL when the sum is 0 |
| assistant_turns / user_turns | counted from `message.data.role` | always recorded |
| lines_added / lines_removed / files_changed | `summary_additions` / `summary_deletions` / `summary_files` | NULL stays NULL (opencode has not summarized the diff yet); a real 0 is kept as 0 |
| context_window | — | **unrecorded**: opencode stores no context window |
| active_ms | — | unrecorded by this reader; it is a derived measure, not a source field |
| by_model | `session.model.id` | One row: opencode pins one model per session |
| cost | `session.cost` | `session_usage` has no cost column, so it rides in the `session_started` event payload instead of being dropped |

## Fingerprint

Pseudo-path `<db path>#<session_id>` in `native_files`, with `mtime_ns` set from
`session.time_updated`. A row whose `time_updated` is unchanged is skipped
without being re-read. Measured: 51 pseudo-paths recorded; a second pass read 0
sessions and skipped 65.

## Not recorded by opencode at all

`agent_nickname`, `context_window`, per-turn model attribution, sidechain
markers, and an exit status for any tool other than `bash`. These stay NULL;
none of them is filled with a zero.
