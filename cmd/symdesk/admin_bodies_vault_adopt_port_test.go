package main

// `vault adopt [<dir>] [--dry-run]`: backfills title, created and tags in
// place. Created values derive from the file name (daily notes) or the file's
// birth/modification time, which the cases pin with explicit past mtimes.

func init() {
	adminBodiesCaseBuilders = append(adminBodiesCaseBuilders, adminVaultAdoptCases)
}

func adminAdoptFiles() map[string]string {
	return map[string]string{
		"plain.md":          "just words\n",
		"h1.md":             "intro\n\n# Real Title\n\nbody\n",
		"full.md":           "---\ntitle: Full\ncreated: 2026-01-02T03:04:05Z\ntags: [a]\n---\nbody\n",
		"full-quoted.md":    "---\ntitle: \"Q\"\ncreated: \"2026-01-02\"\ntags: []\n---\nbody\n",
		"notitle.md":        "---\ncreated: 2026-01-02\ntags: [a]\n---\n# H\nbody\n",
		"notags.md":         "---\ntitle: T\ncreated: \"x\"\n---\nbody\n",
		"nullfields.md":     "---\ntitle:\ncreated:\ntags:\n---\nbody\n",
		"numtitle.md":       "---\ntitle: 2026\ncreated: 5\n---\nbody\n",
		"2026-03-04.md":     "daily words\n",
		"2026-02-30.md":     "not a date\n",
		"crlf.md":           "---\r\ntitle: Crlf\r\n---\r\nbody\r\n",
		"crlf-plain.md":     "plain\r\ntext\r\n",
		"late-fm.md":        "intro line\n---\ntitle: Late\n---\nbody\n",
		"unterminated.md":   "---\ntitle: Open\nno end\n",
		"sub/dir/nested.md": "# Nested H1 \n",
		"unicode-é.md":      "ünï body\n",
		"quote\"title.md":   "x\n",
		"emptyh1.md":        "#  \n# Second\n",
		"spaces h1.md":      "  # Indented\n",
		"tag-only.md":       "---\ntags: [x]\n---\nbody #y\n",
		"empty.md":          "",
		".hidden.md":        "skipped by the walk\n",
		"notes.txt":         "not markdown\n",
		"node_modules/a.md": "skipped directory\n",
	}
}

// adminAdoptMtimes gives every Markdown file a distinct past modification
// time; birth times follow on hosts that derive them.
func adminAdoptMtimes(files map[string]string) map[string]int64 {
	mtimes := map[string]int64{}
	seconds := int64(1700000000)
	for _, name := range sortedKeys(files) {
		mtimes[name] = seconds
		seconds += 86400 * 3
	}
	return mtimes
}

func adminVaultAdoptCases() []adminBodiesCase {
	files := adminAdoptFiles()
	// The duplicate-key note cannot be indexed, so it appears after the setup
	// index run.
	late := map[string]string{"dupe.md": "---\ntitle: A\ntitle: B\n---\nbody\n"}
	mtimes := adminAdoptMtimes(map[string]string{"dupe.md": "", "a-late.md": ""})
	for name, seconds := range adminAdoptMtimes(files) {
		mtimes[name] = seconds
	}
	small := map[string]string{
		"a.md": "---\ntitle: A\ncreated: \"2026-01-01\"\ntags: []\n---\nbody\n",
		"b.md": "# B heading\n\nbody #tag\n",
	}
	smallTimes := adminAdoptMtimes(small)
	return []adminBodiesCase{
		{Name: "vault-adopt-lifecycle", Files: files, PostIndexFiles: late, Mtimes: mtimes, SnapshotVault: true, Volatile: true, Steps: adminConcat(
			adminBoth("vault", "adopt", "--dry-run"),
			adminBoth("vault", "adopt"),
			adminBoth("vault", "adopt"),
			adminBoth("vault", "adopt", "--dry-run"),
		)},
		{Name: "vault-adopt-unindexed", Files: small, Mtimes: smallTimes, SkipIndex: true, SnapshotVault: true, Volatile: true, Steps: adminConcat(
			adminBoth("vault", "adopt"),
			adminBoth("vault", "adopt", "--dry-run=false"),
			adminBoth("vault", "adopt", "--dry-run=true"),
		)},
		{Name: "vault-adopt-directory-argument", Files: small, Mtimes: smallTimes, SnapshotVault: true, Volatile: true, Steps: adminConcat(
			adminBoth("vault", "adopt", "$ROOT/vault", "--dry-run"),
			adminBoth("vault", "adopt", "--dry-run", "$ROOT/vault"),
			adminBoth("vault", "adopt", "$ROOT/missing"),
			adminBoth("vault", "adopt", "$ROOT/vault/a.md"),
			adminBoth("vault", "adopt", "$ROOT/vault", "$ROOT/vault"),
			adminBoth("vault", "adopt", "--bogus"),
			adminBoth("vault", "adopt", "$ROOT/vault"),
		)},
		{Name: "vault-adopt-without-vault", NoVault: true, SkipIndex: true, Steps: adminConcat(
			adminBoth("vault", "adopt"),
			adminBoth("vault", "adopt", "--dry-run"),
			adminBoth("vault", "adopt", "$ROOT/missing"),
		)},
		{Name: "vault-adopt-empty-vault", Files: map[string]string{}, SnapshotVault: true, Volatile: true, Steps: adminConcat(
			adminBoth("vault", "adopt"),
			adminBoth("vault", "adopt", "--dry-run"),
		)},
		{Name: "vault-group", Files: small, Mtimes: smallTimes, Volatile: true, Steps: adminConcat(
			adminBoth("vault"),
			adminBoth("vault", "bogus"),
			adminBoth("vault", "adopt", "--help"),
		)},
	}
}
