# Architecture

`symmeet` is intentionally standalone. The filesystem artifact is the durable
source of truth; indexes and projections can be rebuilt from it.

```text
symmeet executable -> SymMeetMCP -> SymMeetCore
SymMeetAgent -> SymMeetCore
SymDesk -> symmeet runtime JSON contracts
```

`SymMeetCore` owns versioned contracts, artifact storage, privacy enforcement,
and processing-independent logic. `SymMeetMCP` is a narrow stdio adapter and
must emit only protocol frames on stdout. The executable owns CLI formatting.

Raw media and append-only events are the authoritative evidence. Corrected
transcript text and Markdown are user-editable projections. A projection must
never overwrite engine output or claim to be the raw record.

Audio tracks are separate (`microphone`, `system`, and `original`) so capture
and later processing can preserve provenance. Raw diarization uses anonymous
`speaker_id` values; a Markdown projection may link confirmed people without
putting names into the diarization output.

No sibling Symaira repository imports `SymMeetCore`. Cross-repository use is
runtime-only and uses the documented snake_case JSON contracts.
