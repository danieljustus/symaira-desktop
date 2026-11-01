package main

// `asset store` and `export profiles`, recorded for RUST-013.

func init() {
	cliBodiesCaseBuilders = append(cliBodiesCaseBuilders, cliBodiesAssetCases)
}

func cliBodiesAssetCases() []cliBodiesCase {
	vault := bodiesVault()
	vault["pics/Photo One.PNG"] = "PNG fake bytes\n"
	var cases []cliBodiesCase

	cases = append(cases, cliBodiesCase{Name: "asset-store", VolatileHistory: true, Files: vault, SnapshotVault: true, Steps: bodiesConcat(
		bodiesBoth("asset", "store", "$ROOT/vault/pics/Photo One.PNG"),
		bodiesBoth("asset", "store", "$ROOT/vault/pics/Photo One.PNG"),
		bodiesBoth("asset", "store", "$ROOT/vault/pics/Photo One.PNG", "--name", "renamed.jpg"),
		bodiesBoth("asset", "store", "$ROOT/vault/a.md", "--name", "docs/with:odd\\chars.txt"),
		bodiesBoth("asset", "store", "$ROOT/vault/a.md", "--name", "   "),
		bodiesBoth("asset", "store", "$ROOT/vault/a.md", "--name", "noext"),
		bodiesBoth("asset", "store", "$ROOT/vault/a.md", "--name", ".hidden"),
		[]cliBodiesStep{
			bodiesStdin("stdin bytes", "asset", "store", "-", "--name", "from-stdin.bin"),
			bodiesStdin("stdin bytes", "--json", "asset", "store", "-", "--name", "from-stdin.bin"),
			// Without --name the stored file is named after the wall clock,
			// which a recording cannot pin; only the named forms are compared.
			bodiesStdin("", "asset", "store", "-", "--name", "empty.dat"),
		},
		bodiesBoth("asset", "store", "$ROOT/vault/missing.bin"),
		bodiesBoth("asset", "store", "$ROOT/vault"),
		bodiesBoth("asset", "store"),
		bodiesBoth("asset", "store", "a", "b"),
	)})

	cases = append(cases, cliBodiesCase{Name: "export-profiles", NoVault: true, SkipIndex: true, Steps: bodiesConcat(
		bodiesBoth("export", "profiles"),
		bodiesBoth("export", "profiles", "extra"),
	)})

	return cases
}
