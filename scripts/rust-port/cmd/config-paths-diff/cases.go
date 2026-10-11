package main

import portdiff "github.com/danieljustus/symaira-desktop/scripts/rust-port/internal/diff"

// configPathCases is the complete named, synthetic process corpus for the
// current config paths contract. Native filesystem cases are labeled explicitly.
func configPathCases() []namedCase {
	file := func(base, path, content string) portdiff.SetupFile {
		return portdiff.SetupFile{Base: base, Path: path, Content: content}
	}
	directory := func(base, path string) portdiff.SetupFile {
		return portdiff.SetupFile{Base: base, Path: path, Kind: "directory", Mode: 0o700}
	}
	symlink := func(base, path, target string) portdiff.SetupFile {
		return portdiff.SetupFile{Base: base, Path: path, Kind: "symlink", LinkTarget: target}
	}
	addFiles := func(entry namedCase, files ...portdiff.SetupFile) namedCase {
		entry.Input.Setup = files
		return entry
	}
	withEnv := func(entry namedCase, env map[string]string) namedCase {
		entry.Input.SandboxEnv = env
		return entry
	}
	withRawEnv := func(entry namedCase, env map[string]string) namedCase {
		entry.Input.SandboxEnvBase64 = env
		return entry
	}
	withoutHome := func(entry namedCase) namedCase {
		entry.Input.UnsetSandboxEnv = []string{"HOME", "USERPROFILE"}
		return entry
	}
	makeCase := func(id, category string, args ...string) namedCase {
		return namedCase{
			ID:       id,
			Category: category,
			Platform: "any",
			Input: portdiff.Case{
				ID:           id,
				Args:         args,
				TimeoutMS:    30_000,
				CompareFiles: true,
			},
		}
	}

	cases := []namedCase{
		makeCase("default-no-vault-text", "defaults", "config", "paths"),
		makeCase("effective-vault-flag-json", "vault-precedence", "--vault=${WORKSPACE}/vault", "config", "paths", "--output=json"),
		addFiles(makeCase("root-config-vault", "vault-precedence", "config", "paths", "--output=json"),
			file("home", ".config/symdesk/config.toml", "vault = \"${WORKSPACE}/root-vault\"\n")),
		withEnv(addFiles(makeCase("environment-vault-overrides-root-config", "vault-precedence", "config", "paths"),
			file("home", ".config/symdesk/config.toml", "vault = \"${WORKSPACE}/root-vault\"\n")),
			map[string]string{"SYMDESK_VAULT": "${WORKSPACE}/environment-vault"}),
		withEnv(makeCase("flag-vault-overrides-environment", "vault-precedence", "--vault=${WORKSPACE}/flag-vault", "config", "paths"),
			map[string]string{"SYMDESK_VAULT": "${WORKSPACE}/environment-vault"}),
		withEnv(makeCase("empty-vault-flag-falls-back-to-environment", "vault-precedence", "--vault=", "config", "paths"),
			map[string]string{"SYMDESK_VAULT": "${WORKSPACE}/environment-vault"}),
		addFiles(makeCase("symingest-vault-fallback", "ingest-config", "config", "paths", "--output=json"),
			file("home", ".config/symingest/config.toml", "vault = \"${WORKSPACE}/ingest-vault\"\n")),
		makeCase("extra-positionals-accepted", "flags", "config", "paths", "first", "second"),
		makeCase("double-dash-positionals-accepted", "flags", "config", "paths", "--", "--output=json", "extra"),
		makeCase("json-flag-before-command", "flags", "--json", "config", "paths"),
		makeCase("json-flag-between-command", "flags", "config", "--json", "paths"),
		makeCase("json-flag-after-command-with-positionals", "flags", "config", "paths", "extra", "--json"),
		makeCase("output-split-before-command", "flags", "--output", "json", "config", "paths"),
		makeCase("output-split-between-command", "flags", "config", "--output", "json", "paths"),
		makeCase("output-yaml-split-after-command", "flags", "config", "paths", "--output", "yaml", "extra"),
		makeCase("output-repeated-last-wins", "flags", "--output=text", "config", "paths", "--output=json"),
		makeCase("vault-repeated-last-wins", "flags", "--vault=${WORKSPACE}/first-vault", "config", "paths", "--vault", "${WORKSPACE}/last-vault"),
		makeCase("empty-output-falls-back-to-json-flag", "flags", "--json", "--output=", "config", "paths"),
		makeCase("invalid-output-valid-config", "flags", "--output=invalid", "config", "paths"),
		makeCase("invalid-output-json-flag-still-stderr", "flags", "--json", "--output=invalid", "config", "paths"),
		addFiles(makeCase("root-config-directory-before-invalid-output", "ordered-errors", "--output=invalid", "config", "paths"),
			directory("home", ".config/symdesk/config.toml")),
		addFiles(makeCase("root-malformed-toml-before-invalid-output", "ordered-errors", "--output=invalid", "config", "paths"),
			file("home", ".config/symdesk/config.toml", "vault = [\n")),
		withEnv(makeCase("raw-vault-relative-padded-accepted", "path-overrides", "--vault", "  relative/../relative vault ", "config", "paths", "--output=json"),
			map[string]string{"XDG_DATA_HOME": "${WORKSPACE}/xdg-data"}),
		withEnv(makeCase("sidecar-raw-padded-relative-override", "path-overrides", "config", "paths"),
			map[string]string{"SYMDESK_SIDECAR": "  relative/../sidecar override.db  "}),
		withEnv(makeCase("sidecar-blank-override-falls-back", "path-overrides", "config", "paths"),
			map[string]string{"SYMDESK_SIDECAR": "   \t "}),
		addFiles(makeCase("retrieval-padded-relative-index-path", "path-overrides", "config", "paths"),
			file("home", ".config/symseek/config.toml", "index_path = \"  relative/../retrieval index.db  \"\n")),
		addFiles(makeCase("ingest-padded-relative-db-and-archive", "path-overrides", "config", "paths"),
			file("home", ".config/symingest/config.toml", "db_path = \"  relative/../ingest index.db  \"\narchive_path = \"  relative/../ingest archive  \"\n")),
		withEnv(makeCase("contacts-padded-relative-data-home", "path-overrides", "config", "paths"),
			map[string]string{"SYMRELATE_DATA_HOME": "  relative/../contacts data  "}),
		withEnv(makeCase("lexical-xdg-paths", "path-overrides", "config", "paths", "--output=json"),
			map[string]string{
				"XDG_DATA_HOME":   "${WORKSPACE}/xdg-data/../data",
				"XDG_CONFIG_HOME": "${WORKSPACE}/xdg-config/./nested/..",
				"XDG_CACHE_HOME":  "${WORKSPACE}/xdg-cache/../cache",
			}),
		withEnv(makeCase("json-html-and-unicode-path-escaping", "output", "config", "paths", "--output=json"),
			map[string]string{"XDG_DATA_HOME": "${WORKSPACE}/data<&>\u2028\u2029"}),
		addFiles(makeCase("legacy-file-fallbacks", "legacy-paths", "config", "paths", "--output=json"),
			file("home", ".local/share/symingest/symingest.db", "legacy-ingest"),
			file("home", ".local/share/symrelate/symrelate.db", "legacy-contacts"),
			file("home", ".local/share/symaira-seek/symseek.db", "legacy-retrieval")),
		addFiles(makeCase("unified-primary-directories-win", "legacy-paths", "config", "paths", "--output=json"),
			directory("home", ".local/share/symdesk/symingest.db"),
			directory("home", ".local/share/symdesk/symrelate.db"),
			directory("home", ".local/share/symdesk/retrieval.db")),
		addFiles(makeCase("unified-primary-symlinks-win", "native-filesystem", "config", "paths", "--output=json"),
			file("home", ".local/share/symdesk/target.db", "target"),
			symlink("home", ".local/share/symdesk/symingest.db", "target.db"),
			symlink("home", ".local/share/symdesk/symrelate.db", "target.db"),
			symlink("home", ".local/share/symdesk/retrieval.db", "target.db")),
		addFiles(makeCase("vault-symlink-alias-hash", "vault-aliases", "--vault=${WORKSPACE}/alias", "config", "paths", "--output=json"),
			file("workspace", "real-vault/marker.md", "# alias target\n"),
			symlink("workspace", "alias", "real-vault")),
		withEnv(addFiles(makeCase("temporary-root-vault", "vault-aliases", "--vault=${TMPDIR}/vault", "config", "paths", "--output=json"),
			file("sandbox", "tmp/vault/marker.md", "# temporary vault\n")), map[string]string{"XDG_DATA_HOME": ""}),
		withEnv(addFiles(makeCase("symingest-global-project-env-precedence", "ingest-config", "config", "paths", "--output=json"),
			file("home", ".config/symingest/config.toml", "vault = \"${WORKSPACE}/global-vault\"\ndb_path = \"${WORKSPACE}/global.db\"\narchive_path = \"${WORKSPACE}/global-archive\"\n"),
			file("workspace", ".symingest.toml", "vault = \"${WORKSPACE}/project-vault\"\ndb_path = \"${WORKSPACE}/project.db\"\narchive_path = \"${WORKSPACE}/project-archive\"\n")),
			map[string]string{
				"SYMINGEST_VAULT":        "${WORKSPACE}/environment-vault",
				"SYMINGEST_DB_PATH":      "${WORKSPACE}/environment.db",
				"SYMINGEST_ARCHIVE_PATH": "${WORKSPACE}/environment-archive",
			}),
		withEnv(addFiles(makeCase("symingest-empty-env-values-override-config", "ingest-config", "--vault=${WORKSPACE}/root-vault", "config", "paths", "--output=json"),
			file("home", ".config/symingest/config.toml", "vault = \"${WORKSPACE}/global-vault\"\ndb_path = \"${WORKSPACE}/global.db\"\narchive_path = \"${WORKSPACE}/global-archive\"\n")),
			map[string]string{"SYMINGEST_VAULT": "", "SYMINGEST_DB_PATH": "", "SYMINGEST_ARCHIVE_PATH": ""}),
		addFiles(makeCase("symingest-unused-string-wrong-type-global", "ingest-validation", "config", "paths"),
			file("home", ".config/symingest/config.toml", "ocr_lang = 7\n")),
		addFiles(makeCase("symingest-unused-bool-string-coercion-project", "ingest-validation", "config", "paths"),
			file("workspace", ".symingest.toml", "symseek_enabled = \"true\"\n")),
		addFiles(makeCase("symingest-unused-bool-invalid-string-project", "ingest-validation", "config", "paths"),
			file("workspace", ".symingest.toml", "symseek_enabled = \"affirmative\"\n")),
		addFiles(makeCase("symingest-imap-array-wrong-unused-struct", "ingest-validation", "config", "paths"),
			file("home", ".config/symingest/config.toml", "[[imap_accounts]]\nhost = \"mail.example\"\n")),
		withEnv(makeCase("symingest-invalid-bool-environment", "ingest-validation", "config", "paths"),
			map[string]string{"SYMINGEST_SYMSEEK_ENABLED": "maybe"}),
		withEnv(makeCase("symingest-empty-imap-environment", "ingest-validation", "config", "paths"),
			map[string]string{"SYMINGEST_IMAP_ACCOUNTS": ""}),
		withEnv(makeCase("symingest-imap-env-struct-conversion", "ingest-validation", "config", "paths"),
			map[string]string{"SYMINGEST_IMAP_ACCOUNTS": "host=mail.example"}),
		withoutHome(withEnv(makeCase("no-home-preflight-error-not-waived-by-overrides", "ordered-errors", "--vault=${WORKSPACE}/vault", "config", "paths"),
			map[string]string{"XDG_DATA_HOME": "", "SYMDESK_SIDECAR": "${WORKSPACE}/override.db"})),
		withoutHome(withEnv(makeCase("no-home-xdg-fails-at-first-retrieval-preflight", "ordered-errors", "config", "paths"),
			map[string]string{"XDG_DATA_HOME": "${WORKSPACE}/xdg-data"})),
		addFiles(makeCase("retrieval-json-cold-migration", "retrieval-migration", "config", "paths", "--output=json"),
			file("home", ".config/symseek/config.json", "{\"index_path\":\"${WORKSPACE}/legacy-index.db\"}")),
		addFiles(makeCase("retrieval-toml-precedes-json", "retrieval-migration", "config", "paths"),
			file("home", ".config/symseek/config.toml", "index_path = \"${WORKSPACE}/toml-index.db\"\n"),
			file("home", ".config/symseek/config.json", "{\"index_path\":\"${WORKSPACE}/json-index.db\"}")),
		addFiles(makeCase("retrieval-invalid-json-is-retained", "retrieval-migration", "config", "paths"),
			file("home", ".config/symseek/config.json", "{invalid json")),
		addFiles(makeCase("retrieval-json-wrong-field-type-is-retained", "retrieval-migration", "config", "paths"),
			file("home", ".config/symseek/config.json", "{\"index_path\":false}")),
		addFiles(makeCase("retrieval-migration-write-failure-retains-json", "native-filesystem-error", "config", "paths"),
			file("home", ".config/symseek/config.json", "{\"index_path\":\"${WORKSPACE}/legacy-index.db\"}"),
			symlink("home", ".config/symseek/config.toml", "missing/target.toml")),
		addFiles(makeCase("retrieval-migration-unix-private-mode", "native-filesystem-mode", "config", "paths"),
			file("home", ".config/symseek/config.json", "{\"index_path\":\"${WORKSPACE}/legacy-index.db\"}")),
		addFiles(makeCase("retrieval-migration-then-ingest-error-retains-toml", "ordered-side-effects", "config", "paths"),
			file("home", ".config/symseek/config.json", "{\"index_path\":\"${WORKSPACE}/legacy-index.db\"}"),
			file("workspace", ".symingest.toml", "ocr_lang = 7\n")),
		makeCase("vault-leading-parent-ingest-archive-text", "path-overrides", "--vault=../../vault", "config", "paths"),
		makeCase("vault-leading-parent-ingest-archive-json", "path-overrides", "--vault=../../vault", "config", "paths", "--output=json"),
		addFiles(makeCase("retrieval-json-null-scalar-fields", "retrieval-migration", "config", "paths", "--output=json"),
			file("home", ".config/symseek/config.json", `{"index_path":null,"embedding_dim":null,"vector_exact_rerank":null}`)),
		addFiles(makeCase("retrieval-json-null-top-level", "retrieval-migration", "config", "paths", "--output=json"),
			file("home", ".config/symseek/config.json", `null`)),
		addFiles(makeCase("retrieval-json-duplicate-null-preserves-earlier-scalars", "retrieval-migration", "config", "paths", "--output=json"),
			file("home", ".config/symseek/config.json", `{"index_path":"${WORKSPACE}/first.db","index_path":null,"embedding_dim":21,"embedding_dim":null,"vector_exact_rerank":true,"vector_exact_rerank":null}`)),
		addFiles(makeCase("retrieval-json-duplicate-null-then-non-null-last-wins", "retrieval-migration", "config", "paths", "--output=json"),
			file("home", ".config/symseek/config.json", `{"index_path":null,"index_path":"${WORKSPACE}/last.db","embedding_dim":null,"embedding_dim":17,"vector_exact_rerank":null,"vector_exact_rerank":true}`)),
		withEnv(makeCase("symingest-invalid-bool-control-text", "diagnostic-quoting", "config", "paths"),
			map[string]string{"SYMINGEST_SYMSEEK_ENABLED": "\x01"}),
		withEnv(makeCase("symingest-invalid-bool-unicode-json", "diagnostic-quoting", "config", "paths", "--output=json"),
			map[string]string{"SYMINGEST_SYMSEEK_ENABLED": "invalidé\u2028"}),
		addFiles(makeCase("symingest-invalid-bool-toml-control-text", "diagnostic-quoting", "config", "paths"),
			file("workspace", ".symingest.toml", `symseek_enabled = "\u0001"`)),
		addFiles(makeCase("symingest-invalid-bool-toml-unicode-json", "diagnostic-quoting", "config", "paths", "--output=json"),
			file("workspace", ".symingest.toml", `symseek_enabled = "affirmativeé\u2028"`)),
		withRawEnv(makeCase("unrelated-invalid-utf8-environment-does-not-panic", "robustness", "config", "paths"),
			map[string]string{"UNRELATED_BINARY_ENV": "/w=="}),
	}

	for _, name := range []string{
		"XDG_UNUSED_BINARY", "SYMDESK_UNUSED_BINARY", "SYMINGEST_UNUSED_BINARY",
		"SYMRELATE_UNUSED_BINARY", "SYMSEEK_UNUSED_BINARY", "OLLAMA_UNUSED_BINARY",
	} {
		entry := withRawEnv(makeCase("unrelated-invalid-utf8-"+name, "robustness", "config", "paths"),
			map[string]string{name: "/w=="})
		entry.Platform = "unix"
		cases = append(cases, entry)
	}

	for index := range cases {
		if cases[index].ID == "unified-primary-symlinks-win" ||
			cases[index].ID == "vault-symlink-alias-hash" ||
			cases[index].ID == "retrieval-migration-write-failure-retains-json" ||
			cases[index].ID == "retrieval-migration-unix-private-mode" ||
			cases[index].ID == "unrelated-invalid-utf8-environment-does-not-panic" {
			cases[index].Platform = "unix"
		}
	}
	return cases
}
