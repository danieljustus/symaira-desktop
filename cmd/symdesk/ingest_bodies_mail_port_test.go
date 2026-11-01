package main

// `mail status` and `mail fetch`, recorded for RUST-013 against the Go
// binary. The IMAP peer is always a loopback port nothing listens on, so the
// recorded fetch outcomes are the deterministic failure paths (secret
// resolution, connection refused); the successful IMAP exchange is covered by
// the symdesk-ingest mail contract tests against their own IMAP server
// (declared limit of this fixture).

func init() {
	cliIngestCaseBuilders = append(cliIngestCaseBuilders, cliIngestMailCases)
}

func mailAccountTOML(secret, extra string) string {
	return "[[imap_accounts]]\nhost = \"127.0.0.1\"\nport = $CLOSED_PORT\nusername = \"alice\"\npassword_secret = \"" + secret + "\"\n" + extra + "\n"
}

func cliIngestMailCases() []cliIngestCase {
	const cfg = "config/symingest/config.toml"
	both := func() []cliIngestStep {
		return ingestConcat(ingestBoth("mail", "status"), ingestBoth("mail", "fetch"))
	}
	return []cliIngestCase{
		{Name: "mail-status-fetch-unconfigured", SkipIndex: true, Steps: both()},
		{Name: "mail-status-fetch-empty-config", SkipIndex: true,
			HomeFiles: map[string]string{cfg: "ocr_lang = \"eng\"\n"}, Steps: both()},
		{Name: "mail-status-two-accounts", SkipIndex: true,
			HomeFiles: map[string]string{cfg: mailAccountTOML("env://A", "folder = \"Work\"") + mailAccountTOML("env://B", "")},
			Steps:     ingestBoth("mail", "status")},
		{Name: "mail-status-fetch-bad-config", SkipIndex: true,
			HomeFiles: map[string]string{cfg: "x = = 1\n"}, Steps: both()},
		{Name: "mail-fetch-refused", SnapshotVault: true, SnapshotHome: true, Canaries: []string{"mail-secret-canary-2"},
			Env:       map[string]string{"MAIL_TEST_PW": "mail-secret-canary-2"},
			HomeFiles: map[string]string{cfg: mailAccountTOML("env://MAIL_TEST_PW", "")},
			Steps:     both()},
		{Name: "mail-fetch-secret-unset", SkipIndex: true,
			HomeFiles: map[string]string{cfg: mailAccountTOML("env://MAIL_TEST_UNSET", "")}, Steps: both()},
		{Name: "mail-fetch-secret-bare", SkipIndex: true,
			Env:       map[string]string{"MAIL_TEST_BARE": "mail-secret-canary-3"},
			Canaries:  []string{"mail-secret-canary-3"},
			HomeFiles: map[string]string{cfg: mailAccountTOML("MAIL_TEST_BARE", "")}, Steps: both()},
		{Name: "mail-fetch-secret-symvault", SkipIndex: true,
			Env:       map[string]string{"PATH": "$ROOT/home/empty-bin"},
			HomeFiles: map[string]string{cfg: mailAccountTOML("symvault://mail/alice", "")}, Steps: both()},
		{Name: "mail-no-vault", NoVault: true, SkipIndex: true,
			HomeFiles: map[string]string{cfg: mailAccountTOML("env://A", "")}, Steps: both()},
	}
}
