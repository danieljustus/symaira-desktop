package main

// Embedding transport failures of `index` against a closed loopback port,
// recorded for RUST-013: Go's `dial tcp ... connection refused` wording, the
// `engine: ollama retry` log lines and the `exhausted N retries` wrapper.

func init() {
	cliBodiesCaseBuilders = append(cliBodiesCaseBuilders, cliBodiesTransportCases)
}

func cliBodiesTransportCases() []cliBodiesCase {
	home := func(retries string) map[string]string {
		return map[string]string{
			".config/symseek/config.toml": "ollama_url = \"http://127.0.0.1:$CLOSED_PORT/api/embeddings\"\nembedding_dim = 3\ntimeout_seconds = 2\nretry_count = " + retries + "\nretry_backoff_ms = 1\nmodel = \"fixture\"\n",
		}
	}
	vault := map[string]string{
		"one.md": "# One\nhello transport world\n",
		"two.md": "# Two\nsecond note about transport\n",
	}
	var cases []cliBodiesCase
	for _, retries := range []string{"0", "1", "3"} {
		cases = append(cases, cliBodiesCase{Name: "index-transport-retries-" + retries, SkipIndex: true, Files: vault, HomeFiles: home(retries), Steps: []cliBodiesStep{
			bodiesStepOf("index"),
			bodiesStepOf("--json", "index"),
			bodiesStepOf("search", "transport"),
		}})
	}
	return cases
}
