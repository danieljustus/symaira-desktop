package main

// `consume status` and `consume set-path`, recorded for RUST-013 against a
// disposable config home (the global config file is written by the command).

func init() {
	cliBodiesCaseBuilders = append(cliBodiesCaseBuilders, cliBodiesConsumeCases)
}

func cliBodiesConsumeCases() []cliBodiesCase {
	vault := bodiesVault()
	vault["inbox_watch/readme.md"] = "inbox\n"
	var cases []cliBodiesCase
	cases = append(cases, cliBodiesCase{Name: "consume", VolatileHistory: true, Files: vault, Steps: bodiesConcat(
		bodiesBoth("consume", "status"),
		bodiesBoth("consume", "set-path", "$ROOT/vault/inbox_watch"),
		bodiesBoth("consume", "status"),
		bodiesBoth("consume", "set-path", "$ROOT/vault/not-there"),
		bodiesBoth("consume", "status"),
		bodiesBoth("consume", "set-path", ""),
		bodiesBoth("consume", "status"),
		bodiesBoth("config", "get", "inbox"),
		bodiesBoth("consume", "set-path"),
		bodiesBoth("consume", "set-path", "a", "b"),
		bodiesBoth("consume", "status", "extra"),
		bodiesBoth("consume"),
	)})
	cases = append(cases, cliBodiesCase{Name: "consume-env-inbox", VolatileHistory: true, Files: vault,
		Env: map[string]string{"SYMDESK_INBOX": "$ROOT/vault/inbox_watch"},
		Steps: bodiesConcat(
			bodiesBoth("consume", "status"),
			bodiesBoth("consume", "set-path", "$ROOT/elsewhere"),
			bodiesBoth("config", "get", "inbox"),
		)})
	cases = append(cases, cliBodiesCase{Name: "consume-no-vault", VolatileHistory: true, NoVault: true, SkipIndex: true,
		Steps: bodiesConcat(
			bodiesBoth("consume", "status"),
			bodiesBoth("consume", "set-path", "$ROOT/x"),
			bodiesBoth("--vault", "$ROOT/nonexistent", "consume", "status"),
			bodiesBoth("--vault", "$ROOT/cwd", "consume", "status"),
		)})
	// Secret values in the environment must never reach the output of the
	// new command bodies, successful or failing.
	const canaryToken = "r013-canary-token-8f3a1c"
	const canaryKey = "r013-canary-key-5d2e9b"
	cases = append(cases, cliBodiesCase{Name: "secret-canaries", VolatileHistory: true, Files: vault,
		Env: map[string]string{
			"SYMDESK_LLM_API_KEY":   canaryKey,
			"SYMDESK_ADMIN_TOKEN":   canaryToken,
			"SYMDESK_SERVER_TOKEN":  canaryToken,
			"SYMDESK_WORKER_TOKEN":  canaryToken,
			"ANTHROPIC_API_KEY":     canaryKey,
			"SYMDESK_ANTHROPIC_KEY": canaryKey,
		},
		Canaries: []string{canaryToken, canaryKey},
		Steps: bodiesConcat(
			bodiesBoth("consume", "status"),
			// `consume set-path` is left out on purpose: like `config set`, Go
			// saves the environment-merged configuration, so an LLM API key
			// from the environment is persisted (mode 0600) into config.toml.
			// Rust mirrors that Go behavior (see the RUST-013 notes).
			bodiesBoth("search", "export", "hello", "--format", "bogus"),
			bodiesBoth("search", "export", "hello"),
			bodiesBoth("search", "notebook", "hello"),
			bodiesBoth("notebook", "delete", "missing"),
			bodiesBoth("related", "missing.md"),
			bodiesBoth("sidecar", "list"),
			bodiesBoth("sidecar", "prune"),
			bodiesBoth("asset", "store", "$ROOT/vault/missing.bin"),
			bodiesBoth("props", "get", "missing.md"),
			bodiesBoth("tags", "rename", "x", "y"),
			bodiesBoth("history", "undo-task", "missing"),
			bodiesBoth("views", "save", "{"),
		)})
	return cases
}
