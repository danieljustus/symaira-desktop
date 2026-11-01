package main

// `export --note`: HTML files in the working directory, the format/argument
// errors, the dataset export policy and the PDF path without a typesetting
// engine (PATH is an empty case directory, so the host toolchain never shows
// through). Views are saved through `views save` and exported as HTML, CSV
// and (without an engine) PDF.

func init() {
	adminBodiesCaseBuilders = append(adminBodiesCaseBuilders, adminExportCases, adminExportViewCases)
}

func adminExportCases() []adminBodiesCase {
	doc := func(title, body string) string {
		return "---\ntitle: " + title + "\n---\n" + body
	}
	notes := map[string]string{
		"a.md":              doc("Alpha", "Intro with [[b]] and ![[notes/embedded]] and a <b>tag</b> & ampersand.\n\n- one\n- two\n\n| h1 | h2 |\n|----|----|\n| x | y |\n"),
		"notes/b.md":        doc("Beta", "Beta body with `code` and **bold**.\n"),
		"notes/embedded.md": doc("Embedded", "Embedded text.\n"),
		"no-title.md":       "no frontmatter at all\n",
		"UPPER.MD":          doc("Upper", "upper\n"),
	}
	datasets := map[string]string{
		"datasets/pub.md":         adminDatasetHandle("pub", "Public", "sensitivity: public\nretention_rule: default\n"),
		"datasets/internal.md":    adminDatasetHandle("internal", "Internal", "sensitivity: internal\nretention_rule: default\n"),
		"datasets/secret.md":      adminDatasetHandle("secret", "Secret", "sensitivity: restricted\nretention_rule: default\n"),
		"datasets/mismatch.md":    adminDatasetHandle("other", "Mismatch", adminDatasetPolicy),
		"datasets/Bad Slug.md":    adminDatasetHandle("bad-slug", "Bad Slug", adminDatasetPolicy),
		"datasets/sub/inner.md":   doc("Inner", "plain note below datasets\n"),
		"datasets/secret/raw.csv": "a,b\n1,2\n",
	}
	cwd := []string{"sub"}
	return []adminBodiesCase{
		{Name: "export-html", Files: notes, CwdDirs: cwd, SnapshotCwd: true, Steps: adminConcat(
			adminBoth("export", "--note", "a.md", "--format", "html"),
			adminBoth("export", "--note", "notes/b.md", "--format", "HTML", "--output", "beta.html"),
			adminBoth("export", "--note", "./notes/embedded.md", "--format", "html", "--output", "sub/../sub/out.html"),
			adminBoth("export", "--note", "no-title.md", "--format", "html", "--output=plain.html"),
			adminBoth("export", "--note", "UPPER.MD", "--format", "html"),
			adminBoth("export", "--note", "a.md", "--format", "html"),
		)},
		{Name: "export-errors", Files: notes, PostIndexFiles: map[string]string{"bad-asn.md": "---\ntitle: Bad\nasn: 0\n---\nbody\n"}, SnapshotCwd: true, Steps: adminConcat(
			adminBoth("export", "--note", "a.md", "--format", "docx"),
			adminBoth("export", "--note", "a.md", "--format", "csv"),
			adminBoth("export", "--format", "csv"),
			adminBoth("export", "--format", "html"),
			adminBoth("export", "--note", "missing.md", "--format", "html"),
			adminBoth("export", "--note", "../escape.md", "--format", "html"),
			adminBoth("export", "--note", "bad-asn.md", "--format", "html"),
			adminBoth("export", "--note", "a.md", "--format", "html", "--output", "missing-dir/out.html"),
			adminBoth("export", "--note"),
			adminBoth("export", "--bogus"),
			adminBoth("export", "--note", "a.md", "--format"),
			adminBoth("export", "extra", "--note", "a.md", "--format", "html"),
		)},
		{Name: "export-dataset-policy", Files: datasets, SnapshotCwd: true, Steps: adminConcat(
			adminBoth("export", "--note", "datasets/pub.md", "--format", "html"),
			adminBoth("export", "--note", "datasets/internal.md", "--format", "html", "--output", "internal.html"),
			adminBoth("export", "--note", "datasets/secret.md", "--format", "html"),
			adminBoth("export", "--note", "datasets/secret", "--format", "html"),
			adminBoth("export", "--note", "datasets/secret/raw.csv", "--format", "html"),
			adminBoth("export", "--note", "datasets/mismatch.md", "--format", "html"),
			adminBoth("export", "--note", "datasets/Bad Slug.md", "--format", "html"),
			adminBoth("export", "--note", "datasets/", "--format", "html"),
			adminBoth("export", "--note", "datasets/ghost.md", "--format", "html"),
			adminBoth("export", "--note", "./datasets/sub/inner.md", "--format", "html"),
			adminBoth("export", "--note", " datasets/pub.md ", "--format", "html"),
		)},
		{Name: "export-dataset-policy-config", Files: datasets, SnapshotCwd: true,
			Env: map[string]string{"SYMDESK_DATASET_EXPORT_MAX_SENSITIVITY": "RESTRICTED"}, Steps: adminConcat(
				adminBoth("export", "--note", "datasets/secret.md", "--format", "html", "--output", "secret.html"),
			)},
		{Name: "export-dataset-policy-invalid-config", Files: datasets, SnapshotCwd: true,
			Env: map[string]string{"SYMDESK_DATASET_EXPORT_MAX_SENSITIVITY": "top-secret"}, Steps: adminConcat(
				adminBoth("export", "--note", "datasets/pub.md", "--format", "html"),
			)},
		{Name: "export-dataset-policy-public-only", Files: datasets, SnapshotCwd: true,
			Env: map[string]string{"SYMDESK_DATASET_EXPORT_MAX_SENSITIVITY": "public"}, Steps: adminConcat(
				adminBoth("export", "--note", "datasets/pub.md", "--format", "html"),
				adminBoth("export", "--note", "datasets/internal.md", "--format", "html"),
			)},
		{Name: "export-pdf-no-engine", Files: notes, SnapshotCwd: true, Env: map[string]string{"PATH": "$ROOT/nopath"}, Steps: adminConcat(
			adminBoth("export", "--note", "a.md"),
			adminBoth("export", "--note", "a.md", "--format", "pdf", "--profile", "brief", "--output", "a.pdf"),
			adminBoth("export", "--note", "missing.md"),
		)},
		{Name: "export-without-vault", NoVault: true, SkipIndex: true, Steps: adminBoth("export", "--note", "a.md", "--format", "html")},
		{Name: "export-profiles", Steps: adminBoth("export", "profiles")},
	}
}

func adminExportViewCases() []adminBodiesCase {
	invoices := map[string]string{
		"a.md":                 "---\ntitle: Alpha\n---\nbody\n",
		"invoices/one.md":      "---\ntitle: One\nstatus: open\namount: 10\n---\nfirst\n",
		"invoices/two.md":      "---\ntitle: Two\nstatus: paid\namount: 20\n---\nsecond\n",
		"invoices/three.md":    "---\ntitle: Three, with comma\nstatus: open\namount: 5.5\n---\nthird\n",
		"invoices/sub/four.md": "---\ntitle: Four\nstatus: \"quo\\\"te\"\n---\nfourth\n",
	}
	const (
		viewAll  = `{"id":"all","name":"All notes","columns":["title","status"]}`
		viewOpen = `{"id":"open","name":"Open invoices","source":"invoices/","filters":[{"key":"status","operator":"equals","value":"open"}],"columns":["title","amount"]}`
		viewNone = `{"id":"none","name":"No match","source":"invoices/","filters":[{"key":"status","operator":"equals","value":"nothing"}]}`
		viewDSet = `{"id":"dset","name":"Orders","source":"dataset:orders"}`
		viewBad  = `{"id":"baddir","name":"Bad dir","source":"nowhere/"}`
	)
	save := func(spec string) []adminBodiesStep { return adminBoth("views", "save", spec) }
	sync := adminBoth("dataset", "sync", "orders", "--rows", `[{"identity":"1","values":{"id":1,"name":"first"}},{"identity":"2","values":{"id":2,"name":"second"}}]`,
		"--title", "Orders", "--identity-field", "id", "--source-name", "feed", "--source-sha256", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"--imported-at", "2026-01-02T03:04:05Z", "--schema", `{"id":{"type":"number"},"name":{"type":"text","label":"Name"}}`)
	return []adminBodiesCase{
		{Name: "export-view-html-csv", Files: invoices, CwdDirs: []string{"sub"}, SnapshotCwd: true, Volatile: true, Steps: adminConcat(
			save(viewAll), save(viewOpen), save(viewNone), save(viewBad),
			adminBoth("export", "--view", "all", "--format", "html"),
			adminBoth("export", "--view", "open", "--format", "html", "--output", "open.html"),
			adminBoth("export", "--view", "none", "--format", "html"),
			adminBoth("export", "--view", "all", "--format", "csv"),
			adminBoth("export", "--view", "open", "--format", "csv", "--output", "sub/open.csv"),
			adminBoth("export", "--view", "none", "--format", "csv"),
			adminBoth("export", "--view", "ghost", "--format", "html"),
			adminBoth("export", "--view", "ghost", "--format", "csv"),
			adminBoth("export", "--view", "baddir", "--format", "html"),
			adminBoth("export", "--view", "baddir", "--format", "csv"),
			adminBoth("export", "--view", "open", "--format", "csv", "--output", "missing-dir/open.csv"),
			adminBoth("export", "--note", "a.md", "--view", "all", "--format", "html"),
			adminBoth("export", "--view"),
		)},
		{Name: "export-view-dataset-policy", Files: map[string]string{"a.md": "---\ntitle: A\n---\nbody\n"}, SnapshotCwd: true, Volatile: true, Steps: adminConcat(
			sync, save(viewDSet),
			adminBoth("export", "--view", "dset", "--format", "html"),
			adminBoth("export", "--view", "dset", "--format", "csv"),
		)},
		{Name: "export-view-dataset-policy-widened", Files: map[string]string{"a.md": "---\ntitle: A\n---\nbody\n"}, SnapshotCwd: true, Volatile: true,
			Env: map[string]string{"SYMDESK_DATASET_EXPORT_MAX_SENSITIVITY": "restricted"}, Steps: adminConcat(
				sync, save(viewDSet),
				adminBoth("export", "--view", "dset", "--format", "html"),
				adminBoth("export", "--view", "dset", "--format", "csv", "--output", "orders.csv"),
			)},
		{Name: "export-view-pdf-no-engine", Files: invoices, SnapshotCwd: true, Env: map[string]string{"PATH": "$ROOT/nopath"}, Steps: adminConcat(
			save(viewAll),
			adminBoth("export", "--view", "all", "--format", "pdf"),
			adminBoth("export", "--view", "ghost"),
		)},
	}
}
