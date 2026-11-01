package main

// `vault health`: the read-only repair-plan scan. Each case keeps the finding
// count at or below twelve, where Go's sort.Slice is an insertion sort and the
// order of equal (path, category) findings is stable.

func init() {
	adminBodiesCaseBuilders = append(adminBodiesCaseBuilders, adminVaultHealthCases)
}

func adminVaultHealthCases() []adminBodiesCase {
	doc := func(title, body string) string {
		return "---\ntitle: " + title + "\n---\n" + body
	}
	clean := map[string]string{
		"a.md":           doc("Alpha", "See [[b]] and [beta](notes/b.md) and https://example.com/x and [top](#top).\n"),
		"notes/b.md":     doc("Beta", "Back to [[a.md]] and [[Alpha]] and [[people-without-extension]].\n"),
		"assets/pic.png": "not really a png",
	}
	findings := map[string]string{
		"no-frontmatter.md": "plain body without frontmatter\n",
		"links.md": doc("Links", "[[missing/doc]] [[folder/pic.png]] [[assets/pic.png]] [[notes/b#Heading|alias]] [[Gamma Alias]] [[ghost.md]]\n"+
			"[ok](notes/b.md) [bad](notes/gone.md) [esc](../outside.md) [enc](notes/b%20x.md) [abs](/etc/passwd) [mail](mailto:a@example.com)\n"+
			"```\n[[in-code/ignored]] [x](in-code/ignored.md)\n```\n"),
		"notes/b.md":     "---\ntitle: Beta\naliases: [Gamma Alias]\n---\nbody\n",
		"assets/pic.png": "bytes",
		"data.canvas":    "{}",
	}
	derived := map[string]string{
		"src/source.md":      doc("Source", "source text\n"),
		"gen/fresh.md":       "---\ntitle: Fresh\nderived: true\nderived_from: src/source.md\n---\nfresh\n",
		"gen/stale.md":       "---\ntitle: Stale\nderived_from: \"[[Source]]\"\n---\nstale\n",
		"gen/orphan.md":      "---\ntitle: Orphan\nderived_from: nowhere/vanished.md\n---\norphan\n",
		"gen/noext.md":       "---\ntitle: NoExt\nderived_from: src/source\n---\nnoext\n",
		"gen/flag-only.md":   "---\ntitle: Flag\nderived: true\n---\nflag\n",
		"gen/relative.md":    "---\ntitle: Relative\nderived_from: ../src/source.md\n---\nrelative\n",
		"gen/pipe-anchor.md": "---\ntitle: Anchor\nderived_from: \"[[source.md#Part|label]]\"\n---\nanchor\n",
	}
	duplicates := map[string]string{
		"one.md":   doc("One", "The quick brown fox jumps over the lazy dog near the quiet river bank every morning at dawn.\n"),
		"two.md":   doc("Two", "The quick brown fox jumps over the lazy dog near the quiet river bank every morning at dawn.\n"),
		"three.md": doc("Three", "Completely different content about spreadsheets, invoices and archive retention schedules.\n"),
	}
	oneFile := map[string]string{"a.md": doc("A", "body\n")}
	return []adminBodiesCase{
		{Name: "health-clean", Files: clean, Steps: adminConcat(adminBoth("vault", "health"))},
		{Name: "health-findings", Files: findings, PostIndexFiles: map[string]string{"bad-asn.md": "---\ntitle: Bad\nasn: 0\n---\nbody\n"}, Steps: adminConcat(adminBoth("vault", "health"))},
		{Name: "health-derived", Files: derived, Mtimes: map[string]int64{
			"src/source.md": 1700000500, "gen/fresh.md": 1700001000, "gen/stale.md": 1700000000,
			"gen/noext.md": 1700000000, "gen/relative.md": 1700001000, "gen/pipe-anchor.md": 1700000000,
			"gen/orphan.md": 1700000000, "gen/flag-only.md": 1700000000,
		}, Steps: adminConcat(adminBoth("vault", "health"))},
		{Name: "health-duplicates", Files: duplicates, Steps: adminConcat(
			adminBoth("vault", "health"),
			adminBoth("vault", "health", "--duplicate-threshold", "100"),
			adminBoth("vault", "health", "--duplicate-threshold", "0"),
			adminBoth("vault", "health", "--duplicate-threshold=-5"),
			adminBoth("vault", "health", "--duplicate-threshold", "1"),
		)},
		{Name: "health-arguments", Files: oneFile, Steps: adminConcat(
			adminBoth("vault", "health", "extra"),
			adminBoth("vault", "health", "--duplicate-threshold", "abc"),
			adminBoth("vault", "health", "--duplicate-threshold", "0x10"),
			adminBoth("vault", "health", "--bogus"),
			adminBoth("vault", "health", "--duplicate-threshold"),
		)},
		{Name: "health-empty-vault", Files: map[string]string{"readme.txt": "text only\n"}, Steps: adminConcat(adminBoth("vault", "health"))},
		{Name: "health-without-vault", NoVault: true, SkipIndex: true, Steps: adminConcat(adminBoth("vault", "health"))},
	}
}
