# ADR-19: Adapters may depend on pure-Go format decoders

- Status: Accepted
- Date: 2026-08-23
- Supersedes: none
- Related: ADR-1 (local-first), ADR-2 (single daemon, pure-Go SQLite), ADR-11
  (canonical event model), ADR-15 (native session transcript evidence)

## Context

Session sources do not all store transcripts as plain JSONL. dsh writes
`~/.dsh/sessions/<project>/session-<uuid>/session.jsonl.zstd`: JSONL compressed
with zstd. Flatline cannot read that evidence without a zstd decoder, and there
is no zstd in the Go standard library. Shelling out to a `zstd` binary is not an
option either: it is not installed on the reference machine, and a subprocess
per session file would make the read path depend on the user's PATH.

Flatline's two standing constraints are that the daemon builds without CGO
(ADR-2) and that nothing it links may talk to the network (ADR-1).

## Decision

An adapter may take a dependency on a third-party **format decoder** when all of
the following hold:

1. It is **pure Go** — no cgo, so `CGO_ENABLED=0` still builds the daemon.
2. It performs **no network I/O and no telemetry**. The dependency decodes
   bytes it is handed; it does not open sockets, read credentials, or report
   usage.
3. It decodes a **container or compression format**, not a vendor protocol. It
   exists so a local file can be read at all.
4. It is used **only on the read path** of a source adapter. Flatline never
   compresses, writes, or rewrites a source file (AGENTS.md §2/§3).

The first dependency admitted under this rule is
`github.com/klauspost/compress/zstd`, pinned at v1.17.11, for the dsh reader.
v1.17.11 is the last release whose module still declares `go 1.22`; later
releases would force the repository's own `go` directive up.

What this rule does **not** admit: harness SDKs, API clients, telemetry or
crash-reporting libraries, or anything that needs cgo. A source whose history
is only reachable through a network API is out of scope for an adapter — it is
not local evidence.

## Consequences

- dsh sessions become readable, and any future source using a pure-Go-decodable
  container (gzip, zip, brotli) can follow the same path without a new ADR.
- `go.mod` grows by one direct dependency. `go build` stays CGO-free; the
  no-CGO check in DEVELOPMENT.md is unchanged.
- The privacy posture is unchanged: the decoder is handed bytes already read
  from a local file under a configured root, and its output never leaves the
  local daemon.
- Reviewers get one question to ask of any new dependency: *does it decode a
  local byte format, or does it speak to something?* Only the first is allowed.
