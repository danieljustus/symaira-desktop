# v1 contracts

The v1 schemas describe portable meeting artifacts. All field names are
snake_case. A document with an unsupported major `schema_version` must be
rejected; unknown additive fields from supported v1 documents must be preserved
when rewritten.

- `meeting-v1.schema.json` — the durable manifest.
- `segment-v1.schema.json` — raw or user-corrected timed text.
- `events-v1.schema.json` — append-only JSONL event envelopes.

Timestamps are RFC 3339 strings. Media positions are integer milliseconds.
