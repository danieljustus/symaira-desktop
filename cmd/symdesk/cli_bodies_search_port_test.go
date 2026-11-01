package main

// `search export`, `search notebook` and `notebook delete`, recorded for
// RUST-013. The wall-clock `created:` timestamps of the written notes are
// normalized (see normalizeBodiesText); every other byte of the exported
// Markdown is compared through the vault snapshot.

func init() {
	cliBodiesCaseBuilders = append(cliBodiesCaseBuilders, cliBodiesSearchCases)
}

func cliBodiesSearchCases() []cliBodiesCase {
	vault := bodiesVault()
	var cases []cliBodiesCase

	cases = append(cases, cliBodiesCase{Name: "search-export", VolatileHistory: true, Files: vault, SnapshotVault: true, Steps: bodiesConcat(
		bodiesBoth("search", "export", "hello"),
		bodiesBoth("search", "export", "hello"),
		bodiesBoth("search", "export", "hello", "--title", "My Hello Results"),
		bodiesBoth("search", "save-note", "gamma", "--format", "md"),
		bodiesBoth("search", "export", "hello", "--output", "out/hello-set"),
		bodiesBoth("search", "export", "hello", "--output", "out/hello-set.md"),
		bodiesBoth("search", "export", "hello", "--output=deep/er/set.markdown"),
		bodiesBoth("search", "export", "nomatchatall"),
		bodiesBoth("search", "export", "  hello  "),
		bodiesBoth("search", "export", "title:\"quoted\" `tick`", "--title", "# Heading: with `ticks` & more"),
		bodiesBoth("search", "export", "hello", "--output", "$ROOT/vault/abs/in-vault.md"),
		bodiesBoth("search", "export", "hello", "--output", "$ROOT/elsewhere.md"),
		bodiesBoth("search", "export", "hello", "--output", "../escape.md"),
		bodiesBoth("search", "export", "hello", "--output", ".symdesk/x.md"),
		bodiesBoth("search", "export", "hello", "--output", "."),
		bodiesBoth("search", "export", "hello", "--format", "html"),
		bodiesBoth("search", "export", "hello", "--format", "MARKDOWN"),
		bodiesBoth("search", "export", ""),
		bodiesBoth("search", "export", "   "),
		bodiesBoth("search", "export"),
		bodiesBoth("search", "export", "a", "b"),
		bodiesJSON("search", "hello"),
		bodiesJSON("search", "search_result_set"),
	)})

	cases = append(cases, cliBodiesCase{Name: "search-export-pdf-no-engine", VolatileHistory: true, Files: vault, SnapshotVault: true,
		Env: map[string]string{"PATH": "/nonexistent-r013-bin"},
		Steps: bodiesConcat(
			bodiesBoth("search", "export", "hello", "--format", "pdf"),
			bodiesBoth("search", "export", "hello", "--format", "pdf", "--output", "$ROOT/cwd/x.pdf"),
			bodiesBoth("search", "export", "hello", "--format", "PDF", "--title", "T"),
		)})

	cases = append(cases, cliBodiesCase{Name: "search-notebook", VolatileHistory: true, Files: vault, SnapshotVault: true, Steps: bodiesConcat(
		bodiesBoth("search", "notebook", "hello"),
		bodiesBoth("search", "keep", "hello", "--title", "Kept hello"),
		bodiesBoth("search", "save", "gamma"),
		bodiesBoth("search", "notebook", "hello"),
		bodiesBoth("search", "notebook", "nomatchatall"),
		bodiesBoth("search", "notebook", "hello", "--title", "   "),
		bodiesBoth("search", "notebook", ""),
		bodiesBoth("search", "notebook"),
		bodiesBoth("search", "notebook", "a", "b"),
		bodiesJSON("notebook", "list"),
		bodiesJSON("notebook", "show", "search-hello"),
		bodiesBoth("search", "hello"),
	)})

	cases = append(cases, cliBodiesCase{Name: "notebook-delete", VolatileHistory: true, Files: vault, SnapshotVault: true, Steps: bodiesConcat(
		bodiesBoth("notebook", "new", "Doomed", "--description", "to delete"),
		bodiesBoth("notebook", "new", "Second"),
		bodiesBoth("notebook", "delete", "doomed"),
		bodiesBoth("notebook", "delete", "doomed"),
		bodiesBoth("notebook", "delete", "notebooks/second.md"),
		bodiesBoth("notebook", "delete", "missing"),
		bodiesBoth("notebook", "delete", ""),
		bodiesBoth("notebook", "delete"),
		bodiesBoth("notebook", "delete", "a", "b"),
		bodiesBoth("trash", "list"),
		bodiesJSON("notebook", "list"),
	)})

	return cases
}
