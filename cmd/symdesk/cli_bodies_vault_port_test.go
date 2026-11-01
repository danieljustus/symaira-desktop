package main

// Vault-editing command bodies recorded for RUST-013: props, tags, history
// checkpoint/undo-task and the cobra group behaviour of `trash`.

func init() {
	cliBodiesCaseBuilders = append(cliBodiesCaseBuilders, cliBodiesVaultEditCases)
}

func cliBodiesVaultEditCases() []cliBodiesCase {
	vault := bodiesVault()
	var cases []cliBodiesCase

	cases = append(cases, cliBodiesCase{Name: "props", Files: vault, SnapshotVault: true, Steps: bodiesConcat(
		bodiesBoth("props", "get", "a.md"),
		bodiesBoth("props", "get", "tagged.md"),
		bodiesBoth("props", "get", "plain.md"),
		bodiesBoth("props", "get", "missing.md"),
		bodiesBoth("props", "get", "../escape.md"),
		bodiesBoth("props", "get", "notes/c.md"),
		bodiesBoth("props", "get", "unicode-é.md"),
		bodiesBoth("props", "edit", "a.md", "title", "Renamed"),
		bodiesBoth("props", "get", "a.md"),
		bodiesBoth("props", "edit", "a.md", "status", "draft"),
		bodiesBoth("props", "edit", "a.md", "status", "reviewed"),
		bodiesBoth("props", "edit", "a.md", "asn", "5"),
		bodiesBoth("props", "edit", "plain.md", "k", "v"),
		bodiesBoth("props", "get", "plain.md"),
		bodiesBoth("props", "edit", "missing.md", "k", "v"),
		bodiesBoth("props", "edit", "../escape.md", "k", "v"),
		bodiesBoth("props", "edit", "b.md", "key with space", "v: colon"),
		bodiesBoth("props", "edit", "b.md", "number", "42"),
		bodiesBoth("props", "edit", "b.md", "flag", "true"),
		bodiesBoth("props", "edit", "b.md", "tags", "[q, r]"),
		bodiesBoth("props", "edit", "b.md", "empty", ""),
		bodiesBoth("props", "edit", "crlf.md", "title", "Crlf edited"),
		bodiesBoth("props", "edit", "unicode-é.md", "title", "Ünï edited"),
		bodiesBoth("props", "get", "b.md"),
		bodiesBoth("props", "get", "crlf.md"),
		bodiesBoth("props", "get"),
		bodiesBoth("props", "get", "a.md", "b.md"),
		bodiesBoth("props", "edit", "a.md", "k"),
		bodiesBoth("props", "bogus"),
		bodiesBoth("props"),
	)})

	cases = append(cases, cliBodiesCase{Name: "props-unindexed", Files: vault, SkipIndex: true, SnapshotVault: true, Steps: bodiesConcat(
		bodiesBoth("props", "get", "a.md"),
		bodiesBoth("props", "edit", "a.md", "title", "Unindexed edit"),
		bodiesBoth("props", "get", "a.md"),
	)})

	cases = append(cases, cliBodiesCase{Name: "tags", Files: vault, SnapshotVault: true, Steps: bodiesConcat(
		bodiesBoth("tags", "rename", "x", "w"),
		bodiesBoth("tags", "rename", "x", "w"),
		bodiesBoth("tags", "rename", "Y", "y2"),
		bodiesBoth("tags", "rename", "w", "w"),
		bodiesBoth("tags", "rename", "", "w"),
		bodiesBoth("tags", "rename", "w", ""),
		bodiesBoth("tags", "rename", "  w  ", "  v  "),
		bodiesBoth("tags", "merge", "v", "z"),
		bodiesBoth("tags", "merge", "z", "z"),
		bodiesBoth("tags", "merge", "", "z"),
		bodiesBoth("tags", "merge", "other", "merged"),
		bodiesBoth("tags", "merge", "nothing", "somewhere"),
		bodiesBoth("tags", "delete", "z"),
		bodiesBoth("tags", "delete", "z"),
		bodiesBoth("tags", "delete", ""),
		bodiesBoth("tags", "delete", "merged"),
		bodiesBoth("tags", "rename", "y2"),
		bodiesBoth("tags", "delete"),
		bodiesBoth("tags", "bogus"),
		bodiesBoth("tags"),
	)})

	cases = append(cases, cliBodiesCase{Name: "tags-unindexed", Files: vault, SkipIndex: true, SnapshotVault: true, Steps: bodiesConcat(
		bodiesBoth("tags", "rename", "x", "w"),
		bodiesBoth("tags", "delete", "w"),
	)})

	cases = append(cases, cliBodiesCase{Name: "history-checkpoint", Files: vault, SnapshotVault: true, Steps: bodiesConcat(
		bodiesBoth("history", "checkpoint", "t1"),
		bodiesBoth("history", "checkpoint", "t1"),
		bodiesBoth("history", "checkpoint", "t2", "a.md", "b.md"),
		bodiesBoth("history", "checkpoint", "t2", "a.md", "missing.md"),
		bodiesBoth("history", "checkpoint", "t3", "new-file.md"),
		bodiesBoth("history", "checkpoint", "t4", "../escape.md"),
		bodiesBoth("history", "checkpoint", "t5", "notes/c.md", "notes/c.md", "plain.md"),
		bodiesBoth("history", "checkpoint", "../bad", "a.md"),
		bodiesBoth("history", "checkpoint", ".dot"),
		bodiesBoth("history", "checkpoint", "a:b"),
		bodiesBoth("history", "checkpoint", ""),
		bodiesBoth("history", "checkpoint", "with/slash"),
		bodiesBoth("history", "checkpoint"),
		bodiesBoth("history", "tasks"),
		bodiesBoth("note", "move", "a.md", "moved-a.md"),
		bodiesBoth("props", "edit", "b.md", "title", "Changed after checkpoint"),
		bodiesBoth("note", "move", "plain.md", "new-file.md"),
		bodiesBoth("history", "undo-task", "t3"),
		bodiesBoth("history", "undo-task", "t3"),
		bodiesBoth("history", "undo-task", "t2"),
		bodiesBoth("history", "undo-task", "t1"),
		bodiesBoth("history", "undo-task", "missing-task"),
		bodiesBoth("history", "undo-task", "../bad"),
		bodiesBoth("history", "undo-task"),
		bodiesBoth("history", "undo-task", "t2", "t3"),
		bodiesBoth("history", "tasks"),
		bodiesBoth("ls"),
	)})

	cases = append(cases, cliBodiesCase{Name: "history-checkpoint-unreadable", Files: vault, UnixOnly: true, SnapshotVault: true, Steps: bodiesConcat(
		bodiesBoth("history", "checkpoint", "t1", "notes"),
		bodiesBoth("history", "undo-task", "t1"),
	)})

	cases = append(cases, cliBodiesCase{Name: "trash-group", Files: vault, SnapshotVault: true, Steps: bodiesConcat(
		bodiesBoth("trash", "delete", "missing"),
		bodiesBoth("trash", "delete"),
		bodiesBoth("trash", "delete", "a.md", "b.md"),
		bodiesBoth("trash", "--bogus"),
		bodiesBoth("trash"),
		bodiesBoth("note", "delete", "a.md"),
		bodiesBoth("trash", "list"),
		bodiesBoth("trash", "delete", "a.md"),
		bodiesBoth("trash", "list"),
	)})

	return cases
}
