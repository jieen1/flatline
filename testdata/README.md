# P2 Synthetic Fixtures

These fixtures are fabricated inputs for the Claude Code and Codex source adapters. They are not exported sessions and contain no real paths, prompts, identifiers, or user content.

## Layout

```text
<source>/<scenario>.json
```

Each source has three required scenarios:

- `normal`: recorded session metadata and one explicit asset invocation;
- `version_change`: metadata that differs from the preceding synthetic session and can produce an `EnvironmentChanged` alignment anchor;
- `missing_fields`: fields intentionally omitted because the source did not record them. Omission must remain `unknown`/nil and must never become zero or an invented value.

## Envelope

Every fixture has a `_meta` object with:

- `source`;
- `scenario`;
- `description`;
- `synthetic: true`;
- `expected` — the important canonical mapping assertions.

The adapter tests compare normalized canonical output. Generated database IDs and ingestion timestamps are not fixture facts and are excluded from golden comparisons. Stable source event IDs, event types, observation levels, participation signals, timestamps, payload values, and locator references are compared.

## Evidence rules

- `invoked` is used only where the source explicitly records an asset invocation.
- `unknown` means the source omitted a participation field; it is not a zero count.
- `inferred` is reserved for deterministic derivations such as environment-change comparison.
- `followed` is a participation signal and is never an observation level.
- All paths, hashes, session IDs, and content are synthetic.
