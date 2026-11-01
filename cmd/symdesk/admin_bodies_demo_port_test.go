package main

// `demo init [dir] [--size small|large]` materialises the built-in demo vault
// below the working directory. SnapshotCwd records the whole generated tree
// (HashFiles: SHA-256 per file); the large vault is recorded as one digest.

func init() {
	adminBodiesCaseBuilders = append(adminBodiesCaseBuilders, adminDemoCases)
}

func adminDemoCases() []adminBodiesCase {
	return []adminBodiesCase{
		{Name: "demo-init-small", NoVault: true, SkipIndex: true, SnapshotCwd: true, DigestCwd: true, HashFiles: true, Steps: adminConcat(
			adminBoth("demo", "init"),
			adminBoth("demo", "init"),
			adminBoth("demo", "init", "other"),
			adminBoth("demo", "init", "other"),
			adminBoth("demo", "init", "nested/deeper/vault"),
			adminBoth("demo", "init", "clean/../tidy"),
			adminBoth("demo", "init", "$ROOT/cwd/absolute"),
			adminBoth("demo", "init", "--size", "small", "sized"),
			adminBoth("demo", "init", "--size=", "blank-size"),
			adminBoth("demo", "init", "--size", "SMALL", "upper-size"),
		)},
		{Name: "demo-init-text", NoVault: true, SkipIndex: true, SnapshotCwd: true, Steps: adminConcat(
			[]adminBodiesStep{{Args: []string{"demo", "init", "readable"}}},
		)},
		{Name: "demo-init-errors", NoVault: true, SkipIndex: true, CwdFiles: map[string]string{"afile": "x", "occupied/x": "y"}, CwdDirs: []string{"empty"}, SnapshotCwd: true, DigestCwd: true, HashFiles: true, Steps: adminConcat(
			adminBoth("demo", "init", "afile"),
			adminBoth("demo", "init", "afile/below"),
			adminBoth("demo", "init", "empty"),
			adminBoth("demo", "init", "empty"),
			adminBoth("demo", "init", "occupied"),
			adminBoth("demo", "init", "--size", "huge", "bad-size"),
			adminBoth("demo", "init", "--size", "", "x-blank"),
			adminBoth("demo", "init", "a", "b"),
			adminBoth("demo", "init", "/dev/null/below"),
			adminBoth("demo", "init", "--bogus"),
			adminBoth("demo"),
			adminBoth("demo", "bogus"),
			adminBoth("demo", "init", "--help"),
		)},
		{Name: "demo-init-large", NoVault: true, SkipIndex: true, SnapshotCwd: true, DigestCwd: true, HashFiles: true, Steps: adminConcat(
			[]adminBodiesStep{{Args: []string{"--json", "demo", "init", "--size", "large", "big"}}},
			[]adminBodiesStep{{Args: []string{"--json", "demo", "init", "--size", "LARGE", "big"}}},
			[]adminBodiesStep{{Args: []string{"demo", "init", "--size", "Large", "big2"}}},
		)},
	}
}
