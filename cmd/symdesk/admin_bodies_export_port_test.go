package main

// `export --note`: HTML files in the working directory, the format/argument
// errors, the dataset export policy and the PDF path without a typesetting
// engine (PATH is an empty case directory, so the host toolchain never shows
// through). `export --view` and the csv format of a view belong to the saved
// view engine and are not recorded here.

func init() {
	adminBodiesCaseBuilders = append(adminBodiesCaseBuilders, adminExportCases)
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
