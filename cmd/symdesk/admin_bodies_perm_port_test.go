package main

// `perm user|group ...`: named accounts and groups below `<vault>/.symdesk`.
// Tokens are random 32-byte hex strings, so the cases are Volatile: the token
// and its stored hash are recorded as `$SHA256`, everything else byte for byte.

func init() {
	adminBodiesCaseBuilders = append(adminBodiesCaseBuilders, adminPermCases)
}

func adminPermCases() []adminBodiesCase {
	vault := map[string]string{"a.md": "hello\n"}
	return []adminBodiesCase{
		{Name: "perm-lifecycle", Files: vault, SnapshotVault: true, Volatile: true, Steps: adminConcat(
			adminBoth("perm", "user", "list"),
			adminBoth("perm", "group", "list"),
			adminBoth("perm", "user", "add", "alice"),
			adminBoth("perm", "user", "add", "alice"),
			adminBoth("perm", "user", "add", "bob"),
			adminBoth("perm", "user", "list"),
			adminBoth("perm", "group", "add", "team"),
			adminBoth("perm", "group", "add", "team"),
			adminBoth("perm", "group", "add", "team2"),
			adminBoth("perm", "group", "list"),
			adminBoth("perm", "user", "add", "carol", "--group", "team"),
			adminBoth("perm", "user", "add", "dave", "--group", "team"),
			adminBoth("perm", "user", "add", "erin", "--group", "nogroup"),
			adminBoth("perm", "group", "list"),
			adminBoth("perm", "user", "list"),
			adminBoth("perm", "user", "token", "alice"),
			adminBoth("perm", "user", "token", "alice", "--generate"),
			adminBoth("perm", "user", "token", "nobody", "--generate"),
			adminBoth("perm", "user", "token", "alice", "--generate=false"),
			adminBoth("perm", "user", "remove", "carol"),
			adminBoth("perm", "user", "remove", "ghost"),
			adminBoth("perm", "group", "list"),
			adminBoth("perm", "group", "remove", "team"),
			adminBoth("perm", "group", "remove", "ghost"),
			adminBoth("perm", "group", "remove", "team2"),
			adminBoth("perm", "group", "list"),
			adminBoth("perm", "user", "list"),
		)},
		{Name: "perm-arguments", Files: vault, SnapshotVault: true, Volatile: true, Steps: adminConcat(
			adminBoth("perm", "user", "add", ""),
			adminBoth("perm", "group", "add", ""),
			adminBoth("perm", "user", "add"),
			adminBoth("perm", "user", "add", "a", "b"),
			adminBoth("perm", "user", "list", "extra"),
			adminBoth("perm", "user", "add", "x", "--group"),
			adminBoth("perm", "user", "add", "x", "--group", "a", "--group", "b"),
			adminBoth("perm", "user", "remove"),
			adminBoth("perm", "group", "remove"),
			adminBoth("perm", "user", "token"),
			adminBoth("perm"),
			adminBoth("perm", "user"),
			adminBoth("perm", "group"),
			adminBoth("perm", "bogus"),
			adminBoth("perm", "user", "bogus"),
			adminBoth("perm", "user", "add", "<&>"),
			adminBoth("perm", "user", "add", "ünï \"q\""),
			adminBoth("perm", "group", "add", "<&> g"),
			adminBoth("perm", "user", "list"),
			adminBoth("perm", "group", "list"),
		)},
		{Name: "perm-stored-state", Files: map[string]string{
			"a.md":                      "hello\n",
			".symdesk/groups.json":      "[{\"name\":\"g\",\"members\":[\"x\"]},{\"name\":\"h\"}]",
			".symdesk/users.json":       "[{\"name\":\"x\",\"token_hash\":\"abc\",\"roles\":[\"admin\",\"user\"]},{\"name\":\"y\",\"token_hash\":\"def\"}]",
			".symdesk/permissions.json": "[]",
		}, SnapshotVault: true, Volatile: true, Steps: adminConcat(
			adminBoth("perm", "user", "list"),
			adminBoth("perm", "group", "list"),
			adminBoth("perm", "user", "add", "z", "--group", "g"),
			adminBoth("perm", "user", "remove", "x"),
			adminBoth("perm", "group", "list"),
			adminBoth("perm", "user", "list"),
		)},
		{Name: "perm-corrupt-state", Files: map[string]string{
			"a.md":                 "hello\n",
			".symdesk/users.json":  "{bad",
			".symdesk/groups.json": "[{\"name\":1}]",
		}, SnapshotVault: true, Volatile: true, Steps: adminConcat(
			adminBoth("perm", "user", "list"),
			adminBoth("perm", "user", "add", "a"),
			adminBoth("perm", "group", "list"),
			adminBoth("perm", "group", "add", "g"),
			adminBoth("perm", "user", "token", "a", "--generate"),
			adminBoth("perm", "user", "remove", "a"),
		)},
		{Name: "perm-without-vault", NoVault: true, SkipIndex: true, Steps: adminConcat(
			adminBoth("perm", "user", "list"),
			adminBoth("perm", "group", "add", "g"),
			adminBoth("perm", "user", "token", "a", "--generate"),
			adminBoth("--vault", "$ROOT/missing", "perm", "group", "list"),
		)},
	}
}
