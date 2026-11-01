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
	return cases
}
