package main

import (
	"fmt"
	"strings"
)

// RUST-015 HTTP-002/003/005/006/007 and serve flag-surface cases. They extend
// the surface fixture (`http_surface_port_test.go`); the Rust replay is the
// same `crates/symdesk-cli/tests/http_surface.rs`.

// rq is a request step whose volatile values (times, hex ids, version, dates)
// are masked.
func rq(label, method, target, auth, body string) httpStep {
	step := httpReq(label, method, target, httpAuth(auth), body)
	step.Mask = httpVolatile
	step.VolatileLength = true
	return step
}

func withHeaders(step httpStep, headers ...[2]string) httpStep {
	step.Headers = append(step.Headers, headers...)
	return step
}

// httpNamedFiles is the std vault plus a named-user configuration: alice
// (user), worker-user (worker), carol (admin role) and a group `team`.
func httpNamedFiles() map[string]string {
	files := httpStdFiles()
	files[".symdesk/users.json"] = `[{"name":"alice","token_hash":"$NAMED_HASH","roles":["user"]},{"name":"worker-user","token_hash":"$WNAMED_HASH","roles":["worker"]},{"name":"carol","token_hash":"$NAMEDADM_HASH","roles":["admin","user"]}]`
	files[".symdesk/groups.json"] = `[{"name":"team","members":["alice"]}]`
	files[".symdesk/permissions.json"] = `[{"path":"Hello.md","owner":"carol","read_users":["carol"],"write_users":["carol"]},{"path":"nested/Note.md","owner":"alice","read_users":["alice"],"write_users":["alice"]},{"path":"inbox/c.png","owner":"carol","read_groups":["team"]},{"path":"notebooks/research.md","owner":"carol","read_users":["alice"]}]`
	return files
}

func namedCase(name string, steps ...httpStep) httpCase {
	return httpCase{Name: name, Files: httpNamedFiles(), Symlinks: httpStdSymlinks(), Steps: steps}
}

// ---------------------------------------------------------------- HTTP-002

func httpAuthCases() []httpCase {
	var cases []httpCase

	forms := []struct {
		label  string
		header [][2]string
	}{
		{"bearer admin", [][2]string{{"Authorization", "Bearer $ADMIN"}}},
		{"bearer worker", [][2]string{{"Authorization", "Bearer $WORKER"}}},
		{"no scheme prefix", [][2]string{{"Authorization", "$ADMIN"}}},
		{"double space", [][2]string{{"Authorization", "Bearer  $ADMIN"}}},
		{"trailing space", [][2]string{{"Authorization", "Bearer $ADMIN "}}},
		{"tab suffix", [][2]string{{"Authorization", "Bearer $ADMIN\t"}}},
		{"two headers valid first", [][2]string{{"Authorization", "Bearer $ADMIN"}, {"Authorization", "Bearer nope"}}},
	}
	var steps []httpStep
	for _, form := range forms {
		step := rq("status "+form.label, "GET", "/api/v1/status", "", "")
		step.Headers = form.header
		steps = append(steps, step)
	}
	// Failures (kept below the throttle threshold of five per ten seconds).
	failures := []struct {
		label  string
		header [][2]string
	}{
		{"lowercase scheme", [][2]string{{"Authorization", "bearer $ADMIN"}}},
		{"basic scheme", [][2]string{{"Authorization", "Basic $ADMIN"}}},
		{"two headers invalid first", [][2]string{{"Authorization", "Bearer nope"}, {"Authorization", "Bearer $ADMIN"}}},
	}
	for _, form := range failures {
		step := rq("status "+form.label, "GET", "/api/v1/status", "", "")
		step.Headers = form.header
		steps = append(steps, step)
	}
	steps = append(steps, httpTreeStep())
	cases = append(cases, httpCaseOf("auth forms", steps...))

	// Credentials in the wrong place never authenticate.
	steps = nil
	steps = append(steps, rq("token in query", "GET", "/api/v1/status?token=$ADMIN", "", ""))
	steps = append(steps, withHeaders(rq("api key header", "GET", "/api/v1/status", "", ""), [2]string{"X-Api-Key", "$ADMIN"}))
	steps = append(steps, withHeaders(rq("cookie", "GET", "/api/v1/status", "", ""), [2]string{"Cookie", "token=$ADMIN"}))
	steps = append(steps, rq("healthz is public", "GET", "/healthz", "", ""))
	cases = append(cases, httpCaseOf("auth wrong place", steps...))

	// Auth-failure throttle: five failures in ten seconds block the source;
	// a valid credential is still accepted while blocked.
	steps = nil
	for i := 1; i <= 7; i++ {
		steps = append(steps, rq(fmt.Sprintf("wrong token %d", i), "GET", "/api/v1/status", "", ""))
	}
	steps = append(steps, rq("valid while blocked", "GET", "/api/v1/status", "ADMIN", ""))
	steps = append(steps, rq("worker valid while blocked", "GET", "/api/v1/status", "WORKER", ""))
	steps = append(steps, withHeaders(rq("wrong bearer while blocked", "GET", "/api/v1/files?path=Hello.md", "", ""), [2]string{"Authorization", "Bearer nope"}))
	cases = append(cases, httpCaseOf("auth throttle", steps...))

	// Credential rotation: the persisted legacy admin user keeps the first
	// token's hash, so the old admin token keeps working; the old worker
	// token stops.
	steps = nil
	steps = append(steps,
		rq("admin before", "GET", "/api/v1/status", "ADMIN", ""),
		rq("worker before", "POST", "/api/v1/worker/lease", "WORKER", `{"worker_id":"w1","capabilities":["ocr"]}`),
		rq("admin2 before", "GET", "/api/v1/status", "ADMIN2", ""),
		httpTreeStep(),
		httpStep{Label: "restart with rotated credentials", Kind: "restart", Rotate: true},
		rq("old admin", "GET", "/api/v1/status", "ADMIN", ""),
		rq("old worker", "POST", "/api/v1/worker/lease", "WORKER", `{"worker_id":"w1","capabilities":["ocr"]}`),
		rq("new admin", "GET", "/api/v1/status", "ADMIN2", ""),
		rq("new admin jobs", "GET", "/api/v1/jobs", "ADMIN2", ""),
		rq("new worker lease", "POST", "/api/v1/worker/lease", "WORKER2", `{"worker_id":"w1","capabilities":["ocr"]}`),
		rq("new worker admin route", "GET", "/api/v1/jobs", "WORKER2", ""),
		httpTreeStep(),
	)
	cases = append(cases, httpCaseOf("auth rotation", steps...))

	// Second restart with the same credentials does not recreate the admin.
	steps = nil
	steps = append(steps,
		rq("admin", "GET", "/api/v1/status", "ADMIN", ""),
		httpStep{Label: "remove users", Kind: "fs", Op: "remove", Path: ".symdesk/users.json"},
		httpStep{Label: "restart", Kind: "restart"},
		rq("admin after restart", "GET", "/api/v1/status", "ADMIN", ""),
		httpTreeStep(),
	)
	cases = append(cases, httpCaseOf("auth migration runs once", steps...))

	// Server without a separate worker token: the admin token is the worker.
	steps = nil
	steps = append(steps,
		rq("lease with admin token", "POST", "/api/v1/worker/lease", "ADMIN", `{"worker_id":"w1","capabilities":["ocr"]}`),
		rq("lease with unset worker token", "POST", "/api/v1/worker/lease", "WORKER", `{"worker_id":"w1","capabilities":["ocr"]}`),
	)
	c := httpCaseOf("auth no worker token", steps...)
	c.NoWorker = true
	cases = append(cases, c)

	// Named users across every route.
	steps = nil
	for _, route := range httpRoutes {
		for _, who := range []string{"NAMED", "WNAMED", "NAMEDADM"} {
			body := ""
			if route.method == "POST" || route.method == "PUT" {
				body = "{}"
			}
			target := route.target
			steps = append(steps, rq(fmt.Sprintf("%s %s auth=%s", route.method, target, strings.ToLower(who)), route.method, target, who, body))
		}
	}
	cases = append(cases, namedCase("named users routes", steps...))

	// Named users and document ACLs.
	steps = nil
	for _, who := range []string{"NAMED", "WNAMED", "NAMEDADM", "ADMIN"} {
		for _, path := range []string{"Hello.md", "nested/Note.md", "inbox/c.png", "notebooks/research.md", "internal.md"} {
			steps = append(steps, rq(fmt.Sprintf("GET %s as %s", path, strings.ToLower(who)), "GET", "/api/v1/files?path="+path, who, ""))
		}
		for _, path := range []string{"Hello.md", "nested/Note.md", "new/Created.md"} {
			steps = append(steps, rq(fmt.Sprintf("PUT %s as %s", path, strings.ToLower(who)), "PUT", "/api/v1/files?path="+path, who, "written by "+who))
		}
		steps = append(steps, rq("snapshot as "+strings.ToLower(who), "GET", "/api/v1/snapshot", who, ""))
		steps = append(steps, rq("notebook as "+strings.ToLower(who), "GET", "/api/v1/notebooks/research", who, ""))
	}
	steps = append(steps, httpStep{Label: "vault tree", Kind: "tree", TreeInclude: []string{"new/", ".symdesk/users.json", ".symdesk/permissions.json"}, Mask: []string{"hex", "time"}})
	cases = append(cases, namedCase("named users acl", steps...))

	// Token hash edge cases: a hash is not a token.
	steps = nil
	steps = append(steps,
		rq("token hash as credential", "GET", "/api/v1/status", "NAMED_HASH", ""),
		rq("uppercase token", "GET", "/api/v1/status", "", ""),
	)
	steps[1].Headers = [][2]string{{"Authorization", "Bearer " + "NAMED"}}
	cases = append(cases, namedCase("named users hash is no credential", steps...))
	return cases
}

// ---------------------------------------------------------------- HTTP-003

func httpFileCases() []httpCase {
	var cases []httpCase

	paths := []string{
		"Hello.md", "nested/Note.md", "missing.md", "", "/etc/passwd", "../outside.md", "a/../Hello.md", "./Hello.md",
		"nested", "nested/", "nested//Note.md", ".symdesk", ".symdesk/users.json", ".symdesk/server/jobs", "Hello.md%5C",
		"Hello.md%00", "internal.md", "escape.md", "inbox/c.png", "nested/Note.md/x", "%20Hello.md", "Hello.md%20",
		"a%2Fb.md", "Hello.md%3Fx", "CON", "a" + strings.Repeat("b", 300) + ".md",
	}
	var steps []httpStep
	for _, path := range paths {
		steps = append(steps, rq("GET path="+path, "GET", "/api/v1/files?path="+path, "ADMIN", ""))
	}
	steps = append(steps,
		rq("GET without path parameter", "GET", "/api/v1/files", "ADMIN", ""),
		rq("GET repeated path first wins", "GET", "/api/v1/files?path=Hello.md&path=missing.md", "ADMIN", ""),
		rq("HEAD Hello.md", "HEAD", "/api/v1/files?path=Hello.md", "ADMIN", ""),
		rq("HEAD missing", "HEAD", "/api/v1/files?path=missing.md", "ADMIN", ""),
		rq("worker reads files", "GET", "/api/v1/files?path=Hello.md", "WORKER", ""),
	)
	cases = append(cases, httpCaseOf("files get paths", steps...))

	// Conditional and range requests (http.ServeContent).
	date := "Fri, 02 Jan 2026 03:04:05 GMT" // the std mtime 1767323045
	steps = nil
	conditional := []struct {
		label   string
		target  string
		headers [][2]string
	}{
		{"range first bytes", "Hello.md", [][2]string{{"Range", "bytes=0-3"}}},
		{"range open end", "Hello.md", [][2]string{{"Range", "bytes=10-"}}},
		{"range suffix", "Hello.md", [][2]string{{"Range", "bytes=-4"}}},
		{"range unsatisfiable", "Hello.md", [][2]string{{"Range", "bytes=900-999"}}},
		{"range invalid syntax", "Hello.md", [][2]string{{"Range", "bytes=a-b"}}},
		{"range wrong unit", "Hello.md", [][2]string{{"Range", "items=0-1"}}},
		{"range multiple", "Hello.md", [][2]string{{"Range", "bytes=0-1,3-4"}}},
		{"range overlapping", "Hello.md", [][2]string{{"Range", "bytes=0-5,2-7"}}},
		{"if-modified-since equal", "Hello.md", [][2]string{{"If-Modified-Since", date}}},
		{"if-modified-since older", "Hello.md", [][2]string{{"If-Modified-Since", "Thu, 01 Jan 2026 00:00:00 GMT"}}},
		{"if-modified-since newer", "Hello.md", [][2]string{{"If-Modified-Since", "Sat, 03 Jan 2026 00:00:00 GMT"}}},
		{"if-modified-since invalid", "Hello.md", [][2]string{{"If-Modified-Since", "yesterday"}}},
		{"if-unmodified-since older", "Hello.md", [][2]string{{"If-Unmodified-Since", "Thu, 01 Jan 2026 00:00:00 GMT"}}},
		{"if-range matching date", "Hello.md", [][2]string{{"Range", "bytes=0-3"}, {"If-Range", date}}},
		{"if-range stale date", "Hello.md", [][2]string{{"Range", "bytes=0-3"}, {"If-Range", "Thu, 01 Jan 2026 00:00:00 GMT"}}},
		{"if-range etag form", "Hello.md", [][2]string{{"Range", "bytes=0-3"}, {"If-Range", `"abc"`}}},
		{"if-none-match star", "Hello.md", [][2]string{{"If-None-Match", "*"}}},
		{"if-match star", "Hello.md", [][2]string{{"If-Match", "*"}}},
		{"accept-encoding gzip ignored", "Hello.md", [][2]string{{"Accept-Encoding", "gzip"}}},
		{"png content type", "inbox/c.png", nil},
		{"png range", "inbox/c.png", [][2]string{{"Range", "bytes=0-5"}}},
	}
	for _, form := range conditional {
		step := rq("GET "+form.label, "GET", "/api/v1/files?path="+form.target, "ADMIN", "")
		step.Headers = append(step.Headers, form.headers...)
		steps = append(steps, step)
	}
	head := rq("HEAD range", "HEAD", "/api/v1/files?path=Hello.md", "ADMIN", "")
	head.Headers = append(head.Headers, [2]string{"Range", "bytes=0-3"})
	steps = append(steps, head)
	cases = append(cases, httpCaseOf("files get preconditions", steps...))

	// PUT /api/v1/files.
	steps = nil
	puts := []struct{ label, path, body string }{
		{"new note", "put/New.md", "---\ntitle: New\n---\nbody"},
		{"overwrite", "Hello.md", "replaced"},
		{"empty body", "put/Empty.md", ""},
		{"upper-case extension", "put/Upper.MD", "upper"},
		{"mixed-case extension", "put/Mixed.Md", "mixed"},
		{"non markdown", "put/data.txt", "text"},
		{"no extension", "put/noext", "x"},
		{"markdown suffix only", "put/.md", "dotfile"},
		{"empty path", "", "x"},
		{"absolute path", "/abs.md", "x"},
		{"parent traversal", "../escape.md", "x"},
		{"internal state", ".symdesk/x.md", "x"},
		{"nested directories", "deep/er/est/Note.md", "deep"},
		{"directory target", "nested.md/", "x"},
		{"over a directory", "nested", "x"},
		{"symlink parent escape", "escape.md/x.md", "x"},
		{"through symlink file", "internal.md", "through link"},
		{"unicode body", "put/Unicode.md", "Grüße ✓ \u2028"},
	}
	for _, put := range puts {
		steps = append(steps, rq("PUT "+put.label, "PUT", "/api/v1/files?path="+put.path, "ADMIN", put.body))
	}
	steps = append(steps,
		rq("PUT without credentials", "PUT", "/api/v1/files?path=put/Anon.md", "", "x"),
		rq("PUT with worker token", "PUT", "/api/v1/files?path=put/Worker.md", "WORKER", "worker wrote"),
		rq("readback new note", "GET", "/api/v1/files?path=put/New.md", "ADMIN", ""),
		rq("readback overwrite", "GET", "/api/v1/files?path=Hello.md", "ADMIN", ""),
		httpStep{Label: "vault tree", Kind: "tree", TreeInclude: []string{"put/", "deep/", "Hello.md"}, Mask: []string{"hex", "time"}},
	)
	chunked := httpStep{Label: "PUT chunked body", Method: "PUT", Target: "/api/v1/files?path=put/Chunked.md", Headers: append(httpAuth("ADMIN"), [2]string{"Transfer-Encoding", "chunked"}), Body: "5\r\nhello\r\n6\r\n world\r\n0\r\n\r\n", Mask: httpVolatile, VolatileLength: true}
	steps = append(steps, chunked)
	expect := rq("PUT expect continue", "PUT", "/api/v1/files?path=put/Expect.md", "ADMIN", "expect body")
	expect.Headers = append(expect.Headers, [2]string{"Expect", "100-continue"})
	steps = append(steps, expect)
	steps = append(steps, httpStep{Label: "vault tree after chunked", Kind: "tree", TreeInclude: []string{"put/"}, Mask: []string{"hex", "time"}})
	cases = append(cases, httpCaseOf("files put", steps...))

	// Limits: 8 MiB is accepted, one more byte is not.
	limit := rq("PUT exactly 8 MiB", "PUT", "/api/v1/files?path=limit/Exact.md", "ADMIN", "")
	limit.BodyRepeat = &httpRepeat{Text: "a", Count: 8 << 20}
	limit.ReadMS = 90000 // indexing 8 MiB in an unoptimised Rust build takes seconds
	over := rq("PUT 8 MiB plus one byte", "PUT", "/api/v1/files?path=limit/Over.md", "ADMIN", "")
	over.BodyRepeat = &httpRepeat{Text: "a", Count: 8<<20 + 1}
	over.ReadMS = 15000
	cases = append(cases, httpCaseOf("files put limits", limit, over,
		httpStep{Label: "vault tree", Kind: "tree", Mask: []string{"hex", "time"}},
	))

	// Snapshot.
	steps = nil
	steps = append(steps,
		func() httpStep {
			s := rq("snapshot plain", "GET", "/api/v1/snapshot", "ADMIN", "")
			s.Capture = map[string]string{"SNAPETAG": "header:etag"}
			s.Mask = append([]string{"nobody"}, httpVolatile...)
			return s
		}(),
		func() httpStep {
			s := rq("snapshot gzip", "GET", "/api/v1/snapshot", "ADMIN", "")
			s.Headers = append(s.Headers, [2]string{"Accept-Encoding", "gzip"})
			s.Mask = append([]string{"nobody"}, httpVolatile...)
			return s
		}(),
		func() httpStep {
			s := rq("snapshot not modified", "GET", "/api/v1/snapshot", "ADMIN", "")
			s.Headers = append(s.Headers, [2]string{"If-None-Match", "$SNAPETAG"})
			return s
		}(),
		func() httpStep {
			s := rq("snapshot stale etag", "GET", "/api/v1/snapshot", "ADMIN", "")
			s.Headers = append(s.Headers, [2]string{"If-None-Match", `"stale"`})
			s.Mask = append([]string{"nobody"}, httpVolatile...)
			return s
		}(),
		httpStep{Label: "write a note", Kind: "fs", Op: "write", Path: "added/Later.md", Content: "later"},
		func() httpStep {
			s := rq("snapshot after write", "GET", "/api/v1/snapshot", "ADMIN", "")
			s.Headers = append(s.Headers, [2]string{"If-None-Match", "$SNAPETAG"})
			s.Mask = append([]string{"nobody"}, httpVolatile...)
			return s
		}(),
		rq("snapshot head", "HEAD", "/api/v1/snapshot", "ADMIN", ""),
		rq("snapshot post", "POST", "/api/v1/snapshot", "ADMIN", "{}"),
	)
	cases = append(cases, httpCaseOf("snapshot", steps...))
	return cases
}

func httpCommandCases() []httpCase {
	command := func(label, who, body string) httpStep { return rq(label, "POST", "/api/v1/command", who, body) }
	steps := []httpStep{
		command("invalid json", "ADMIN", "{"),
		command("empty body", "ADMIN", ""),
		command("unknown field", "ADMIN", `{"arguments":["ls"],"x":1}`),
		command("null arguments", "ADMIN", `{"arguments":null}`),
		command("empty arguments", "ADMIN", `{"arguments":[]}`),
		command("trailing data", "ADMIN", `{"arguments":["ls"]} {"x":1}`),
		command("string arguments", "ADMIN", `{"arguments":"ls"}`),
		command("not allowed command", "ADMIN", `{"arguments":["rm","-rf","x"]}`),
		command("not allowed subcommand", "ADMIN", `{"arguments":["note","purge"]}`),
		command("missing subcommand", "ADMIN", `{"arguments":["note"]}`),
		command("flag as subcommand", "ADMIN", `{"arguments":["note","--json"]}`),
		command("vault flag", "ADMIN", `{"arguments":["ls","--vault","/tmp"]}`),
		command("vault flag equals", "ADMIN", `{"arguments":["ls","--VAULT=/tmp"]}`),
		command("output flag", "ADMIN", `{"arguments":["ls","--output","/tmp/x"]}`),
		command("output flag equals", "ADMIN", `{"arguments":["ls","--Output=/tmp/x"]}`),
		command("ls", "ADMIN", `{"arguments":["ls"]}`),
		command("ls json flag given", "ADMIN", `{"arguments":["ls","--json"]}`),
		command("search", "ADMIN", `{"arguments":["search","Body"]}`),
		command("search without query fails", "ADMIN", `{"arguments":["search"]}`),
		command("stdin is passed", "ADMIN", `{"arguments":["ls"],"stdin":"ignored"}`),
		command("worker token", "WORKER", `{"arguments":["ls"]}`),
		command("no credentials", "", `{"arguments":["ls"]}`),
		rq("get method", "GET", "/api/v1/command", "ADMIN", ""),
	}
	big := command("body over 2 MiB", "ADMIN", "")
	big.Body = `{"arguments":["ls"],"stdin":"`
	big.BodyRepeat = &httpRepeat{Text: "a", Count: 2<<20 + 16}
	big.Body += ""
	steps = append(steps, big)
	steps = append(steps, httpStep{Label: "vault tree", Kind: "tree", Mask: []string{"hex", "time"}})
	cases := []httpCase{httpCaseOf("command", steps...)}
	cases = append(cases, namedCase("command named users",
		command("user", "NAMED", `{"arguments":["ls"]}`),
		command("worker", "WNAMED", `{"arguments":["ls"]}`),
		command("admin role", "NAMEDADM", `{"arguments":["ls"]}`),
	))
	return cases
}

// ---------------------------------------------------------------- HTTP-005

func httpShareCases() []httpCase {
	var cases []httpCase
	create := func(label, who, body string) httpStep { return rq(label, "POST", "/api/v1/share", who, body) }

	steps := []httpStep{
		func() httpStep {
			s := create("create", "ADMIN", `{"path":"Hello.md","expiry":1}`)
			s.Capture = map[string]string{"SHAREID": "id", "SHARETOK": "token"}
			return s
		}(),
		rq("list after create", "GET", "/api/v1/shares", "ADMIN", ""),
		rq("access", "GET", "/s/$SHARETOK", "", ""),
		rq("access head", "HEAD", "/s/$SHARETOK", "", ""),
		func() httpStep {
			s := rq("access range", "GET", "/s/$SHARETOK", "", "")
			s.Headers = append(s.Headers, [2]string{"Range", "bytes=0-3"})
			return s
		}(),
		func() httpStep {
			s := rq("access ignores credentials", "GET", "/s/$SHARETOK", "WORKER", "")
			return s
		}(),
		rq("access post", "POST", "/s/$SHARETOK", "", "{}"),
		httpStep{Label: "vault tree", Kind: "tree", TreeInclude: []string{".symdesk/server/shares.json"}, Mask: []string{"hex", "time"}},
		rq("revoke", "DELETE", "/api/v1/share/$SHAREID", "ADMIN", ""),
		rq("revoke again", "DELETE", "/api/v1/share/$SHAREID", "ADMIN", ""),
		rq("access after revoke", "GET", "/s/$SHARETOK", "", ""),
		rq("list after revoke", "GET", "/api/v1/shares", "ADMIN", ""),
		httpStep{Label: "vault tree after revoke", Kind: "tree", TreeInclude: []string{".symdesk/server/shares.json"}, Mask: []string{"hex", "time"}},
	}
	cases = append(cases, httpCaseOf("shares lifecycle", steps...))

	steps = nil
	bodies := []struct{ label, body string }{
		{"invalid json", "{"},
		{"empty body", ""},
		{"null", "null"},
		{"missing path", `{"expiry":1}`},
		{"empty path", `{"path":"","expiry":1}`},
		{"expiry zero", `{"path":"Hello.md","expiry":0}`},
		{"expiry negative", `{"path":"Hello.md","expiry":-1}`},
		{"expiry too large", `{"path":"Hello.md","expiry":169}`},
		{"expiry fractional", `{"path":"Hello.md","expiry":1.5}`},
		{"expiry string", `{"path":"Hello.md","expiry":"2"}`},
		{"expiry missing", `{"path":"Hello.md"}`},
		{"path number", `{"path":5,"expiry":1}`},
		{"missing document", `{"path":"missing.md","expiry":1}`},
		{"directory", `{"path":"nested","expiry":1}`},
		{"traversal", `{"path":"../outside.md","expiry":1}`},
		{"absolute", `{"path":"/etc/passwd","expiry":1}`},
		{"internal state", `{"path":".symdesk/users.json","expiry":1}`},
		{"backslash", `{"path":"a\\b.md","expiry":1}`},
		{"dataset path", `{"path":"datasets/x.csv","expiry":1}`},
		{"dataset root", `{"path":"datasets","expiry":1}`},
		{"symlink escape", `{"path":"escape.md","expiry":1}`},
		{"symlink inside", `{"path":"internal.md","expiry":168}`},
		{"unknown field allowed", `{"path":"nested/Note.md","expiry":2,"extra":true}`},
		{"trailing data allowed", `{"path":"Hello.md","expiry":3} garbage`},
	}
	for _, form := range bodies {
		steps = append(steps, create("create "+form.label, "ADMIN", form.body))
	}
	steps = append(steps,
		create("create as worker", "WORKER", `{"path":"Hello.md","expiry":1}`),
		create("create unauthenticated", "", `{"path":"Hello.md","expiry":1}`),
		rq("list", "GET", "/api/v1/shares", "ADMIN", ""),
		httpStep{Label: "vault tree", Kind: "tree", TreeInclude: []string{".symdesk/server/shares.json"}, Mask: []string{"hex", "time"}},
	)
	c := httpCaseOf("shares create validation", steps...)
	c.Files["datasets/x.csv"] = "a,b\n1,2\n"
	cases = append(cases, c)

	// Legacy store: expired links, a dataset-backed link, and plaintext
	// `token` fields that are cleared when the server starts.
	legacy := `[{"id":"aaaaaaaaaaaaaaaaaaaaaaaa","path":"Hello.md","created_by":"admin","created_at":"2026-01-01T00:00:00Z","expires_at":"2020-01-01T00:00:00Z","token_hash":"$SHARE_HASH","token":"$SHARE"},` +
		`{"id":"bbbbbbbbbbbbbbbbbbbbbbbb","path":"nested/Note.md","created_by":"carol","created_at":"2026-01-02T00:00:00Z","expires_at":"2999-01-01T00:00:00Z","token_hash":"$SHARE2_HASH","token":"legacy-plain-token"},` +
		`{"id":"cccccccccccccccccccccccc","path":"datasets/x.csv","created_by":"admin","created_at":"2026-01-03T00:00:00Z","expires_at":"2999-01-01T00:00:00Z","token_hash":"` + "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" + `"}]`
	steps = []httpStep{
		httpStep{Label: "store before start", Kind: "tree", TreeInclude: []string{".symdesk/server/shares.json"}, Mask: []string{"hex", "time"}},
		rq("expired link", "GET", "/s/$SHARE", "", ""),
		rq("active link", "GET", "/s/$SHARE2", "", ""),
		rq("list as admin", "GET", "/api/v1/shares", "ADMIN", ""),
		rq("revoke expired link", "DELETE", "/api/v1/share/aaaaaaaaaaaaaaaaaaaaaaaa", "ADMIN", ""),
		httpStep{Label: "store after revoke", Kind: "tree", TreeInclude: []string{".symdesk/server/shares.json"}, Mask: []string{"hex", "time"}},
		httpStep{Label: "restart", Kind: "restart"},
		httpStep{Label: "store after restart", Kind: "tree", TreeInclude: []string{".symdesk/server/shares.json"}, Mask: []string{"hex", "time"}},
	}
	c = httpCaseOf("shares legacy store", steps...)
	c.Files[".symdesk/server/shares.json"] = legacy
	c.Files["datasets/x.csv"] = "a,b\n1,2\n"
	cases = append(cases, c)

	// Public access throttling: three misses in thirty seconds block the source.
	steps = nil
	for i := 1; i <= 5; i++ {
		steps = append(steps, rq(fmt.Sprintf("unknown token %d", i), "GET", fmt.Sprintf("/s/unknown-%d", i), "", ""))
	}
	steps = append(steps,
		rq("list blocked source", "GET", "/api/v1/shares", "ADMIN", ""),
	)
	cases = append(cases, httpCaseOf("shares access throttle", steps...))

	// Named users.
	steps = []httpStep{
		func() httpStep {
			s := create("alice creates", "NAMED", `{"path":"nested/Note.md","expiry":2}`)
			s.Capture = map[string]string{"ALICEID": "id", "ALICETOK": "token"}
			return s
		}(),
		create("alice creates denied path", "NAMED", `{"path":"Hello.md","expiry":2}`),
		create("worker-user creates", "WNAMED", `{"path":"nested/Note.md","expiry":2}`),
		func() httpStep {
			s := create("carol creates", "NAMEDADM", `{"path":"Hello.md","expiry":2}`)
			s.Capture = map[string]string{"CAROLID": "id", "CAROLTOK": "token"}
			return s
		}(),
		rq("alice lists own", "GET", "/api/v1/shares", "NAMED", ""),
		rq("carol lists all", "GET", "/api/v1/shares", "NAMEDADM", ""),
		rq("worker-user lists none", "GET", "/api/v1/shares", "WNAMED", ""),
		rq("alice revokes carol's", "DELETE", "/api/v1/share/$CAROLID", "NAMED", ""),
		rq("worker-user revokes alice's", "DELETE", "/api/v1/share/$ALICEID", "WNAMED", ""),
		rq("public access to alice's", "GET", "/s/$ALICETOK", "", ""),
		rq("alice revokes own", "DELETE", "/api/v1/share/$ALICEID", "NAMED", ""),
		rq("alice revokes own again", "DELETE", "/api/v1/share/$ALICEID", "NAMED", ""),
		rq("carol revokes any", "DELETE", "/api/v1/share/$ALICEID", "NAMEDADM", ""),
		rq("public access after revoke", "GET", "/s/$ALICETOK", "", ""),
		rq("revoke with empty id", "DELETE", "/api/v1/share/", "NAMEDADM", ""),
		rq("carol lists after", "GET", "/api/v1/shares", "NAMEDADM", ""),
	}
	cases = append(cases, namedCase("shares named users", steps...))
	return cases
}
