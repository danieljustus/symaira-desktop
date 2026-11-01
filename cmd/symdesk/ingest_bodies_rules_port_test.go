package main

// `rules *` (classification rules in the symingest document store) and
// `mail rules *` (the IMAP account section of the symingest configuration),
// recorded for RUST-013 against the Go binary.

func init() {
	cliIngestCaseBuilders = append(cliIngestCaseBuilders, cliIngestRulesCases, cliIngestMailRulesCases)
}

func cliIngestRulesCases() []cliIngestCase {
	steps := ingestConcat(
		ingestBoth("rules", "list"),
		ingestBoth("rules", "add", "Invoice", "tag", "billing"),
		ingestBoth("rules", "add", "  Strom ", "category", "energy"),
		ingestBoth("rules", "add", "Müller", "correspondent", "Mueller GmbH"),
		ingestBoth("rules", "add", "Rechnung", "document_type", "invoice"),
		ingestBoth("rules", "list"),
		// validation failures
		ingestBoth("rules", "add", "", "tag", "x"),
		ingestBoth("rules", "add", "p", "colour", "x"),
		ingestBoth("rules", "add", "p", "tag", ""),
		ingestBoth("rules", "add", "p", "", "v"),
		ingestBoth("rules", "add", "p", "TAG", "v"),
		// update
		ingestBoth("rules", "update", "1", "Invoices", "tag", "billing2"),
		ingestBoth("rules", "update", "2", "Strom", "category", "energy"),
		ingestBoth("rules", "update", "99", "p", "tag", "v"),
		ingestBoth("rules", "update", "1", "", "tag", "v"),
		ingestBoth("rules", "update", "1", "p", "nope", "v"),
		ingestBoth("rules", "update", "abc", "p", "tag", "v"),
		ingestBoth("rules", "update", "1.5", "p", "tag", "v"),
		ingestBoth("rules", "update", "-1", "p", "tag", "v"),
		ingestBoth("rules", "update", "9223372036854775808", "p", "tag", "v"),
		ingestBoth("rules", "list"),
		// test
		ingestBoth("rules", "test", "an INVOICES arrived from müller"),
		ingestBoth("rules", "test", "RECHNUNG und strom"),
		ingestBoth("rules", "test", "nothing matches"),
		ingestBoth("rules", "test", ""),
		// delete
		ingestBoth("rules", "delete", "2"),
		ingestBoth("rules", "delete", "2"),
		ingestBoth("rules", "delete", "abc"),
		ingestBoth("rules", "delete", "0"),
		ingestBoth("rules", "list"),
		// dry-run on an empty document store
		ingestBoth("rules", "dry-run", "invoice", "tag", "billing"),
		ingestBoth("rules", "dry-run", "", "tag", "billing"),
		ingestBoth("rules", "dry-run", "x", "bad", "billing"),
		ingestBoth("rules", "dry-run", "x", "tag", ""),
		// the dry-run never persists
		ingestBoth("rules", "list"),
	)
	return []cliIngestCase{
		{Name: "rules-crud", SkipIndex: true, Steps: steps},
		{Name: "rules-no-vault-flag", NoVault: true, SkipIndex: true, Steps: ingestConcat(
			ingestBoth("rules", "list"),
			ingestBoth("rules", "add", "a", "tag", "b"),
			ingestBoth("rules", "dry-run", "a", "tag", "b"),
		)},
		{Name: "rules-store-unusable", SkipIndex: true,
			Env: map[string]string{"SYMINGEST_DB_PATH": "$ROOT/home/not-a-dir/file/symingest.db"},
			HomeFiles: map[string]string{"not-a-dir": "a file where a directory is expected\n"},
			Steps: ingestConcat(
				ingestBoth("rules", "list"),
				ingestBoth("rules", "add", "a", "tag", "b"),
				ingestBoth("rules", "dry-run", "a", "tag", "b"),
			)},
		{Name: "rules-config-db-path", SkipIndex: true,
			HomeFiles: map[string]string{".config/symingest/config.toml": "db_path = \"$ROOT/home/custom/rules.db\"\n"},
			Steps: ingestConcat(
				ingestBoth("rules", "add", "a", "tag", "b"),
				ingestBoth("rules", "list"),
			)},
		{Name: "rules-bad-symingest-config", SkipIndex: true,
			HomeFiles: map[string]string{".config/symingest/config.toml": "this is = = not toml\n"},
			Steps: ingestConcat(
				ingestBoth("rules", "list"),
				ingestBoth("rules", "add", "a", "tag", "b"),
				ingestBoth("rules", "dry-run", "a", "tag", "b"),
			)},
	}
}

const mailRulesAccount = `{"host":"imap.example.test","port":993,"username":"alice","password_secret":"symvault://mail/alice","folder":"INBOX","from":["billing@example.test"],"subject":["Invoice"],"has_attachment":true,"action":"mark_seen","move_to":"","archive_mail":true}`

func cliIngestMailRulesCases() []cliIngestCase {
	const cfg = "--config=$ROOT/home/mail.toml"
	flow := ingestConcat(
		ingestBoth("mail", "rules", "list", cfg),
		[]cliIngestStep{
			ingestStdin(mailRulesAccount, "--json", "mail", "rules", "create", cfg),
			ingestStdin(mailRulesAccount, "mail", "rules", "create", cfg),
			ingestStdin(`{"host":"imap2.example.test","port":143,"username":"bob","password_secret":"plain-secret-canary-1","action":"move","move_to":"Archive"}`, "--json", "mail", "rules", "create", cfg),
			ingestStdin(`{"host":"imap2.example.test","port":143,"username":"bob","password_secret":"plain-secret-canary-1","action":"move","move_to":"Archive"}`, "mail", "rules", "create", cfg),
		},
		ingestBoth("mail", "rules", "list", cfg),
		[]cliIngestStep{
			// invalid accounts
			ingestStdin(`{}`, "--json", "mail", "rules", "create", cfg),
			ingestStdin(`{"host":"h","port":70000,"username":"u","password_secret":"env://X"}`, "--json", "mail", "rules", "create", cfg),
			ingestStdin(`{"host":"h","port":1,"username":"u","password_secret":"env://X","action":"delete"}`, "--json", "mail", "rules", "create", cfg),
			ingestStdin(`{"host":"h","port":1,"username":"u","password_secret":"env://X","action":"move"}`, "mail", "rules", "create", cfg),
			ingestStdin(`{"host":"h","port":1,"username":"u"}`, "--json", "mail", "rules", "create", cfg),
			ingestStdin(`not json`, "--json", "mail", "rules", "create", cfg),
			ingestStdin(``, "--json", "mail", "rules", "create", cfg),
			ingestStdin(`{"host":"h","port":1,"username":"u","password_secret":"keychain://k"} trailing`, "--json", "mail", "rules", "create", cfg),
			// update: masked echo / empty secret keep the stored one
			ingestStdin(`{"host":"imap2.example.test","port":143,"username":"bob","password_secret":"<redacted>","folder":"Work","action":"move","move_to":"Done"}`, "--json", "mail", "rules", "update", "bob@imap2.example.test:143/INBOX", cfg),
			ingestStdin(`{"host":"imap2.example.test","port":143,"username":"bob","password_secret":"","folder":"INBOX","action":"mark_seen"}`, "--json", "mail", "rules", "update", "bob@imap2.example.test:143/Work", cfg),
			ingestStdin(`{"host":"imap2.example.test","port":143,"username":"bob","password_secret":"env://NEW","folder":"INBOX"}`, "--json", "mail", "rules", "update", "bob@imap2.example.test:143/INBOX", cfg),
			ingestStdin(mailRulesAccount, "--json", "mail", "rules", "update", "nobody@nowhere:1/INBOX", cfg),
			ingestStdin(`{}`, "--json", "mail", "rules", "update", "alice@imap.example.test:993/INBOX", cfg),
			ingestStdin(`garbage`, "--json", "mail", "rules", "update", "alice@imap.example.test:993/INBOX", cfg),
		},
		ingestBoth("mail", "rules", "list", cfg),
		ingestBoth("mail", "rules", "delete", "bob@imap2.example.test:143/INBOX", cfg),
		ingestBoth("mail", "rules", "delete", "bob@imap2.example.test:143/INBOX", cfg),
		ingestBoth("mail", "rules", "list", cfg),
		ingestBoth("mail", "rules", "delete", "alice@imap.example.test:993/INBOX", cfg),
		ingestBoth("mail", "rules", "list", cfg),
	)
	preserved := "# my comment\nocr_lang = \"eng\"\nimap_poll_interval = \"10m\"\n\n[other]\nkeep = true\n\n[[imap_accounts]]\nhost = \"old.example.test\"\nport = 993\nusername = \"carol\"\npassword_secret = \"env://CAROL\"\n\n[tail]\nvalue = 1\n"
	return []cliIngestCase{
		{Name: "mail-rules-flow", SkipIndex: true, SnapshotHome: true, Canaries: []string{"plain-secret-canary-1"}, Steps: flow},
		{Name: "mail-rules-preserve-toml", SkipIndex: true, SnapshotHome: true,
			HomeFiles: map[string]string{"mail.toml": preserved},
			Steps: ingestConcat(
				ingestBoth("mail", "rules", "list", cfg),
				[]cliIngestStep{
					ingestStdin(mailRulesAccount, "--json", "mail", "rules", "create", cfg),
				},
				ingestBoth("mail", "rules", "delete", "carol@old.example.test:993/INBOX", cfg),
				ingestBoth("mail", "rules", "delete", "alice@imap.example.test:993/INBOX", cfg),
			)},
		{Name: "mail-rules-bad-files", SkipIndex: true,
			HomeFiles: map[string]string{"bad.toml": "x = = 1\n", "bad-poll.toml": "imap_poll_interval = \"soon\"\n[[imap_accounts]]\nhost=\"h\"\nport=1\nusername=\"u\"\npassword_secret=\"env://X\"\n"},
			Steps: ingestConcat(
				ingestBoth("mail", "rules", "list", "--config=$ROOT/home/bad.toml"),
				ingestBoth("mail", "rules", "list", "--config=$ROOT/home/bad-poll.toml"),
				ingestBoth("mail", "rules", "list", "--config=$ROOT/home"),
				ingestBoth("mail", "rules", "delete", "x@y:1/INBOX", "--config=$ROOT/home/bad.toml"),
				ingestBoth("mail", "rules", "delete", "x@y:1/INBOX", "--config=$ROOT/home/missing.toml"),
				[]cliIngestStep{
					ingestStdin(mailRulesAccount, "--json", "mail", "rules", "create", "--config=$ROOT/home/bad.toml"),
					ingestStdin(mailRulesAccount, "--json", "mail", "rules", "create", "--config=$ROOT/home"),
					ingestStdin(mailRulesAccount, "--json", "mail", "rules", "update", "alice@imap.example.test:993/INBOX", "--config=$ROOT/home/missing.toml"),
				},
			)},
		{Name: "mail-rules-default-paths", SkipIndex: true, SnapshotHome: true,
			Steps: ingestConcat(
				ingestBoth("mail", "rules", "list"),
				[]cliIngestStep{ingestStdin(mailRulesAccount, "--json", "mail", "rules", "create")},
				ingestBoth("mail", "rules", "list"),
			)},
		{Name: "mail-rules-project-config", SkipIndex: true, SnapshotHome: true,
			CwdFiles: map[string]string{".symingest.toml": "ocr_lang = \"eng\"\n"},
			Steps: ingestConcat(
				ingestBoth("mail", "rules", "list"),
				[]cliIngestStep{ingestStdin(mailRulesAccount, "--json", "mail", "rules", "create")},
				ingestBoth("mail", "rules", "list"),
			)},
	}
}
