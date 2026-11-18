# Meet store fixtures (inputs of `tests/meeting_import.rs`)

A miniature `$XDG_DATA_HOME/symmeet` tree; `meetings/<uuid>/` is the layout of
`meet/Sources/SymMeetCore/Storage/ArtifactLayout.swift`. The tests copy these
directories into a temp store, so the fixtures are never modified.

| Path | Provenance |
|---|---|
| `meetings/00000000-0000-0000-0000-000000000001/manifest.json` | byte copy of `meet/Tests/Fixtures/integration/meeting-complete/manifest.json` (decoded by the Swift contract tests) |
| `meetings/00000000-0000-0000-0000-000000000001/segments.raw.jsonl` | byte copy of `meet/Tests/Fixtures/integration/meeting-complete/segments.jsonl` |
| `kitchen-sink.expected.md` | byte copy of `meet/Tests/Fixtures/exports/kitchen-sink.md`, the Swift `MarkdownRenderer` golden |
| `meetings/1111…/segments.raw.jsonl` | **derived**: the `segments` array of the Swift golden `meet/Tests/Fixtures/exports/kitchen-sink.json`, one compact JSON object per line (file order kept), so the Rust transcript can be compared with the Swift golden |
| `meetings/1111…/manifest.json` | **hand-authored** from `MeetingManifest` (`SymMeetCore/Contracts/MeetingManifest.swift`): the `meeting-complete` manifest with the golden's meeting id and `language: multi` |
| `meetings/1111…/turns.raw.jsonl` | **hand-authored** from `SpeakerTurn` (`Speakers/SpeakerTurn.swift`) |
| `meetings/1111…/speaker_edits.jsonl` | **hand-authored** from `SpeakerEditEvent` (`Speakers/SpeakerMap.swift`): two `label` events |

No file here was written by the Swift recorder itself; the Swift repository has
no recorder output checked in. Real files from Swift are the `meet/Tests/Fixtures`
copies above; everything marked hand-authored follows the Codable types exactly
(ISO-8601 dates without fractional seconds, snake_case keys).
