package main

// `dataset list` (and the text form of `dataset describe`): handles below
// `datasets/`, the legacy policy migration, title ordering, diagnostics.

func init() {
	adminBodiesCaseBuilders = append(adminBodiesCaseBuilders, adminDatasetListCases)
}

func adminDatasetHandle(slug, title, extra string) string {
	return "---\ntype: dataset\ntitle: " + title + "\ndataset_id: " + slug + "\nsource: datasets/" + slug + "/source.csv\n" + extra + "---\n\n# Body of " + slug + "\n"
}

const adminDatasetPolicy = "sensitivity: internal\nretention_rule: default\n"

func adminDatasetListCases() []adminBodiesCase {
	basic := map[string]string{
		"datasets/orders.md": adminDatasetHandle("orders", "Orders", "custom_policy: keep-me\n"),
		"datasets/alpha.md": adminDatasetHandle("alpha", "alpha lower", adminDatasetPolicy+
			"created: \"2026-01-01T00:00:00Z\"\nidentity_field: id\nrefresh: make refresh\n"+
			"schema:\n  id:\n    type: number\n    label: Identifier\n  state:\n    type: select\n    options: [open, closed]\n    description: Order state\n    default: open\n"+
			"coverage:\n  from: \"2026-01-01\"\n  to: \"2026-06-30\"\n"+
			"provenance:\n  imported_at: \"2026-02-03T04:05:06Z\"\n  source_name: erp <export>\n  source_sha256: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n"),
		"datasets/zeta.md":     adminDatasetHandle("zeta", "Zeta", "sensitivity: restricted\nretention_rule: default\n"),
		"datasets/notes.txt":   "not a handle\n",
		"datasets/UPPER.MD":    adminDatasetHandle("upper", "Upper", adminDatasetPolicy),
		"datasets/sub/deep.md": adminDatasetHandle("deep", "Deep", adminDatasetPolicy),
		"datasets/beta-key.md": adminDatasetHandle("beta-key", "Beta ünï", "sensitivity: confidential\nretention_rule: default\n"),
		"other/plain.md":       "---\ntitle: Plain\n---\nbody\n",
	}
	broken := map[string]string{
		"datasets/good.md":       adminDatasetHandle("good", "Good", adminDatasetPolicy),
		"datasets/mismatch.md":   adminDatasetHandle("other", "Mismatch", adminDatasetPolicy),
		"datasets/half.md":       adminDatasetHandle("half", "Half", "sensitivity: internal\n"),
		"datasets/half2.md":      adminDatasetHandle("half2", "Half Two", "retention_rule: default\n"),
		"datasets/badpolicy.md":  adminDatasetHandle("badpolicy", "Bad", "sensitivity: ultra\nretention_rule: default\n"),
		"datasets/notdataset.md": "---\ntype: note\ntitle: Not\n---\nbody\n",
		"datasets/nofm.md":       "no frontmatter\n",
	}
	sync := adminBoth("dataset", "sync", "orders", "--rows", `[{"identity":"1","values":{"id":1,"name":"first"}},{"identity":"2","values":{"id":2,"name":"second"}}]`,
		"--title", "Orders", "--identity-field", "id", "--source-name", "feed", "--source-sha256", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"--imported-at", "2026-01-02T03:04:05Z", "--schema", `{"id":{"type":"number"},"name":{"type":"text","label":"Name"}}`)
	return []adminBodiesCase{
		{Name: "dataset-list-basic", Files: basic, SnapshotVault: true, Volatile: true, Steps: adminConcat(
			adminBoth("dataset", "list"),
			adminBoth("dataset", "describe", "alpha"),
			adminBoth("dataset", "describe", "orders"),
			adminBoth("dataset", "describe", "missing"),
			adminBoth("dataset", "list"),
		)},
		{Name: "dataset-list-synced", Files: map[string]string{"a.md": "---\ntitle: A\n---\nbody\n"}, SnapshotVault: true, Volatile: true, Steps: adminConcat(
			adminBoth("dataset", "list"),
			sync,
			adminBoth("dataset", "list"),
			adminBoth("dataset", "describe", "orders"),
		)},
		{Name: "dataset-list-diagnostics", Files: broken, SnapshotVault: true, Volatile: true, Steps: adminConcat(
			adminBoth("dataset", "list"),
		)},
		{Name: "dataset-list-empty", Files: map[string]string{"a.md": "---\ntitle: A\n---\nbody\n"}, SnapshotVault: true, Steps: adminConcat(
			adminBoth("dataset", "list"),
		)},
		{Name: "dataset-list-arguments", Files: map[string]string{"a.md": "---\ntitle: A\n---\nbody\n"}, Steps: adminConcat(
			adminBoth("dataset", "list", "extra"),
			adminBoth("dataset", "list", "--bogus"),
			adminBoth("dataset"),
		)},
		{Name: "dataset-list-without-vault", NoVault: true, SkipIndex: true, Steps: adminBoth("dataset", "list")},
		{Name: "dataset-datasets-is-file", Files: map[string]string{"datasets": "a file, not a directory\n"}, Steps: adminBoth("dataset", "list")},
	}
}
