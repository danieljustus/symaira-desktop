# SymDesk Integration

SymDesk imports and reviews meeting artifacts produced by `symmeet`.

## Import flow

1. SymDesk calls `symmeet meeting list --json` to discover available meetings.
2. SymDesk calls `symmeet meeting show <meeting_id> --json` for metadata.
3. SymDesk calls `symmeet export <meeting_id> --format markdown --output -` for the transcript.
4. SymDesk displays the transcript in the review UI.

## Review workflow

After the user reviews and corrects speaker labels or transcript text:

1. SymDesk writes corrected segments back to the meeting artifact directory.
2. SymDesk marks the meeting as reviewed in its own state (not in the artifact).
3. The next export prefers edited segments over raw segments.

## Participant confirmation

Confirmed `entity_id` values (mapping a speaker to a known person) are
stored separately from `speaker_id` values. SymDesk never writes
entity mappings into the meeting artifact — those live in SymDesk's
own review state.

## Timeout guidance

| Operation | Timeout |
|-----------|---------|
| `meeting list` | 5s |
| `meeting show` | 5s |
| `export --format markdown` | 30s |

## Error handling

- Exit code 0: success
- Exit code 1: runtime failure (meeting corrupted, disk full)
- Exit code 2: invalid input (unknown meeting ID)
- Exit code 3: permission denied (meeting in trash without `--include-trashed`)

SymDesk should surface the stderr message to the user and retry on
exit code 1 if the error is transient (disk full, lock contention).
