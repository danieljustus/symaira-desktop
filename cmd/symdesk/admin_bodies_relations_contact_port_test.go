package main

// `relations contact [--id] <name|id>` and `relations contact link|unlink`:
// the contact store is seeded below the isolated HOME (ContactSQL), the vault
// carries correspondents, a meeting note with a linked participant and an
// ordinary note with a stale reference cache.

func init() {
	adminBodiesCaseBuilders = append(adminBodiesCaseBuilders, adminRelationsContactCases)
}

const adminContactRefAlice = "provider: symrelate\n  schema_version: 1\n  id: p1\n  kind: person\n  display_name: "

func adminContactVault() map[string]string {
	return map[string]string{
		"inv/acme-1.md":  "---\ntitle: Invoice 1\ntype: invoice\ncorrespondent: Acme GmbH\n---\nfirst invoice\n",
		"inv/acme-2.md":  "---\ntitle: Invoice 2\ntype: invoice\ncorrespondent: acme gmbh\n---\nsecond invoice\n",
		"inv/other.md":   "---\ntitle: Other\ntype: invoice\ncorrespondent: Other AG\n---\nother invoice\n",
		"inv/alice-1.md": "---\ntitle: Letter\ntype: letter\ncorrespondent: Alice Example\n---\nletter\n",
		"meet/m1.md": "---\ntype: meeting\ntitle: Weekly\ncreated: \"2026-01-05T10:00:00Z\"\ntags: [meeting]\nmeeting_id: mt-1\nstarted_at: \"2026-01-05T10:00:00Z\"\nparticipants:\n" +
			"  - label: Ali\n    speaker_ids: [s1]\n    contact_ref:\n      provider: symrelate\n      schema_version: 1\n      id: p1\n      kind: person\n      display_name: Alice Example\n" +
			"  - label: Bob\n    speaker_ids: [s2]\nsymmeet_source:\n  artifact_schema_version: 2\n  review_state: confirmed\n---\n# Weekly\n\ntranscript\n",
		"meet/m2.md": "---\ntype: meeting\ntitle: Other meeting\ncreated: \"2026-01-06T10:00:00Z\"\ntags: []\nmeeting_id: mt-2\nstarted_at: \"2026-01-06T10:00:00Z\"\nparticipants:\n" +
			"  - label: Org\n    speaker_ids: [s1]\n    contact_ref:\n      provider: symrelate\n      schema_version: 1\n      id: o1\n      kind: organization\n      display_name: Acme GmbH\nsymmeet_source:\n  artifact_schema_version: 2\n  review_state: confirmed\n---\n# Other\n",
		"notes/linked.md":   "---\ntitle: Linked\ntags: [x]\ncontact:\n  " + adminContactRefAlice + "Old Name\n  id_extra: keep\n---\nbody mentions Alice\n",
		"notes/plain.md":    "---\ntitle: Plain\nz_last: 1\na_first: two\ntags:\n  - one\n  - two\n---\nplain body\n",
		"notes/nofm.md":     "no frontmatter\n",
		"notes/other-id.md": "---\ntitle: Foreign\ncontact:\n  provider: other\n  schema_version: 1\n  id: p1\n  kind: person\n  display_name: Foreign\n---\nforeign provider\n",
	}
}

var adminContactSeed = []string{
	"INSERT INTO persons(id, display_name) VALUES('p1', 'Alice Example')",
	"INSERT INTO persons(id, display_name) VALUES('p2', 'Alice Example')",
	"INSERT INTO persons(id, display_name) VALUES('p3', 'Ünï <&> Contact')",
	"INSERT INTO organizations(id, name) VALUES('o1', 'Acme GmbH')",
	"INSERT INTO organizations(id, name) VALUES('o2', 'Alice Example')",
}

func adminRelationsContactCases() []adminBodiesCase {
	files := adminContactVault()
	return []adminBodiesCase{
		{Name: "contact-by-name", Files: files, ContactSQL: adminContactSeed, Volatile: true, SnapshotVault: true, Steps: adminConcat(
			adminBoth("relations", "contact", "Acme GmbH"),
			adminBoth("relations", "contact", "  acme gmbh  "),
			adminBoth("relations", "contact", "Alice Example"),
			adminBoth("relations", "contact", "ALICE EXAMPLE"),
			adminBoth("relations", "contact", "Ünï <&> Contact"),
			adminBoth("relations", "contact", "Other AG"),
			adminBoth("relations", "contact", "Nobody Known"),
			adminBoth("relations", "contact", "   "),
			adminBoth("relations", "contact", ""),
		)},
		{Name: "contact-by-id", Files: files, ContactSQL: adminContactSeed, Volatile: true, SnapshotVault: true, Steps: adminConcat(
			adminBoth("relations", "contact", "--id", "p1"),
			adminBoth("relations", "contact", "--id", "p1"),
			adminBoth("relations", "contact", "--id", " o1 "),
			adminBoth("relations", "contact", "--id", "p3"),
			adminBoth("relations", "contact", "--id", "missing"),
			adminBoth("relations", "contact", "--id", ""),
			adminBoth("relations", "contact", "--id", "   "),
			adminBoth("relations", "contact", "p1", "--id"),
			adminBoth("relations", "contact", "--id=true", "o2"),
		)},
		{Name: "contact-link-lifecycle", Files: files, ContactSQL: adminContactSeed, Volatile: true, SnapshotVault: true, Steps: adminConcat(
			adminBoth("relations", "contact", "link", "notes/plain.md", "p1"),
			adminBoth("relations", "contact", "link", "notes/plain.md", "p1"),
			adminBoth("relations", "contact", "link", "notes/plain.md", "o1"),
			adminBoth("relations", "contact", "--id", "o1"),
			adminBoth("relations", "contact", "link", "notes/nofm.md", "p3"),
			adminBoth("relations", "contact", "link", "notes/linked.md", "p3"),
			adminBoth("relations", "contact", "link", "notes/plain.md", " p2 "),
			adminBoth("relations", "contact", "unlink", "notes/plain.md"),
			adminBoth("relations", "contact", "unlink", "notes/plain.md"),
			adminBoth("relations", "contact", "unlink", "notes/linked.md"),
			adminBoth("relations", "contact", "unlink", "notes/nofm.md"),
			adminBoth("relations", "contact", "--id", "p1"),
		)},
		{Name: "contact-link-errors", Files: files, ContactSQL: adminContactSeed, Volatile: true, SnapshotVault: true, Steps: adminConcat(
			adminBoth("relations", "contact", "link", "notes/plain.md", "missing"),
			adminBoth("relations", "contact", "link", "notes/plain.md", ""),
			adminBoth("relations", "contact", "link", "notes/ghost.md", "p1"),
			adminBoth("relations", "contact", "link", "../escape.md", "p1"),
			adminBoth("relations", "contact", "link", "/etc/passwd", "p1"),
			adminBoth("relations", "contact", "link", "notes", "p1"),
			adminBoth("relations", "contact", "link", "notes/plain.md"),
			adminBoth("relations", "contact", "link", "notes/plain.md", "p1", "extra"),
			adminBoth("relations", "contact", "unlink"),
			adminBoth("relations", "contact", "unlink", "a", "b"),
			adminBoth("relations", "contact", "unlink", "notes/ghost.md"),
			adminBoth("relations", "contact", "unlink", "../escape.md"),
			adminBoth("relations", "contact"),
			adminBoth("relations", "contact", "a", "b"),
			adminBoth("relations", "contact", "bogus-flag", "--nope"),
		)},
		{Name: "contact-store-unavailable", Files: files, SnapshotVault: true, Volatile: true,
			Env: map[string]string{"SYMRELATE_DATA_HOME": "$ROOT/vault/inv/acme-1.md/store"}, Steps: adminConcat(
				adminBoth("relations", "contact", "Acme GmbH"),
				adminBoth("relations", "contact", "--id", "p1"),
				adminBoth("relations", "contact", "link", "notes/plain.md", "p1"),
				adminBoth("relations", "contact", "unlink", "notes/linked.md"),
			)},
		{Name: "contact-empty-store", Files: files, SnapshotVault: true, Volatile: true, Steps: adminConcat(
			adminBoth("relations", "contact", "Acme GmbH"),
			adminBoth("relations", "contact", "--id", "p1"),
			adminBoth("relations", "contact", "link", "notes/plain.md", "p1"),
		)},
		{Name: "contact-without-vault", NoVault: true, Volatile: true, SkipIndex: true, Steps: adminConcat(
			adminBoth("relations", "contact", "Acme GmbH"),
			adminBoth("relations", "contact", "--id", "p1"),
			adminBoth("relations", "contact", "link", "a.md", "p1"),
			adminBoth("relations", "contact", "unlink", "a.md"),
		)},
	}
}
