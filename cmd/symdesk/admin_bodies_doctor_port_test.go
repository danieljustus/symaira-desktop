package main

import "strings"

// `doctor` (text and --json): the vault, contract/ASN, archive, mail-config,
// sidecar, retrieval, sibling-tool, conflict, AI and agent-results sections.
// The text report lists its sections in Go's random map order, so every case
// compares the lines of the report sorted (SortLines). Sibling tools are fake
// scripts below the isolated HOME (`bin/` and `.symaira/bin/`), found through
// SYMAIRA_BIN, the managed runtime directory and a controlled PATH, so the host
// toolchain never shows through.

func init() {
	adminBodiesCaseBuilders = append(adminBodiesCaseBuilders, adminDoctorCases)
}

const adminFakeToolScript = "#!/bin/sh\nprintf '%s\\n' '{\"schema_version\":1,\"version\":\"VERSION\"}'\n"

func adminFakeTool(version string) string {
	return strings.ReplaceAll(adminFakeToolScript, "VERSION", version)
}

func adminDoctorHome(extra map[string]string) map[string]string {
	home := cliErrorsHomeFiles()
	for name, content := range extra {
		home[name] = content
	}
	return home
}

func adminDoctorEnv(extra map[string]string) map[string]string {
	env := map[string]string{"PATH": "$ROOT/nopath", "SYMINGEST_ARCHIVE_PATH": ""}
	for name, value := range extra {
		env[name] = value
	}
	return env
}

func adminDoctorCases() []adminBodiesCase {
	clean := map[string]string{
		"a.md":       "---\ntitle: Alpha\ntags: [x]\n---\nhello alpha\n",
		"notes/b.md": "---\ntitle: Beta\nasn: 7\n---\nhello beta [[a]]\n",
	}
	archive := map[string]string{
		"a.md":                 "---\ntitle: Alpha\n---\nbody\n",
		"archive/real.pdf":     "%PDF-1.4 not parsed\n",
		"ok-rel.md":            "---\ntitle: Ok\narchive_path: archive/real.pdf\n---\nbody\n",
		"missing-rel.md":       "---\ntitle: Missing\narchive_path: archive/gone.pdf\n---\nbody\n",
		"escape.md":            "---\ntitle: Escape\narchive_path: ../outside.pdf\n---\nbody\n",
		"dir-rel.md":           "---\ntitle: Dir\narchive_path: archive\n---\nbody\n",
		"abs-missing.md":       "---\ntitle: Abs\narchive_path: /nonexistent/abs.pdf\n---\nbody\n",
		"abs-dir.md":           "---\ntitle: AbsDir\narchive_path: /\n---\nbody\n",
		"nonstring.md":         "---\ntitle: Num\narchive_path: 5\n---\nbody\n",
		"empty-path.md":        "---\ntitle: Empty\narchive_path: \"\"\n---\nbody\n",
		"note 2.md":            "---\ntitle: Dup\n---\nsync copy\n",
		"x conflicted copy.md": "---\ntitle: Copy\n---\nsync copy\n",
	}
	asn := map[string]string{
		"one.md":    "---\ntitle: One\nasn: 5\n---\nbody\n",
		"two.md":    "---\ntitle: Two\nasn: 5\n---\nbody\n",
		"three.md":  "---\ntitle: Three\nasn: 9\n---\nbody\n",
		"fine.md":   "---\ntitle: Fine\n---\nbody\n",
		"dups/a.md": "---\ntitle: DA\nasn: 11\n---\nbody\n",
		"dups/b.md": "---\ntitle: DB\nasn: 11\n---\nbody\n",
		"dups/c.md": "---\ntitle: DC\nasn: 11\n---\nbody\n",
	}
	// Written after the setup index, which refuses them.
	asnInvalid := map[string]string{
		"zero.md": "---\ntitle: Zero\nasn: 0\n---\nbody\n",
		"neg.md":  "---\ntitle: Neg\nasn: -3\n---\nbody\n",
		"text.md": "---\ntitle: Text\nasn: abc\n---\nbody\n",
	}
	tools := map[string]string{
		"bin/symmemory":          adminFakeTool("1.2.3"),
		".symaira/bin/symvault":  adminFakeTool("1.0.0"),
		"path/bin/symvault":      adminFakeTool("1.1.0"),
		"path/bin/symbrowse":     adminFakeTool("0.9.0"),
		".symaira/bin/symbrowse": adminFakeTool("0.9.0"),
	}
	return []adminBodiesCase{
		{Name: "doctor-clean", Files: clean, SortLines: true, Env: adminDoctorEnv(nil), Steps: adminBoth("doctor")},
		{Name: "doctor-archive-paths", Files: archive, SortLines: true, Env: adminDoctorEnv(nil), Steps: adminBoth("doctor")},
		{Name: "doctor-archive-env", Files: clean, SortLines: true,
			Env:   adminDoctorEnv(map[string]string{"SYMINGEST_ARCHIVE_PATH": "$ROOT/vault/archive"}),
			Steps: adminBoth("doctor")},
		{Name: "doctor-asn", Files: asn, PostIndexFiles: asnInvalid, SortLines: true, Env: adminDoctorEnv(nil), Steps: adminBoth("doctor")},
		{Name: "doctor-no-vault", NoVault: true, SkipIndex: true, SortLines: true, Env: adminDoctorEnv(nil), Steps: adminBoth("doctor")},
		{Name: "doctor-tools", Files: clean, SortLines: true, HomeFiles: adminDoctorHome(tools),
			Env:   adminDoctorEnv(map[string]string{"SYMAIRA_BIN": "$ROOT/home/bin", "PATH": "$ROOT/home/path/bin"}),
			Steps: adminBoth("doctor")},
		{Name: "doctor-retrieval-down", Files: clean, SortLines: true,
			HomeFiles: map[string]string{".config/symseek/config.toml": "ollama_url = \"http://127.0.0.1:$CLOSED_PORT/api/embeddings\"\nembedding_dim = 3\ntimeout_seconds = 1\nretry_count = 0\nmodel = \"fixture\"\n"},
			Env:       adminDoctorEnv(nil), Steps: adminBoth("doctor")},
		{Name: "doctor-ai", Files: clean, SortLines: true,
			Env:   adminDoctorEnv(map[string]string{"SYMDESK_LLM_PROVIDER": "anthropic", "SYMDESK_LLM_API_KEY": "env:DOCTOR_FAKE_KEY", "DOCTOR_FAKE_KEY": "k"}),
			Steps: adminBoth("doctor")},
		{Name: "doctor-ai-default-model", Files: clean, SortLines: true,
			Env:      adminDoctorEnv(map[string]string{"SYMDESK_LLM_PROVIDER": "anthropic", "SYMDESK_LLM_API_KEY": "plain-secret-value", "SYMDESK_LLM_MODEL": "claude-test"}),
			Canaries: []string{"plain-secret-value"},
			Steps:    adminBoth("doctor")},
		{Name: "doctor-arguments", Files: clean, SortLines: true, Env: adminDoctorEnv(nil), Steps: adminConcat(
			adminBoth("doctor", "extra"),
			adminBoth("doctor", "--bogus"),
		)},
	}
}
