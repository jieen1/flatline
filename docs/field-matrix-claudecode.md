# Claude Code Adapter Field Matrix

P2 synthetic-fixture contract. Coverage describes what the adapter may claim, not what every source session contains.

| Canonical field | Status | Mapping / evidence |
| --- | --- | --- |
| source | supported | Fixed adapter source `claude_code`. |
| source_session_id | supported | `session.id`; required for ingestion. |
| started_at / ended_at | supported | `session.started_at` / `session.ended_at`; omitted remains unknown. |
| harness_version | supported | `session.harness_version`; explicit source field. |
| model | supported | `session.model`; explicit source field. |
| cwd | supported | `session.cwd`; synthetic fixtures use a synthetic path. |
| asset invocation | supported | `messages[].asset_invocations[]`; maps to `invoked` + `asset_invoked`. |
| asset content hash | supported | Invocation `content_hash`; carried in canonical payload for P3. |
| reference inputs | supported | Invocation `references[]`; carried in canonical payload for later reference checks. |
| loaded / offered signal | unrecorded | Not claimed unless a future source format records it explicitly. |
| followed signal | unsupported | Requires a later behavior detector; never inferred by this adapter. |
| observation level | supported | `invoked` for explicit invocation; `unknown` when participation evidence is omitted. |
| locator | supported | Source, qualified session, message id/index, and stable raw reference. |
| raw source bytes | unsupported | The adapter receives raw input but does not persist user content in this matrix. |

Missing fields are represented as nil/unknown in canonical output; they are never converted to zero or a positive participation count.
