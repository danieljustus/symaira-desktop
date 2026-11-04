package main

import (
	"fmt"
	"strings"
)

// RUST-015 HTTP-006 (AI streaming), HTTP-007 (network hardening, TLS) and the
// `serve` flag surface with Home Assistant options. Replay:
// `crates/symdesk-cli/tests/http_surface.rs`.

// providerCase starts the fake provider and plants a provider API key in the
// server environment (SEC-002: it must not appear in any response or log).
func providerCase(name string, steps ...httpStep) httpCase {
	c := httpCaseOf(name, steps...)
	c.Provider = true
	c.Env = map[string]string{"SYMDESK_LLM_API_KEY": "$PROVKEY"}
	return c
}

func transform(label, who, text string) httpStep {
	return rq(label, "POST", "/api/v1/ai/transform", who, fmt.Sprintf(`{"text":%q}`, text))
}

func ask(label, who, query string) httpStep {
	return rq(label, "POST", "/api/v1/ai/ask", who, fmt.Sprintf(`{"query":%q}`, query))
}

func providerLog() httpStep {
	return httpStep{Label: "provider requests", Kind: "provider"}
}

func httpAICases() []httpCase {
	var cases []httpCase

	// Streaming transform against the fake provider.
	streamed := func(label, text string, lines int, release bool) httpStep {
		step := rq(label, "POST", "/api/v1/ai/transform", "ADMIN", fmt.Sprintf(`{"text":%q}`, text))
		step.Kind = "stream"
		step.StreamLines = lines
		step.StreamUntil = "first"
		step.Release = release
		return step
	}
	abort := func(label, text string, lines int) httpStep {
		step := streamed(label, text, lines, false)
		step.Abort = true
		return step
	}
	steps := []httpStep{
		transform("chunks", "ADMIN", "short provider input"),
		streamed("flushed before the provider finishes", "gated provider input", 1, true),
		transform("split json line", "ADMIN", "split provider input"),
		transform("provider failure", "ADMIN", "provider error input"),
		transform("oversized provider line", "ADMIN", "oversized provider input"),
		withUntil(abort("client disconnect after first chunk", "disconnect provider input", 1), "partial"),
		abort("client disconnect before first chunk", "hang provider input", 0),
		{Label: "provider after disconnects", Kind: "provider", Count: 2},
		transform("worker token", "WORKER", "worker provider input"),
		func() httpStep {
			s := rq("intent continue", "POST", "/api/v1/ai/transform", "ADMIN", `{"text":"continue provider input","intent":"continue"}`)
			return s
		}(),
		rq("intent rewrite", "POST", "/api/v1/ai/transform", "ADMIN", `{"text":"rewrite provider input","intent":"rewrite"}`),
		rq("intent unknown falls back", "POST", "/api/v1/ai/transform", "ADMIN", `{"text":"unknown provider input","intent":"shout"}`),
		rq("intent trimmed", "POST", "/api/v1/ai/transform", "ADMIN", `{"text":"  trimmed provider input  ","intent":" summarize "}`),
		providerLog(),
	}
	cases = append(cases, providerCase("ai transform provider", steps...))

	steps = []httpStep{
		rq("empty text", "POST", "/api/v1/ai/transform", "ADMIN", `{"text":"   "}`),
		rq("missing text", "POST", "/api/v1/ai/transform", "ADMIN", `{}`),
		rq("invalid json", "POST", "/api/v1/ai/transform", "ADMIN", `{`),
		rq("unknown field", "POST", "/api/v1/ai/transform", "ADMIN", `{"text":"a","x":1}`),
		rq("wrong type", "POST", "/api/v1/ai/transform", "ADMIN", `{"text":5}`),
		rq("trailing data is ignored", "POST", "/api/v1/ai/transform", "ADMIN", `{"text":"a provider input"} {"x":1}`),
		rq("get method", "GET", "/api/v1/ai/transform", "ADMIN", ""),
		rq("no credentials", "POST", "/api/v1/ai/transform", "", `{"text":"a"}`),
		providerLog(),
	}
	over := rq("text over 256 KiB", "POST", "/api/v1/ai/transform", "ADMIN", `{"text":"`)
	over.BodyRepeat = &httpRepeat{Text: "a", Count: 256<<10 + 8}
	steps = append(steps[:len(steps)-1], over, providerLog())
	cases = append(cases, providerCase("ai transform validation", steps...))

	// No provider configured: Go degrades honestly.
	cases = append(cases, httpCaseOf("ai transform without provider",
		transform("not configured", "ADMIN", "some text"),
		transform("worker", "WORKER", "some text"),
	))
	cases = append(cases, httpCaseOf("ai ask without provider",
		ask("unscoped", "ADMIN", "Body"),
		ask("no hits", "ADMIN", "zzzzqqqq"),
		rq("notebook scoped", "POST", "/api/v1/ai/ask", "ADMIN", `{"query":"Body","notebook":"research"}`),
		rq("unknown notebook", "POST", "/api/v1/ai/ask", "ADMIN", `{"query":"Body","notebook":"nope"}`),
		rq("empty query", "POST", "/api/v1/ai/ask", "ADMIN", `{"query":"  "}`),
		rq("unknown field", "POST", "/api/v1/ai/ask", "ADMIN", `{"query":"a","x":1}`),
		rq("get method", "GET", "/api/v1/ai/ask", "ADMIN", ""),
		rq("no credentials", "POST", "/api/v1/ai/ask", "", `{"query":"a"}`),
	))

	// Ask against the fake provider: retrieval context in the prompt.
	askStream := func(label, query string, lines int, release bool) httpStep {
		step := rq(label, "POST", "/api/v1/ai/ask", "ADMIN", fmt.Sprintf(`{"query":%q}`, query))
		step.Kind = "stream"
		step.StreamLines = lines
		step.StreamUntil = "first"
		step.Release = release
		return step
	}
	cases = append(cases, providerCase("ai ask provider",
		ask("with sources", "ADMIN", "Body short provider input"),
		ask("sources and chunks", "ADMIN", "Body"),
		askStream("flushed before the provider finishes", "Body gated provider input", 5, true),
		ask("provider failure", "ADMIN", "provider error input"),
		rq("notebook scoped", "POST", "/api/v1/ai/ask", "ADMIN", `{"query":"Body","notebook":"research"}`),
		func() httpStep {
			s := rq("disconnect", "POST", "/api/v1/ai/ask", "ADMIN", `{"query":"disconnect provider input"}`)
			s.Kind = "stream"
			s.StreamLines = 20
			s.StreamUntil = "partial"
			s.Abort = true
			return s
		}(),
		httpStep{Label: "provider after disconnect", Kind: "provider", Count: 1},
	))

	// Ask as a named user: documents the user cannot read never reach the
	// prompt or the citations.
	named := namedCase("ai ask named users",
		ask("alice", "NAMED", "Body"),
		ask("worker-user", "WNAMED", "Body"),
		ask("carol", "NAMEDADM", "Body"),
		providerLog(),
	)
	named.Provider = true
	named.Env = map[string]string{"SYMDESK_LLM_API_KEY": "$PROVKEY"}
	cases = append(cases, named)

	// The AI request bucket: twelve requests in thirty seconds block the source.
	var steps12 []httpStep
	for i := 1; i <= 14; i++ {
		steps12 = append(steps12, transform(fmt.Sprintf("request %d", i), "ADMIN", "rate input"))
	}
	steps12 = append(steps12, ask("ask while blocked", "ADMIN", "Body"), rq("status is not limited", "GET", "/api/v1/status", "ADMIN", ""))
	cases = append(cases, httpCaseOf("ai rate limit", steps12...))
	return cases
}

// ---------------------------------------------------------------- HTTP-007

func httpNetworkCases() []httpCase {
	var cases []httpCase

	// Host: only a missing or malformed Host is rejected; a foreign Host is
	// served (no virtual-host check).
	var steps []httpStep
	for _, host := range []string{"evil.example", "127.0.0.1:1", "localhost", "[::1]:9", "user@host", "a b", "evil.example:notaport", ""} {
		step := rq("status Host="+host, "GET", "/api/v1/status", "ADMIN", "")
		step.Headers = append(step.Headers, [2]string{"Host", host})
		steps = append(steps, step)
	}
	noHost := rq("status without Host", "GET", "/api/v1/status", "ADMIN", "")
	noHost.NoHost = true
	steps = append(steps, noHost)
	http10 := rq("status HTTP/1.0 without Host", "GET", "/api/v1/status", "ADMIN", "")
	http10.Version = "HTTP/1.0"
	http10.NoHost = true
	steps = append(steps, http10)
	absolute := httpStep{Label: "absolute-form target", Kind: "raw", Raw: "GET http://other.example/api/v1/status HTTP/1.1\r\nHost: 127.0.0.1:{{PORT}}\r\nAuthorization: Bearer $ADMIN\r\nConnection: close\r\n\r\n", Mask: httpVolatile, VolatileLength: true}
	steps = append(steps, absolute)
	cases = append(cases, httpCaseOf("network host", steps...))

	// Origin and CORS: no Access-Control headers, preflight is a plain 405.
	steps = nil
	for _, origin := range []string{"http://evil.example", "null", "http://127.0.0.1"} {
		for _, target := range []string{"/healthz", "/api/v1/status"} {
			step := rq(fmt.Sprintf("GET %s Origin=%s", target, origin), "GET", target, "ADMIN", "")
			step.Headers = append(step.Headers, [2]string{"Origin", origin})
			steps = append(steps, step)
		}
	}
	preflight := rq("preflight status", "OPTIONS", "/api/v1/status", "", "")
	preflight.Headers = append(preflight.Headers, [2]string{"Origin", "http://evil.example"}, [2]string{"Access-Control-Request-Method", "GET"}, [2]string{"Access-Control-Request-Headers", "authorization"})
	steps = append(steps, preflight)
	preflightFiles := rq("preflight files PUT", "OPTIONS", "/api/v1/files?path=x.md", "ADMIN", "")
	preflightFiles.Headers = append(preflightFiles.Headers, [2]string{"Origin", "http://evil.example"}, [2]string{"Access-Control-Request-Method", "PUT"})
	steps = append(steps, preflightFiles)
	for _, target := range []string{"/healthz", "/s/unknown", "/missing"} {
		step := rq("security headers "+target, "GET", target, "", "")
		steps = append(steps, step)
	}
	cases = append(cases, httpCaseOf("network origin", steps...))

	// Bodies: limits per route and framing errors.
	jsonBody := func(label, target string, count int) httpStep {
		step := rq(label, "POST", target, "ADMIN", "")
		step.Body = `{"x":"`
		step.BodyRepeat = &httpRepeat{Text: "a", Count: count}
		step.ReadMS = 10000
		return step
	}
	steps = []httpStep{
		jsonBody("lease body over 64 KiB", "/api/v1/worker/lease", 64<<10+8),
		jsonBody("fail body over 256 KiB", "/api/v1/worker/fail", 256<<10+8),
		jsonBody("ask body over 8 KiB", "/api/v1/ai/ask", 8<<10+8),
		func() httpStep {
			step := jsonBody("retry body is ignored", "/api/v1/jobs/retry?id=00000000000000000000000000000000", 1<<20)
			step.IgnoreClosed = true
			return step
		}(),
		jsonBody("share body is unbounded json", "/api/v1/share", 1<<20),
	}
	// A negative or conflicting Content-Length is rejected by hyper's parser
	// (bodiless 400): a declared limit, covered by a Rust-only assertion.
	chunkedBad := httpStep{Label: "invalid chunk size", Method: "POST", Target: "/api/v1/worker/lease", Headers: append(httpAuth("ADMIN"), [2]string{"Transfer-Encoding", "chunked"}), Body: "zz\r\n{}\r\n0\r\n\r\n", Mask: httpVolatile, VolatileLength: true}
	chunkedAndLength := httpStep{Label: "chunked with content-length", Kind: "raw", Raw: "POST /api/v1/worker/lease HTTP/1.1\r\nHost: 127.0.0.1:{{PORT}}\r\nAuthorization: Bearer $ADMIN\r\nTransfer-Encoding: chunked\r\nContent-Length: 2\r\nConnection: close\r\n\r\n2\r\n{}\r\n0\r\n\r\n", Mask: httpVolatile, VolatileLength: true}
	shortBody := httpStep{Label: "short body then idle", Kind: "raw", Raw: "POST /api/v1/worker/lease HTTP/1.1\r\nHost: 127.0.0.1:{{PORT}}\r\nAuthorization: Bearer $ADMIN\r\nContent-Length: 50\r\nConnection: close\r\n\r\n{\"worker_id\":", ReadMS: 1500, Mask: httpVolatile, VolatileLength: true}
	steps = append(steps, chunkedBad, chunkedAndLength, shortBody)
	cases = append(cases, httpCaseOf("network bodies", steps...))

	// Read-header timeout: an incomplete header block is closed after 10 s.
	hold := httpStep{Label: "incomplete header", Kind: "raw", Raw: "GET /healthz HTTP/1.1\r\nHost: 127.0.0.1:{{PORT}}\r\nX-Slow: a", ReadMS: 14000, TimeClass: true, Mask: httpVolatile, VolatileLength: true}
	cases = append(cases, httpCaseOf("network header timeout", hold,
		rq("server still serves", "GET", "/healthz", "", "")))

	// Shutdown: an idle server stops at once; a stalled request holds the
	// graceful stop until the 15 s deadline.
	cases = append(cases, httpCaseOf("network shutdown idle",
		rq("status", "GET", "/api/v1/status", "ADMIN", ""),
		httpStep{Label: "sigterm", Kind: "shutdown", Stall: "GET /healthz HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n"},
	))
	cases = append(cases, httpCaseOf("network shutdown stalled",
		httpStep{Label: "sigterm with a stalled request", Kind: "shutdown", Stall: "POST /api/v1/worker/lease HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer $ADMIN\r\nContent-Length: 100\r\n\r\n{\"worker"},
	))

	// TLS.
	tlsSteps := []httpStep{
		rq("healthz", "GET", "/healthz", "", ""),
		rq("status", "GET", "/api/v1/status", "ADMIN", ""),
		rq("wrong token", "GET", "/api/v1/status", "", ""),
		rq("file", "GET", "/api/v1/files?path=Hello.md", "ADMIN", ""),
		rq("put", "PUT", "/api/v1/files?path=tls/Put.md", "ADMIN", "over tls"),
		rq("snapshot", "GET", "/api/v1/snapshot", "ADMIN", ""),
		withTLSVersion(rq("tls 1.2 client", "GET", "/healthz", "", ""), "1.2"),
		withTLSVersion(rq("tls 1.3 client", "GET", "/healthz", "", ""), "1.3"),
		withPlain(rq("plaintext request on the tls port", "GET", "/healthz", "", "")),
		withPlain(rq("plaintext post on the tls port", "POST", "/api/v1/share", "ADMIN", "{}")),
		{Label: "tls head", Method: "HEAD", Target: "/healthz", Mask: httpVolatile, VolatileLength: true},
		{Label: "garbage instead of a handshake", Kind: "raw", Plain: true, Raw: "\x16\x03\x01\x00\x05garbage\r\n\r\n", ReadMS: 1500, Mask: httpVolatile, VolatileLength: true},
		httpStep{Label: "vault tree", Kind: "tree", TreeInclude: []string{"tls/"}, Mask: []string{"hex", "time"}},
	}
	tlsCase := httpCaseOf("network tls", tlsSteps...)
	tlsCase.TLS = true
	cases = append(cases, tlsCase)

	// Launch-only cases: flag validation of the TLS and local-worker options.
	launch := func(label string, args ...string) httpStep {
		return httpStep{Label: label, Kind: "launch", Args: args}
	}
	common := []string{"--vault", "$VAULT", "--token", "$ADMIN", "--listen", "127.0.0.1:$PORT"}
	with := func(extra ...string) []string { return append(append([]string{}, common...), extra...) }
	launchCase := httpCaseOf("serve launch tls and worker flags",
		launch("tls cert without key", with("--tls-cert", "$TLSCERT")...),
		launch("tls key without cert", with("--tls-key", "$TLSKEY")...),
		launch("missing certificate file", with("--tls-cert", "$ROOT/missing.pem", "--tls-key", "$TLSKEY")...),
		launch("missing key file", with("--tls-cert", "$TLSCERT", "--tls-key", "$ROOT/missing.pem")...),
		launch("certificate is not pem", with("--tls-cert", "$VAULT/bad.pem", "--tls-key", "$TLSKEY")...),
		launch("key is not pem", with("--tls-cert", "$TLSCERT", "--tls-key", "$VAULT/bad.pem")...),
		launch("local worker with tls", with("--local-worker", "--tls-cert", "$TLSCERT", "--tls-key", "$TLSKEY")...),
		launch("local worker explicit true with tls", with("--local-worker=true", "--tls-cert", "$TLSCERT")...),
		launch("local worker invalid ollama url", with("--local-worker", "--ollama-url", "not a url")...),
		launch("local worker ollama url with path", with("--local-worker", "--ollama-url", "http://127.0.0.1:1/x")...),
		launch("short token", "--vault", "$VAULT", "--token", "short", "--listen", "127.0.0.1:$PORT"),
		launch("short worker token", with("--worker-token", "short")...),
		launch("worker token equals token", with("--worker-token", "$ADMIN")...),
		launch("no token", "--vault", "$VAULT", "--listen", "127.0.0.1:$PORT"),
		launch("missing vault directory", "--vault", "$ROOT/nope", "--token", "$ADMIN", "--listen", "127.0.0.1:$PORT"),
		launch("vault is a file", "--vault", "$VAULT/bad.pem", "--token", "$ADMIN", "--listen", "127.0.0.1:$PORT"),
		launch("invalid listen port", "--vault", "$VAULT", "--token", "$ADMIN", "--listen", "127.0.0.1:99999"),
		launch("listen without port", "--vault", "$VAULT", "--token", "$ADMIN", "--listen", "nocolon"),
		launch("listen too many colons", "--vault", "$VAULT", "--token", "$ADMIN", "--listen", "a:b:c"),
		launch("unknown host", "--vault", "$VAULT", "--token", "$ADMIN", "--listen", "nosuch.invalid:80"),
		launch("invalid local-worker value", with("--local-worker=maybe")...),
		launch("unknown flag", with("--bogus")...),
	)
	launchCase.Files["bad.pem"] = "not a pem file\n"
	launchCase.NoServer = true
	cases = append(cases, launchCase)
	return cases
}

func withUntil(step httpStep, text string) httpStep {
	step.StreamUntil = text
	return step
}

func withTLSVersion(step httpStep, version string) httpStep {
	step.TLSVersion = version
	return step
}

func withPlain(step httpStep) httpStep {
	step.Plain = true
	return step
}

// ------------------------------------------------- serve flags and options

func haCase(name string, serve httpServe, env map[string]string, steps ...httpStep) httpCase {
	c := httpCaseOf(name, steps...)
	c.Serve = &serve
	c.Env = env
	return c
}

func statusAs(who string) httpStep {
	return rq("status with "+strings.ToLower(who), "GET", "/api/v1/status", who, "")
}

func fileProbe(label, path string) httpStep {
	return rq(label, "GET", "/api/v1/files?path="+path, "ADMIN", "")
}

func httpServeCases() []httpCase {
	var cases []httpCase
	listen := "127.0.0.1:$PORT"
	base := func(extra ...string) []string {
		return append([]string{"--listen", listen, "--vault", "$VAULT"}, extra...)
	}

	// Token precedence: flag, then environment, then the options file.
	cases = append(cases,
		haCase("serve token flag beats env and options", httpServe{
			Args:    base("--token", "$ADMIN"),
			Env:     map[string]string{"SYMDESK_SERVER_TOKEN": "$ADMIN2"},
			Options: `{"server_token":"$WORKER2"}`,
		}, nil, statusAs("ADMIN"), statusAs("ADMIN2"), statusAs("WORKER2")),
		haCase("serve token env beats options", httpServe{
			Args:    base(),
			Env:     map[string]string{"SYMDESK_SERVER_TOKEN": "$ADMIN2"},
			Options: `{"server_token":"$WORKER2"}`,
		}, nil, statusAs("ADMIN2"), statusAs("WORKER2")),
		haCase("serve token from options", httpServe{
			Args:    base(),
			Options: `{"server_token":"$WORKER2"}`,
		}, nil, statusAs("WORKER2"), statusAs("ADMIN")),
		haCase("serve empty token flag falls to options", httpServe{
			Args:    base("--token", ""),
			Env:     map[string]string{"SYMDESK_SERVER_TOKEN": "$ADMIN2"},
			Options: `{"server_token":"$WORKER2"}`,
		}, nil, statusAs("WORKER2"), statusAs("ADMIN2")),
		haCase("serve worker token precedence", httpServe{
			Args:    base("--token", "$ADMIN", "--worker-token", "$WORKER"),
			Env:     map[string]string{"SYMDESK_WORKER_TOKEN": "$WORKER2"},
			Options: `{"worker_token":"$NAMED"}`,
		}, nil,
			rq("lease flag worker", "POST", "/api/v1/worker/lease", "WORKER", `{"worker_id":"w","capabilities":["ocr"]}`),
			rq("lease env worker", "POST", "/api/v1/worker/lease", "WORKER2", `{"worker_id":"w","capabilities":["ocr"]}`),
			rq("lease options worker", "POST", "/api/v1/worker/lease", "NAMED", `{"worker_id":"w","capabilities":["ocr"]}`),
		),
		haCase("serve worker token from env then options", httpServe{
			Args:    base("--token", "$ADMIN"),
			Env:     map[string]string{"SYMDESK_WORKER_TOKEN": "$WORKER2"},
			Options: `{"worker_token":"$NAMED"}`,
		}, nil,
			rq("lease env worker", "POST", "/api/v1/worker/lease", "WORKER2", `{"worker_id":"w","capabilities":["ocr"]}`),
			rq("lease options worker", "POST", "/api/v1/worker/lease", "NAMED", `{"worker_id":"w","capabilities":["ocr"]}`),
		),
		haCase("serve worker token only in options", httpServe{
			Args:    base("--token", "$ADMIN"),
			Options: `{"worker_token":"$NAMED"}`,
		}, nil,
			rq("lease options worker", "POST", "/api/v1/worker/lease", "NAMED", `{"worker_id":"w","capabilities":["ocr"]}`),
			rq("admin routes denied to it", "GET", "/api/v1/jobs", "NAMED", ""),
		),
	)

	// Listen precedence: flag, then options (above the environment), then the
	// environment. The wrong address is never contacted.
	cases = append(cases,
		haCase("serve listen flag beats options", httpServe{
			Args:    []string{"--listen", listen, "--vault", "$VAULT", "--token", "$ADMIN"},
			Options: `{"listen":"127.0.0.1:1"}`,
		}, nil, statusAs("ADMIN")),
		haCase("serve listen options beat env", httpServe{
			Args:    []string{"--vault", "$VAULT", "--token", "$ADMIN"},
			Env:     map[string]string{"SYMDESK_SERVER_LISTEN": "127.0.0.1:1"},
			Options: `{"listen":"` + listen + `"}`,
		}, nil, statusAs("ADMIN")),
		haCase("serve listen from env", httpServe{
			Args: []string{"--vault", "$VAULT", "--token", "$ADMIN"},
			Env:  map[string]string{"SYMDESK_SERVER_LISTEN": listen},
		}, nil, statusAs("ADMIN")),
	)

	// Vault precedence: flag, then environment/config, then the options file.
	cases = append(cases,
		haCase("serve vault flag beats options", httpServe{
			Args:    []string{"--listen", listen, "--token", "$ADMIN", "--vault", "$VAULT"},
			Env:     map[string]string{"SYMDESK_VAULT": "$VAULT2"},
			Options: `{"vault":"$VAULT2"}`,
		}, nil, fileProbe("first vault", "Hello.md"), fileProbe("second vault", "Second.md")),
		haCase("serve vault env beats options", httpServe{
			Args:    []string{"--listen", listen, "--token", "$ADMIN"},
			Env:     map[string]string{"SYMDESK_VAULT": "$VAULT"},
			Options: `{"vault":"$VAULT2"}`,
		}, nil, fileProbe("first vault", "Hello.md"), fileProbe("second vault", "Second.md")),
		haCase("serve vault from options", httpServe{
			Args:    []string{"--listen", listen, "--token", "$ADMIN"},
			Env:     map[string]string{"SYMDESK_VAULT": ""},
			Options: `{"vault":"$VAULT2"}`,
		}, nil, fileProbe("first vault", "Hello.md"), fileProbe("second vault", "Second.md")),
	)

	// Options file quirks: Go's json decoding rules.
	cases = append(cases,
		haCase("serve options case-insensitive keys", httpServe{
			Args:    []string{"--vault", "$VAULT"},
			Options: `{"SERVER_TOKEN":"$ADMIN","Listen":"` + listen + `"}`,
		}, nil, statusAs("ADMIN")),
		haCase("serve options duplicate keys last wins", httpServe{
			Args:    []string{"--vault", "$VAULT", "--listen", listen},
			Options: `{"server_token":"$ADMIN2","server_token":"$ADMIN"}`,
		}, nil, statusAs("ADMIN"), statusAs("ADMIN2")),
		haCase("serve options wrong field type is skipped", httpServe{
			Args:    []string{"--vault", "$VAULT", "--token", "$ADMIN"},
			Options: `{"vault":7,"listen":"` + listen + `","local_processing":"yes"}`,
		}, nil, statusAs("ADMIN")),
		haCase("serve options invalid json is ignored", httpServe{
			Args:    base("--token", "$ADMIN"),
			Options: `{"server_token":`,
		}, nil, statusAs("ADMIN")),
		haCase("serve options array is ignored", httpServe{
			Args:    base("--token", "$ADMIN"),
			Options: `[{"server_token":"x"}]`,
		}, nil, statusAs("ADMIN")),
		haCase("serve options null values keep defaults", httpServe{
			Args:    base("--token", "$ADMIN"),
			Options: `{"listen":null,"vault":null,"server_token":null}`,
		}, nil, statusAs("ADMIN")),
	)

	// Launch: option-driven errors.
	launchCase := httpCaseOf("serve launch options",
		httpStep{Label: "options local processing with tls", Kind: "launch", Args: []string{"--vault", "$VAULT", "--token", "$ADMIN", "--listen", listen, "--tls-cert", "$TLSCERT", "--tls-key", "$TLSKEY"}},
		httpStep{Label: "flag false overrides, tls loads", Kind: "launch", Args: []string{"--vault", "$VAULT", "--token", "$ADMIN", "--listen", listen, "--local-worker=false", "--tls-cert", "$ROOT/missing.pem", "--tls-key", "$TLSKEY"}},
		httpStep{Label: "options ollama url is checked", Kind: "launch", Args: []string{"--vault", "$VAULT", "--token", "$ADMIN", "--listen", listen, "--local-worker"}},
		httpStep{Label: "flag ollama url beats options", Kind: "launch", Args: []string{"--vault", "$VAULT", "--token", "$ADMIN", "--listen", listen, "--local-worker", "--ollama-url", "http://127.0.0.1:1"}},
	)
	launchCase.NoServer = true
	launchCase.TLS = false
	launchCase.Serve = &httpServe{Options: `{"local_processing":true,"ollama_url":"nope"}`}
	cases = append(cases, launchCase)

	// Local worker: processes jobs inside the server.
	upload := func(label string) httpStep {
		step := httpStep{Label: label, Kind: "raw", Raw: "POST /api/v1/ingest HTTP/1.1\r\nHost: 127.0.0.1:{{PORT}}\r\nAuthorization: Bearer $ADMIN\r\nContent-Type: multipart/form-data; boundary=XBOUNDARYX\r\nContent-Length: 128\r\nConnection: close\r\n\r\n", Mask: httpVolatile, VolatileLength: true}
		body := "--XBOUNDARYX\r\nContent-Disposition: form-data; name=\"file\"; filename=\"scan.png\"\r\nContent-Type: image/png\r\n\r\nnot really a png\r\n--XBOUNDARYX--\r\n"
		step.Raw = strings.Replace(step.Raw, "Content-Length: 128", fmt.Sprintf("Content-Length: %d", len(body)), 1) + body
		return step
	}
	jobs := func() httpStep { return rq("jobs", "GET", "/api/v1/jobs", "ADMIN", "") }
	workerArgs := base("--token", "$ADMIN")
	enabled := haCase("serve local worker from options", httpServe{
		Args:    workerArgs,
		Options: `{"local_processing":true}`,
	}, nil, upload("upload"), httpStep{Label: "wait for the worker", Kind: "wait_jobs", Body: "$ADMIN"}, jobs())
	enabled.EmptyPath = true
	disabled := haCase("serve local worker flag false beats options", httpServe{
		Args:    append(append([]string{}, workerArgs...), "--local-worker=false"),
		Options: `{"local_processing":true}`,
	}, nil, upload("upload"), httpStep{Label: "past the worker poll interval", Kind: "sleep", WaitMS: 7000}, jobs())
	disabled.EmptyPath = true
	engine := haCase("serve local worker engine from options", httpServe{
		Args:    append(append([]string{}, workerArgs...), "--local-worker"),
		Env:     map[string]string{"SYMDESK_WORKER_ENGINE": "tesseract"},
		Options: `{"worker_engine":"bogus-engine"}`,
	}, nil, upload("upload"), httpStep{Label: "wait for the worker", Kind: "wait_jobs", Body: "$ADMIN"}, jobs())
	engine.EmptyPath = true
	cases = append(cases, enabled, disabled, engine)
	return cases
}
