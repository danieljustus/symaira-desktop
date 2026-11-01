package main

// `meeting list|show|review|publish` and `meeting participant
// candidates|confirm|create|unlink-contact`, recorded for RUST-013 against the
// Go binary. Memory-backed steps run against a stateful fake `symmemory` in
// the case HOME (files below `mem/`, which SnapshotHome compares), so no real
// sibling tool, credential or network is involved.

func init() {
	cliIngestCaseBuilders = append(cliIngestCaseBuilders, cliIngestMeetingCases)
}

// fakeSymmemoryMeeting keeps its entities, relation log and memory log below
// $HOME/mem. `entity add` fails on a duplicate name like the real tool and
// `set` fails for values containing "boom".
const fakeSymmemoryMeeting = `#!/bin/sh
D="$HOME/mem"
mkdir -p "$D"
key() { printf '%s' "$1" | tr ' ' '_'; }
case "$1" in
version) echo '{"tool":"symmemory","version":"1.2.3","schema_version":1}' ;;
entity)
  case "$2" in
  show)
    f="$D/ent_$(key "$3")"
    if [ -f "$f" ]; then cat "$f"; echo "relations: none"; else echo "no such entity $3" >&2; exit 1; fi ;;
  add)
    f="$D/ent_$(key "$3")"
    if [ -f "$f" ]; then echo "UNIQUE constraint failed" >&2; exit 1; fi
    n=$(cat "$D/next" 2>/dev/null || echo 100)
    echo $((n+1)) > "$D/next"
    printf '{"id":"ent-%s","name":"%s","type":"%s","aliases":[],"description":""}\n' "$n" "$3" "$5" > "$f"
    echo "added $3" ;;
  list)
    printf '['
    sep=''
    for f in "$D"/ent_*; do
      [ -f "$f" ] || continue
      printf '%s' "$sep"
      tr -d '\n' < "$f"
      sep=','
    done
    echo ']' ;;
  relate) echo "$3|$4|$5" >> "$D/relations.log"; echo ok ;;
  *) echo "unexpected: $*" >&2; exit 2 ;;
  esac ;;
set)
  case "$3" in *boom*) echo "set exploded" >&2; exit 1 ;; esac
  n=$(cat "$D/memnext" 2>/dev/null || echo 1)
  echo $((n+1)) > "$D/memnext"
  echo "$3|$5|$9" >> "$D/set.log"
  printf '{"id":"mem-%s","content":"x","scope":"%s","entities":[]}\n' "$n" "$5" ;;
*) echo "unexpected: $*" >&2; exit 2 ;;
esac
`

const meetingFull = `---
type: meeting
title: Weekly Sync
created: 2026-03-01T10:00:00Z
tags:
  - meeting
  - weekly
meeting_id: m-100
started_at: 2026-03-01T10:00:00Z
ended_at: 2026-03-01T11:00:00Z
duration_ms: 3600000
language: de
participants:
  - label: Alice
    speaker_ids:
      - s1
  - label: Bob
    speaker_ids:
      - s2
      - s3
    entity_id: ent-2
    role: lead
    contact_ref:
      provider: symrelate
      schema_version: 1
      id: c-77
      kind: person
      display_name: Bob B
      note: kept
symmeet_source:
  artifact_schema_version: 2
  review_state: needs_review
custom_flag: true
z_extra:
  nested: [1, 2]
  name: "needs: quoting"
---
# Weekly Sync

Body text stays byte for byte.
  Indented line and trailing spaces   

> quote
`

const meetingMinimal = `---
type: meeting
title: 2024
created: 2026-07-01
meeting_id: 42
started_at: "2026-07-01 09:00"
symmeet_source:
  artifact_schema_version: 1
---
Short body
`

const meetingConfirmed = `---
type: meeting
title: Planning
created: "2026-04-02T08:00:00Z"
tags: []
meeting_id: m-200
started_at: "2026-04-02T08:00:00Z"
participants:
  - label: Alice Example
    speaker_ids: [a1]
    entity_id: ent-1
  - label: Bob Builder
    speaker_ids: [b1]
    entity_id: ent-2
  - label: Unconfirmed
    speaker_ids: [u1]
symmeet_source:
  artifact_schema_version: 2
  review_state: reviewed
---
Planning transcript
`

const meetingNoID = `---
type: meeting
title: No id
created: "2026-04-03T08:00:00Z"
tags: []
meeting_id: ""
started_at: "2026-04-03T08:00:00Z"
symmeet_source:
  artifact_schema_version: 2
  review_state: reviewed
---
x
`

const meetingUnknownEntity = `---
type: meeting
title: Ghost
created: "2026-04-04T08:00:00Z"
tags: []
meeting_id: m-300
started_at: "2026-04-04T08:00:00Z"
participants:
  - label: Ghost
    speaker_ids: [g1]
    entity_id: ent-404
symmeet_source:
  artifact_schema_version: 2
  review_state: reviewed
---
x
`

const meetingBadDuration = `---
type: meeting
title: Bad
created: "2026-04-05T08:00:00Z"
meeting_id: m-400
started_at: "2026-04-05T08:00:00Z"
duration_ms: lots
symmeet_source:
  artifact_schema_version: 2
  review_state: reviewed
---
x
`

func meetingVault() map[string]string {
	return map[string]string{
		"meetings/m1.md":         meetingFull,
		"meetings/m2.md":         meetingMinimal,
		"meetings/confirmed.md":  meetingConfirmed,
		"meetings/noid.md":       meetingNoID,
		"meetings/ghost.md":      meetingUnknownEntity,
		"meetings/bad.md":        meetingBadDuration,
		"notes/plain.md":         "---\ntitle: Plain\n---\nnot a meeting\n",
		"meetings/broken.md":     "---\ntype: meeting\ntitle: A\ntitle: B\n---\nbody\n",
		"meetings/nonmeeting.md": "---\ntype: note\ntitle: Notes\n---\nbody\n",
		"meetings/crlf.md":       "---\r\ntype: meeting\r\ntitle: CRLF\r\ncreated: \"2026-05-01T08:00:00Z\"\r\nmeeting_id: m-500\r\nstarted_at: \"2026-05-01T08:00:00Z\"\r\nsymmeet_source:\r\n  artifact_schema_version: 2\r\n  review_state: needs_review\r\n---\r\nbody\r\n",
	}
}

// meetingShape builds a meeting note whose extra frontmatter lines are chosen
// by the case; the standard fields stay valid.
func meetingShape(lines ...string) string {
	out := "---\ntype: meeting\ntitle: Shape\ncreated: \"2026-06-01T08:00:00Z\"\nmeeting_id: m-9\nstarted_at: \"2026-06-01T08:00:00Z\"\nparticipants:\n  - label: P\n    speaker_ids: [p1]\nsymmeet_source:\n  artifact_schema_version: 2\n  review_state: needs_review\n"
	for _, line := range lines {
		out += line + "\n"
	}
	return out + "---\nbody\n"
}

func meetingShapeVault() map[string]string {
	return map[string]string{
		"s/long.md":        meetingShape("duration_ms: a long string value"),
		"s/bool.md":        meetingShape("duration_ms: true"),
		"s/seq.md":         meetingShape("duration_ms: [1, 2]"),
		"s/float.md":       meetingShape("duration_ms: 1.5"),
		"s/ts.md":          meetingShape("duration_ms: 2026-01-01"),
		"s/title-seq.md":   meetingShape("language: [de, en]"),
		"s/title-map.md":   meetingShape("language: {a: 1}"),
		"s/tags-scalar.md": meetingShape("tags: notalist"),
		"s/tags-nested.md": meetingShape("tags: [a, 2, true]"),
		"s/two.md":         meetingShape("duration_ms: nope", "language: [x]"),
		"s/parts.md":       "---\ntype: meeting\ntitle: P\nmeeting_id: m-8\nparticipants: nope\nsymmeet_source:\n  artifact_schema_version: 2\n---\nb\n",
		"s/source.md":      "---\ntype: meeting\ntitle: P\nmeeting_id: m-8\nsymmeet_source: flat\n---\nb\n",
		"s/offset.md":      meetingShape("ended_at: 2026-06-01T10:00:00+02:00", "extra_at: 2026-06-01 10:00:00.5", "extra_day: 2026-06-02"),
		"s/extras.md": meetingShape(
			"a10: ten",
			"a2: two",
			"Zed: upper",
			"yes_no: [yes, no, null, ~, 'true', '123', '1.5', 1e3, 0x1f, 0o17]",
			"quoted: \"it's: a #test\"",
			"multi: |",
			"  line one",
			"  line two",
			"folded: >",
			"  folded text",
			"  continues",
			"empty_map: {}",
			"empty_list: []",
			"nulled:",
			"big: 123456789012",
			"float: 1.50",
			"neg: -3",
			"list_of_maps:",
			"  - {k: v, z: 1}",
			"  - [x, y]",
			"unicode_ключ: wert ünï",
			"'key: colon': value",
			"tab_text: \"a\\tb\"",
		),
		"s/ok.md": meetingShape(),
	}
}

func meetingHome() map[string]string {
	home := cliErrorsHomeFiles()
	home["bin/symmemory"] = fakeSymmemoryMeeting
	home["mem/ent_Alice_Example"] = "{\"id\":\"ent-1\",\"name\":\"Alice Example\",\"type\":\"person\",\"aliases\":[\"Alice\",\"Ally\"],\"description\":\"\"}\n"
	home["mem/ent_Bob_Builder"] = "{\"id\":\"ent-2\",\"name\":\"Bob Builder\",\"type\":\"person\",\"aliases\":[],\"description\":\"builder\"}\n"
	return home
}

func cliIngestMeetingCases() []cliIngestCase {
	const memoryPath = "$ROOT/home/bin:/usr/bin:/bin"
	memoryEnv := map[string]string{"PATH": memoryPath}
	noMemoryEnv := map[string]string{"PATH": "/usr/bin:/bin"}
	return []cliIngestCase{
		{Name: "meeting-read", Files: meetingVault(), SkipIndex: true, Steps: ingestConcat(
			ingestBoth("meeting", "list"),
			ingestBoth("meeting", "list", "--include-errors"),
			ingestBoth("meeting", "list", "extra"),
			ingestBoth("meeting", "show", "meetings/m1.md"),
			ingestBoth("meeting", "show", "meetings/m2.md"),
			ingestBoth("meeting", "show", "meetings/crlf.md"),
			ingestBoth("meeting", "show", "notes/plain.md"),
			ingestBoth("meeting", "show", "meetings/nonmeeting.md"),
			ingestBoth("meeting", "show", "meetings/broken.md"),
			ingestBoth("meeting", "show", "missing.md"),
			ingestBoth("meeting", "show", "../escape.md"),
			ingestBoth("meeting", "show"),
			ingestBoth("meeting", "show", "meetings/m1.md", "meetings/m2.md"),
		)},
		{Name: "meeting-shapes", Files: meetingShapeVault(), SkipIndex: true, SnapshotVault: true, Steps: ingestConcat(
			ingestBoth("meeting", "review", "s/long.md"),
			ingestBoth("meeting", "review", "s/bool.md"),
			ingestBoth("meeting", "review", "s/seq.md"),
			ingestBoth("meeting", "review", "s/float.md"),
			ingestBoth("meeting", "review", "s/ts.md"),
			ingestBoth("meeting", "review", "s/title-seq.md"),
			ingestBoth("meeting", "review", "s/title-map.md"),
			ingestBoth("meeting", "review", "s/tags-scalar.md"),
			ingestBoth("meeting", "review", "s/tags-nested.md"),
			ingestBoth("meeting", "review", "s/two.md"),
			ingestBoth("meeting", "review", "s/parts.md"),
			ingestBoth("meeting", "review", "s/source.md"),
			ingestBoth("meeting", "review", "s/offset.md"),
			ingestBoth("meeting", "review", "s/extras.md"),
			ingestBoth("meeting", "review", "s/ok.md"),
			ingestBoth("meeting", "show", "s/extras.md"),
			ingestBoth("meeting", "show", "s/offset.md"),
			ingestBoth("meeting", "list"),
		)},
		{Name: "meeting-contact", Files: meetingVault(), SkipIndex: true, SnapshotVault: true, SnapshotHome: true,
			Contacts: []cliIngestContact{
				{ID: "c-77", Name: "Bob Contact", Kind: "person"},
				{ID: "c-org", Name: "Acme GmbH", Kind: "organization"},
				{ID: "c-blank", Name: "", Kind: "person"},
			},
			Steps: ingestConcat(
				ingestBoth("meeting", "participant", "contact", "c-77"),
				ingestBoth("meeting", "participant", "contact", "c-org"),
				ingestBoth("meeting", "participant", "contact", "c-blank"),
				ingestBoth("meeting", "participant", "contact", "c-none"),
				ingestBoth("meeting", "participant", "contact", ""),
				ingestBoth("meeting", "participant", "contact"),
				ingestBoth("meeting", "participant", "link-contact", "meetings/m1.md", "s1", "c-77"),
				ingestBoth("meeting", "participant", "link-contact", "meetings/m1.md", "s2", "c-org"),
				ingestBoth("meeting", "participant", "link-contact", "meetings/m1.md", "s3", "c-none"),
				ingestBoth("meeting", "participant", "link-contact", "meetings/m1.md", "s9", "c-77"),
				ingestBoth("meeting", "participant", "link-contact", "notes/plain.md", "s1", "c-77"),
				ingestBoth("meeting", "participant", "link-contact", "missing.md", "s1", "c-77"),
				ingestBoth("meeting", "participant", "link-contact", "meetings/m1.md", "s1"),
				ingestBoth("meeting", "participant", "unlink-contact", "meetings/m1.md", "s1"),
				ingestBoth("meeting", "show", "meetings/m1.md"),
			)},
		{Name: "meeting-contact-empty-store", Files: meetingVault(), SkipIndex: true, SnapshotVault: true, Steps: ingestConcat(
			ingestBoth("meeting", "participant", "contact", "c-77"),
			ingestBoth("meeting", "participant", "link-contact", "meetings/m1.md", "s1", "c-77"),
		)},
		{Name: "meeting-empty-vault", Files: map[string]string{"a.md": "---\ntitle: A\n---\nx\n"}, Steps: ingestConcat(
			ingestBoth("meeting", "list"),
			ingestBoth("meeting", "list", "--include-errors"),
		)},
		{Name: "meeting-review", Files: meetingVault(), SkipIndex: true, SnapshotVault: true, Steps: ingestConcat(
			ingestBoth("meeting", "review", "meetings/m1.md"),
			ingestBoth("meeting", "review", "meetings/m1.md"),
			ingestBoth("meeting", "review", "meetings/m2.md"),
			ingestBoth("meeting", "review", "meetings/crlf.md"),
			ingestBoth("meeting", "review", "meetings/bad.md"),
			ingestBoth("meeting", "review", "meetings/broken.md"),
			ingestBoth("meeting", "review", "notes/plain.md"),
			ingestBoth("meeting", "review", "missing.md"),
			ingestBoth("meeting", "review", "../escape.md"),
			ingestBoth("meeting", "review"),
			ingestBoth("meeting", "list"),
		)},
		{Name: "meeting-participant-local", Files: meetingVault(), SkipIndex: true, SnapshotVault: true, Steps: ingestConcat(
			ingestBoth("meeting", "participant", "confirm", "meetings/m1.md", "s1", "ent-1"),
			ingestBoth("meeting", "participant", "confirm", "meetings/m1.md", "s3", "ent-9"),
			ingestBoth("meeting", "participant", "confirm", "meetings/m1.md", "s3"),
			ingestBoth("meeting", "participant", "confirm", "meetings/m1.md", "s9", "ent-1"),
			ingestBoth("meeting", "participant", "confirm", "meetings/m2.md", "s1", "ent-1"),
			ingestBoth("meeting", "participant", "confirm", "notes/plain.md", "s1", "ent-1"),
			ingestBoth("meeting", "participant", "confirm", "missing.md", "s1", "ent-1"),
			ingestBoth("meeting", "participant", "confirm", "meetings/m1.md"),
			ingestBoth("meeting", "participant", "unlink-contact", "meetings/m1.md", "s2"),
			ingestBoth("meeting", "participant", "unlink-contact", "meetings/m1.md", "s2"),
			ingestBoth("meeting", "participant", "unlink-contact", "meetings/m1.md", "s9"),
			ingestBoth("meeting", "participant", "unlink-contact", "meetings/m1.md"),
			ingestBoth("meeting", "participant", "unlink-contact", "notes/plain.md", "s1"),
			ingestBoth("meeting", "participant", "confirm", "meetings/bad.md", "s1", "ent-1"),
			ingestBoth("meeting", "show", "meetings/m1.md"),
		)},
		{Name: "meeting-memory", Files: meetingVault(), SkipIndex: true, HomeFiles: meetingHome(), Env: memoryEnv, SnapshotVault: true, SnapshotHome: true, Steps: ingestConcat(
			ingestBoth("meeting", "participant", "candidates", "alice example"),
			ingestBoth("meeting", "participant", "candidates", "Alice"),
			ingestBoth("meeting", "participant", "candidates", "ALLY"),
			ingestBoth("meeting", "participant", "candidates", "nobody"),
			ingestBoth("meeting", "participant", "candidates", "  "),
			ingestBoth("meeting", "participant", "candidates"),
			ingestBoth("meeting", "participant", "create", "meetings/m1.md", "s1", "Alice Example"),
			ingestBoth("meeting", "participant", "create", "meetings/m1.md", "s2", "  Carol New  "),
			ingestBoth("meeting", "participant", "create", "meetings/m1.md", "s2", "Carol New"),
			ingestBoth("meeting", "participant", "create", "meetings/m1.md", "s9", "Dave"),
			ingestBoth("meeting", "participant", "create", "meetings/m1.md", "s1", "   "),
			ingestBoth("meeting", "participant", "create", "notes/plain.md", "s1", "Erin"),
			ingestBoth("meeting", "participant", "create", "meetings/m1.md", "s1"),
			ingestBoth("meeting", "participant", "candidates", "Carol New"),
		)},
		{Name: "meeting-publish", Files: meetingVault(), SkipIndex: true, HomeFiles: meetingHome(), Env: memoryEnv, SnapshotVault: true, SnapshotHome: true, Steps: ingestConcat(
			ingestBoth("meeting", "publish", "meetings/confirmed.md"),
			ingestBoth("meeting", "publish", "meetings/confirmed.md", "--fact", "Alice owns the roadmap", "--fact", "  Budget approved  ", "--fact", "", "--fact", "Alice owns the roadmap"),
			ingestBoth("meeting", "publish", "meetings/confirmed.md", "--fact", "Alice owns the roadmap", "--fact", "New decision"),
			ingestBoth("meeting", "publish", "meetings/m1.md", "--fact", "Weekly fact"),
			ingestBoth("meeting", "publish", "meetings/m2.md", "--fact", "Short fact"),
			ingestBoth("meeting", "publish", "meetings/noid.md", "--fact", "x"),
			ingestBoth("meeting", "publish", "meetings/ghost.md", "--fact", "x"),
			ingestBoth("meeting", "publish", "meetings/confirmed.md", "--fact", "this will boom"),
			ingestBoth("meeting", "publish", "meetings/confirmed.md", "--fact", "Fine one", "--fact", "boom two", "--fact", "never reached"),
			ingestBoth("meeting", "publish", "meetings/confirmed.md", "--fact", "Fine one", "--fact", "Fine two"),
			ingestBoth("meeting", "publish", "notes/plain.md"),
			ingestBoth("meeting", "publish", "missing.md"),
			ingestBoth("meeting", "publish"),
		)},
		{Name: "meeting-no-memory", Files: meetingVault(), SkipIndex: true, Env: noMemoryEnv, SnapshotVault: true, Steps: ingestConcat(
			ingestBoth("meeting", "participant", "candidates", "Alice"),
			ingestBoth("meeting", "participant", "create", "meetings/m1.md", "s1", "Alice"),
			ingestBoth("meeting", "publish", "meetings/confirmed.md", "--fact", "x"),
			ingestBoth("meeting", "participant", "confirm", "meetings/m1.md", "s1", "ent-1"),
		)},
		{Name: "meeting-no-vault", NoVault: true, SkipIndex: true, Steps: ingestConcat(
			ingestBoth("meeting", "list"),
			ingestBoth("meeting", "show", "a.md"),
			ingestBoth("meeting", "review", "a.md"),
		)},
	}
}
