package main

// `sidecar list` and `sidecar prune`, recorded for RUST-013 against a
// disposable XDG_DATA_HOME that holds crafted orphan, unidentified and broken
// sidecar directories next to the live sidecar of the case vault.

func init() {
	cliBodiesCaseBuilders = append(cliBodiesCaseBuilders, cliBodiesSidecarCases)
}

func cliBodiesSidecarCases() []cliBodiesCase {
	home := cliErrorsHomeFiles()
	home["data/symdesk/vaults/0000orphan0000001/sidecar.db"] = "not a database but a regular file"
	home["data/symdesk/vaults/0000orphan0000001/metadata.json"] = `{"vault_path":"/nonexistent-r013-vault/one","last_used":"2024-05-06T07:08:09.5Z"}`
	home["data/symdesk/vaults/0000orphan0000002/sidecar.db"] = "second orphan with more bytes in it"
	home["data/symdesk/vaults/0000orphan0000002/metadata.json"] = `{"vault_path":"/nonexistent-r013-vault/two","last_used":"2024-05-06T09:08:09+02:00"}`
	home["data/symdesk/vaults/0000orphan0000002/retrieval.db"] = "retrieval index of the second orphan"
	home["data/symdesk/vaults/0000nometa0000003/sidecar.db"] = "no metadata here"
	home["data/symdesk/vaults/0000badmeta000004/sidecar.db"] = "bad metadata"
	home["data/symdesk/vaults/0000badmeta000004/metadata.json"] = `{not json`
	home["data/symdesk/vaults/0000emptymeta00005/sidecar.db"] = "empty vault path"
	home["data/symdesk/vaults/0000emptymeta00005/metadata.json"] = `{"vault_path":"","last_used":"2024-01-01T00:00:00Z"}`
	home["data/symdesk/vaults/0000nolast0000006/sidecar.db"] = "no last used"
	home["data/symdesk/vaults/0000nolast0000006/metadata.json"] = `{"vault_path":"/nonexistent-r013-vault/nolast"}`
	home["data/symdesk/vaults/0000nodb000000007/metadata.json"] = `{"vault_path":"/nonexistent-r013-vault/nodb","last_used":"2024-01-01T00:00:00Z"}`
	home["data/symdesk/vaults/stray-file"] = "a file, not a directory"
	// A live sidecar: its vault path ("/") always exists. The size of a real
	// SQLite sidecar differs between hosts, so no real vault is indexed here.
	home["data/symdesk/vaults/0000live00000008/sidecar.db"] = "live sidecar bytes"
	home["data/symdesk/vaults/0000live00000008/metadata.json"] = `{"vault_path":"/","last_used":"2025-02-03T04:05:06.123456789Z"}`
	home["data/symdesk/sidecar.db"] = "legacy sidecar"
	home["data/symdesk/retrieval.db"] = "legacy retrieval index"

	vault := bodiesVault()
	var cases []cliBodiesCase

	cases = append(cases, cliBodiesCase{Name: "sidecar", VolatileHistory: true, SkipIndex: true, Files: vault, HomeFiles: home, Steps: bodiesConcat(
		bodiesBoth("sidecar", "list"),
		bodiesBoth("sidecar", "prune"),
		bodiesBoth("sidecar", "prune", "--confirm=false"),
		bodiesBoth("sidecar", "prune", "--confirm"),
		bodiesBoth("sidecar", "list"),
		bodiesBoth("sidecar", "prune", "--confirm"),
		bodiesBoth("sidecar", "list", "extra"),
		bodiesBoth("sidecar", "prune", "--confirm=maybe"),
		bodiesBoth("sidecar"),
	)})

	cases = append(cases, cliBodiesCase{Name: "sidecar-empty", VolatileHistory: true, SkipIndex: true, NoVault: true, Files: vault, Steps: bodiesConcat(
		bodiesBoth("sidecar", "list"),
		bodiesBoth("sidecar", "prune", "--confirm"),
	)})

	return cases
}
