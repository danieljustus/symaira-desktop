package main

// Saved-view and base-note write commands recorded for RUST-013.

func init() {
	cliBodiesCaseBuilders = append(cliBodiesCaseBuilders, cliBodiesViewCases)
}

func cliBodiesViewCases() []cliBodiesCase {
	vault := bodiesVault()
	vault["invoices/one.md"] = "---\ntitle: One\nstatus: open\namount: 10\n---\nfirst\n"
	vault["invoices/two.md"] = "---\ntitle: Two\nstatus: paid\namount: 20\ntags: [paid]\n---\nsecond\n"
	const viewAll = `{"id":"all","name":"All notes","columns":["title","status"],"sorts":[{"key":"title","ascending":true}]}`
	const viewOpen = `{"id":"open","name":"Open","source":"invoices/","filters":[{"key":"status","operator":"equals","value":"open"}],"columns":["title"],"template":{"ref":"","defaults":{"status":"open"}}}`
	const viewPaid = `{"id":"paid","name":"Paid","source":"invoices/","filters":[{"key":"status","value":"paid"}],"filter_group":{"operator":"and","filters":[{"key":"amount","operator":"gt","value":"5"}]}}`
	const viewNoID = `{"name":"No Id View"}`
	var cases []cliBodiesCase

	cases = append(cases, cliBodiesCase{Name: "views-write", VolatileHistory: true, Files: vault, SnapshotVault: true, Steps: bodiesConcat(
		bodiesBoth("views", "list"),
		bodiesBoth("views", "save", viewAll),
		bodiesBoth("views", "save", viewOpen),
		bodiesBoth("views", "save", viewPaid),
		bodiesBoth("views", "save", viewNoID),
		bodiesJSON("views", "list"),
		bodiesJSON("views", "get", "open"),
		bodiesJSON("views", "siblings", "open"),
		bodiesBoth("views", "save", `{"id":"all","name":"All notes renamed","columns":["title"]}`),
		bodiesBoth("views", "get", "all"),
		bodiesBoth("views", "save", `{"id":"orphan","name":"Orphan","source":"nowhere/"}`),
		bodiesBoth("views", "delete", "orphan"),
		bodiesBoth("views", "delete", "orphan"),
		bodiesBoth("views", "delete", "missing"),
		bodiesBoth("views", "delete", ""),
		bodiesBoth("views", "save", "{"),
		bodiesBoth("views", "save", "not json"),
		bodiesBoth("views", "save", ""),
		bodiesBoth("views", "save", "[]"),
		bodiesBoth("views", "save", `{"id":"x"} trailing`),
		bodiesBoth("views", "save"),
		bodiesBoth("views", "delete"),
		bodiesBoth("views", "delete", "a", "b"),
		bodiesJSON("views", "list"),
		bodiesJSON("views", "base", "list"),
		// Equal (empty) titles tie in Go's unstable sort; nothing lists after these.
		bodiesBoth("views", "save", "null"),
		bodiesBoth("views", "save", "{}"),
	)})

	cases = append(cases, cliBodiesCase{Name: "views-base-write", VolatileHistory: true, Files: vault, SnapshotVault: true, Steps: bodiesConcat(
		bodiesBoth("views", "base", "new", "Invoice Tracker"),
		bodiesBoth("views", "base", "new", "Invoice Tracker"),
		bodiesBoth("views", "base", "new", "Projects", "All the projects"),
		bodiesBoth("views", "base", "new", "  Spaces & Symbols!  "),
		bodiesBoth("views", "base", "new", "ÜÖ"),
		bodiesBoth("views", "base", "new", ""),
		bodiesBoth("views", "base", "get", "invoice-tracker"),
		bodiesJSON("views", "base", "list"),
		bodiesBoth("views", "base", "save", `{"id":"custom","path":"bases/custom.md","title":"Custom","created":"2026-01-02T03:04:05Z","tags":["base","x"],"properties":{"amount":{"type":"number","label":"Amount"}},"views":[{"id":"cv","name":"CV","columns":["title"]}]}`),
		bodiesBoth("views", "base", "save", `{"title":"Only Title","views":[]}`),
		bodiesBoth("views", "base", "save", `{"id":"escape","path":"../escape.md","title":"Escape","views":[]}`),
		bodiesBoth("views", "base", "save", `{"id":"abs","path":"/abs.md","title":"Abs","views":[]}`),
		bodiesBoth("views", "base", "save", "{"),
		bodiesBoth("views", "base", "save", "nope"),
		bodiesBoth("views", "base", "get", "custom"),
		bodiesBoth("views", "base", "get", "bases/custom.md"),
		bodiesBoth("views", "base", "get", "missing"),
		bodiesBoth("views", "base", "get", ""),
		bodiesBoth("views", "base", "delete", "custom"),
		bodiesBoth("views", "base", "delete", "custom"),
		bodiesBoth("views", "base", "delete", "missing"),
		bodiesBoth("views", "base", "delete", "projects"),
		bodiesBoth("trash", "list"),
		bodiesJSON("views", "base", "list"),
		bodiesBoth("views", "base", "new"),
		bodiesBoth("views", "base", "new", "a", "b", "c"),
		bodiesBoth("views", "base", "save"),
		bodiesBoth("views", "base", "delete"),
	)})

	cases = append(cases, cliBodiesCase{Name: "views-write-unindexed", VolatileHistory: true, Files: vault, SkipIndex: true, SnapshotVault: true, Steps: bodiesConcat(
		bodiesBoth("views", "save", viewAll),
		bodiesBoth("views", "base", "new", "Fresh"),
		bodiesBoth("views", "list"),
	)})

	return cases
}
