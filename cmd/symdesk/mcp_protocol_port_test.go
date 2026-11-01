package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// MCP-001: raw initialize/ping/notification/framing transcripts recorded from
// the real Go `symdesk mcp` process. Every case feeds raw bytes to stdin and
// pins the exact stdout/stderr/exit code of the Go process.

// mcpProtocolGen describes an input too large to store literally. Kind is one
// of pad-line, pad-frame, garbage-line, garbage-line-then-ping,
// oversized-frame-then-ping or deep-line; N is the size parameter.
type mcpProtocolGen struct {
	Kind string `json:"kind"`
	N    int    `json:"n"`
}

type mcpProtocolCase struct {
	ID         string          `json:"id"`
	AllowWrite bool            `json:"allow_write,omitempty"`
	Input      string          `json:"input"`
	InputHex   string          `json:"input_hex,omitempty"`
	Gen        *mcpProtocolGen `json:"generate,omitempty"`
	Stdout     string          `json:"stdout"`
	Stderr     string          `json:"stderr"`
	ExitCode   int             `json:"exit_code"`
}

type mcpProtocolFixture struct {
	SchemaVersion int               `json:"schema_version"`
	Cases         []mcpProtocolCase `json:"cases"`
}

func mcpFrame(body string) string {
	return fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)
}

func mcpProtocolCases() []mcpProtocolCase {
	var cases []mcpProtocolCase
	add := func(id, input string) {
		cases = append(cases, mcpProtocolCase{ID: id, Input: input})
	}
	line := func(id, request string) { add(id, request+"\n") }

	// Request identifiers of every JSON type.
	for i, id := range []string{`1`, `0`, `-1`, `"a"`, `""`, `null`, `1.5`, `1e3`, `-0`, `12345678901234567890`, `true`, `false`, `[]`, `{}`, `[1,"x"]`, `{"a":1}`, `"\u00e9\"\\\n\u2028"`, `"<&>"`, `1E2`, `0.0`, `100000000000000000000000000000000000000`} {
		line(fmt.Sprintf("init-id-%02d", i), `{"jsonrpc":"2.0","id":`+id+`,"method":"initialize"}`)
		line(fmt.Sprintf("ping-id-%02d", i), `{"jsonrpc":"2.0","id":`+id+`,"method":"ping"}`)
	}
	// Initialize parameters.
	for i, params := range []string{``, `,"params":null`, `,"params":{}`, `,"params":[]`, `,"params":"str"`, `,"params":5`, `,"params":true`,
		`,"params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"x","version":"1"}}`,
		`,"params":{"protocolVersion":"1999-01-01"}`, `,"params":{"protocolVersion":7}`, `,"params":{"protocolVersion":null,"capabilities":null}`,
		`,"params":{"capabilities":{"roots":{"listChanged":true},"sampling":{}},"clientInfo":{"name":"é","version":""}}`,
		`,"params":{"unknown":[1,2,3]}`, `,"params":{"protocolVersion":"2025-03-26"}`} {
		line(fmt.Sprintf("init-params-%02d", i), `{"jsonrpc":"2.0","id":1,"method":"initialize"`+params+`}`)
	}
	// jsonrpc member variants.
	for i, rpc := range []string{``, `"jsonrpc":"1.0",`, `"jsonrpc":2,`, `"jsonrpc":null,`, `"jsonrpc":"2.0","jsonrpc":"1.0",`, `"JSONRPC":"2.0",`, `"jsonrpc":"",`, `"jsonrpc":[],`} {
		line(fmt.Sprintf("init-jsonrpc-%02d", i), `{`+rpc+`"id":1,"method":"initialize"}`)
	}
	// Method spelling and types.
	for i, method := range []string{`"initialize"`, `"Initialize"`, `"initialize "`, `" initialize"`, `"INITIALIZE"`, `"ping"`, `"Ping"`, `""`, `null`, `1`, `true`, `[]`, `{}`, `"initialized"`, `"notifications/initialized"`, `"notifications/cancelled"`, `"notifications/progress"`, `"tools/list"`, `"tools/call"`, `"resources/list"`, `"resources/read"`, `"resources/templates/list"`, `"prompts/list"`, `"prompts/get"`, `"completion/complete"`, `"logging/setLevel"`, `"roots/list"`, `"sampling/createMessage"`, `"shutdown"`, `"exit"`, `"rpc.discover"`, `"initialize\u0000"`, `"\u00e9"`} {
		line(fmt.Sprintf("method-id-%02d", i), `{"jsonrpc":"2.0","id":7,"method":`+method+`}`)
		line(fmt.Sprintf("method-notification-%02d", i), `{"jsonrpc":"2.0","method":`+method+`}`)
		line(fmt.Sprintf("method-null-id-%02d", i), `{"jsonrpc":"2.0","id":null,"method":`+method+`}`)
	}
	// Notifications carrying params or an id.
	line("notif-initialized-with-id", `{"jsonrpc":"2.0","id":3,"method":"notifications/initialized"}`)
	line("notif-initialized-params", `{"jsonrpc":"2.0","method":"notifications/initialized","params":{"x":1}}`)
	line("notif-cancelled-id", `{"jsonrpc":"2.0","id":4,"method":"notifications/cancelled","params":{"requestId":3}}`)
	line("notif-cancelled-null-params", `{"jsonrpc":"2.0","method":"notifications/cancelled","params":null}`)
	line("ping-with-params", `{"jsonrpc":"2.0","id":5,"method":"ping","params":{"a":[1,2]}}`)
	line("ping-bad-params", `{"jsonrpc":"2.0","id":5,"method":"ping","params":7}`)
	line("tools-list-bad-params", `{"jsonrpc":"2.0","id":5,"method":"tools/list","params":"x"}`)
	line("tools-call-no-params", `{"jsonrpc":"2.0","id":5,"method":"tools/call"}`)
	line("tools-call-null-params", `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":null}`)
	line("tools-call-array-params", `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":[]}`)
	line("tools-call-name-number", `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":5}}`)
	line("tools-call-name-missing", `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"arguments":{}}}`)
	line("tools-call-name-unknown", `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"nope","arguments":{}}}`)
	line("tools-call-unknown-notification", `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"nope","arguments":{}}}`)

	// Invalid and malformed JSON-RPC documents.
	for i, doc := range []string{`[]`, `[{"jsonrpc":"2.0","id":1,"method":"ping"}]`, `[{"jsonrpc":"2.0","id":1,"method":"ping"},{"jsonrpc":"2.0","method":"ping"}]`, `null`, `1`, `"str"`, `true`, `{}`, `{"id":1}`, `{"jsonrpc":"2.0"}`, `{"jsonrpc":"2.0","id":1}`, `{"id":1,"method":"ping"}`, `{"method":"ping"}`,
		`{`, `}`, `{"a":}`, `{"jsonrpc":"2.0","id":1,"method":`, `{"jsonrpc":"2.0","id":1,"method":"ping"} x`, `{"jsonrpc":"2.0","id":1,"method":"ping"}{"jsonrpc":"2.0","id":2,"method":"ping"}`, `{"jsonrpc":"2.0","id":1,"method":"ping"},`, `{'jsonrpc':'2.0'}`, `{"jsonrpc":"2.0","id":1,"method":"ping",}`, `{"jsonrpc":"2.0","id":01,"method":"ping"}`, `{"jsonrpc":"2.0","id":+1,"method":"ping"}`, `{"jsonrpc":"2.0","id":NaN,"method":"ping"}`, `{"jsonrpc":"2.0","id":.5,"method":"ping"}`, `{"jsonrpc":"2.0","id":1,"method":"ping","method":"initialize"}`, `{"jsonrpc":"2.0","id":1,"id":2,"method":"ping"}`, `{"jsonrpc":"2.0","id":"x","Method":"ping"}`, `{"jsonrpc":"2.0","ID":9,"method":"ping"}`, `{"jsonrpc":"2.0","id":1,"method":"ping","extra":{"deep":[[[[1]]]]}}`, `"\ud800"`, `{"jsonrpc":"2.0","id":"\ud800","method":"ping"}`, `{"jsonrpc":"2.0","id":1,"method":"pi\ud800ng"}`, `{"jsonrpc":"2.0","id":1,"method":"ping"}` + strings.Repeat(" ", 10), `   {"jsonrpc":"2.0","id":1,"method":"ping"}`, "\t{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}", "\ufeff{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}", "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"pi\\u006eg\"}"} {
		line(fmt.Sprintf("invalid-%02d", i), doc)
	}
	for _, depth := range []int{5000, 9998, 9999, 10000, 20000} {
		cases = append(cases, mcpProtocolCase{ID: fmt.Sprintf("deep-nesting-%d", depth), Gen: &mcpProtocolGen{Kind: "deep-line", N: depth}})
	}

	// Line framing: blanks, CRLF, no trailing newline, NUL and several messages.
	add("line-empty-input", "")
	add("line-only-newline", "\n")
	add("line-many-newlines", "\n\n\n")
	add("line-only-spaces", "   \n")
	add("line-spaces-then-request", "   \n\n{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}\n")
	add("line-crlf", "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}\r\n")
	add("line-crlf-two", "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}\r\n{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"ping\"}\r\n")
	add("line-cr-only", "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}\r{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"ping\"}\r")
	add("line-no-trailing-newline", `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	add("line-no-trailing-newline-garbage", `{"jsonrpc":"2.0","id":1,"method":"pi`)
	add("line-nul", "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}\x00\n")
	add("line-nul-first", "\x00{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}\n")
	add("line-two-requests", "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}\n{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"initialize\"}\n")
	add("line-five-mixed", "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\"}\n{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"ping\"}\n{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"nope\"}\n{\"jsonrpc\":\"2.0\",\"id\":4,\"method\":\"tools/list\"}\n")
	add("line-bad-then-good", "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\n{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"ping\"}\n")
	add("line-good-then-bad-then-good", "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}\n{oops\n{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"ping\"}\n")
	add("line-notifications-only", strings.Repeat("{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n", 5))
	add("line-then-framed", "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}\n"+mcpFrame(`{"jsonrpc":"2.0","id":2,"method":"ping"}`))
	add("framed-then-line", mcpFrame(`{"jsonrpc":"2.0","id":2,"method":"ping"}`)+"{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}\n")
	add("line-embedded-frame-header", "Content-Length: 3\n{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}\n")
	add("line-unicode-id", "{\"jsonrpc\":\"2.0\",\"id\":\"日本語\",\"method\":\"ping\"}\n")

	// Content-Length framing.
	ping := `{"jsonrpc":"2.0","id":1,"method":"ping"}`
	init := `{"jsonrpc":"2.0","id":1,"method":"initialize"}`
	add("frame-ping", mcpFrame(ping))
	add("frame-initialize", mcpFrame(init))
	add("frame-two", mcpFrame(ping)+mcpFrame(init))
	add("frame-notification", mcpFrame(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	add("frame-notification-then-ping", mcpFrame(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)+mcpFrame(ping))
	add("frame-extra-header", "Content-Type: application/vscode-jsonrpc; charset=utf-8\r\nContent-Length: "+fmt.Sprint(len(ping))+"\r\n\r\n"+ping)
	add("frame-extra-header-after", "Content-Length: "+fmt.Sprint(len(ping))+"\r\nContent-Type: x\r\n\r\n"+ping)
	add("frame-lowercase", "content-length: "+fmt.Sprint(len(ping))+"\r\n\r\n"+ping)
	add("frame-mixed-case", "CoNtEnT-lEnGtH: "+fmt.Sprint(len(ping))+"\r\n\r\n"+ping)
	add("frame-no-space", "Content-Length:"+fmt.Sprint(len(ping))+"\r\n\r\n"+ping)
	add("frame-many-spaces", "Content-Length:    "+fmt.Sprint(len(ping))+"   \r\n\r\n"+ping)
	add("frame-tab", "Content-Length:\t"+fmt.Sprint(len(ping))+"\r\n\r\n"+ping)
	add("frame-space-before-colon", "Content-Length : "+fmt.Sprint(len(ping))+"\r\n\r\n"+ping)
	add("frame-lf-only", "Content-Length: "+fmt.Sprint(len(ping))+"\n\n"+ping)
	add("frame-lf-crlf-mix", "Content-Length: "+fmt.Sprint(len(ping))+"\r\n\n"+ping)
	add("frame-cr-lf-lf", "Content-Length: "+fmt.Sprint(len(ping))+"\n\r\n"+ping)
	add("frame-duplicate-same", "Content-Length: "+fmt.Sprint(len(ping))+"\r\nContent-Length: "+fmt.Sprint(len(ping))+"\r\n\r\n"+ping)
	add("frame-duplicate-different", "Content-Length: 3\r\nContent-Length: "+fmt.Sprint(len(ping))+"\r\n\r\n"+ping)
	add("frame-duplicate-different-reverse", "Content-Length: "+fmt.Sprint(len(ping))+"\r\nContent-Length: 3\r\n\r\n"+ping)
	for i, n := range []string{`+5`, `-1`, `0`, `00`, `1e3`, `0x10`, ` 5`, `5 5`, `nope`, ``, `99999999999999999999`, `18446744073709551616`, `9223372036854775807`, `-9223372036854775808`, `1048576`, `1048577`, `4294967296`, `٣`, `5.0`, `５`} {
		add(fmt.Sprintf("frame-length-%02d", i), "Content-Length: "+n+"\r\n\r\n"+ping)
	}
	add("frame-length-zero-then-ping", "Content-Length: 0\r\n\r\n"+mcpFrame(ping))
	add("frame-missing-length", "Content-Type: x\r\n\r\n"+ping)
	add("frame-header-only", "Content-Length: 5\r\n")
	add("frame-header-no-blank", "Content-Length: 5")
	add("frame-header-then-eof-after-blank", "Content-Length: 5\r\n\r\n")
	add("frame-body-short", "Content-Length: 100\r\n\r\n"+ping)
	add("frame-body-long", "Content-Length: 5\r\n\r\n"+ping)
	add("frame-body-exact-then-junk", mcpFrame(ping)+"junk")
	add("frame-body-exact-then-newline", mcpFrame(ping)+"\n")
	add("frame-body-exact-then-crlf", mcpFrame(ping)+"\r\n")
	add("frame-body-invalid-json", mcpFrame(`{"jsonrpc":`))
	add("frame-body-array", mcpFrame(`[]`))
	add("frame-body-batch", mcpFrame(`[`+ping+`]`))
	add("frame-body-empty-object", mcpFrame(`{}`))
	add("frame-body-null", mcpFrame(`null`))
	add("frame-body-whitespace", mcpFrame(`  `))
	add("frame-body-newline-inside", mcpFrame("{\"jsonrpc\":\"2.0\",\n\"id\":1,\"method\":\"ping\"}"))
	add("frame-body-trailing-newline-counted", mcpFrame(ping+"\n"))
	add("frame-body-multibyte", mcpFrame(`{"jsonrpc":"2.0","id":"日本","method":"ping"}`))
	add("frame-body-length-chars-not-bytes", "Content-Length: 40\r\n\r\n"+`{"jsonrpc":"2.0","id":"日本","method":"ping"}`)
	add("frame-header-no-colon", "Content-Length 5\r\n\r\n"+ping)
	add("frame-header-garbage-line", "Garbage\r\nContent-Length: "+fmt.Sprint(len(ping))+"\r\n\r\n"+ping)
	add("frame-header-empty-name", ": x\r\nContent-Length: "+fmt.Sprint(len(ping))+"\r\n\r\n"+ping)
	add("frame-header-obsolete-fold", "Content-Length: "+fmt.Sprint(len(ping))+"\r\n x\r\n\r\n"+ping)
	add("frame-header-leading-space", " Content-Length: "+fmt.Sprint(len(ping))+"\r\n\r\n"+ping)
	add("frame-header-utf8-name", "Content-Léngth: 3\r\nContent-Length: "+fmt.Sprint(len(ping))+"\r\n\r\n"+ping)
	add("frame-bom", "\ufeff"+mcpFrame(ping))
	add("frame-leading-blank-lines", "\r\n\r\n"+mcpFrame(ping))
	add("frame-leading-newline", "\n"+mcpFrame(ping))
	add("frame-three-notifications-then-init", strings.Repeat(mcpFrame(`{"jsonrpc":"2.0","method":"notifications/initialized"}`), 3)+mcpFrame(init))
	add("frame-bad-then-good", "Content-Length: nope\r\n\r\n"+mcpFrame(ping))
	add("frame-garbage-body-then-good", "Content-Length: 3\r\n\r\nabc"+mcpFrame(ping))
	add("frame-1mib-exact-boundary-body", "")
	cases = cases[:len(cases)-1]

	// Size limits: the inputs are generated from (kind, n) by both languages.
	const limit = 1 << 20
	gen := func(id, kind string, n int) {
		cases = append(cases, mcpProtocolCase{ID: id, Gen: &mcpProtocolGen{Kind: kind, N: n}})
	}
	for _, total := range []int{limit - 1, limit, limit + 1} {
		gen(fmt.Sprintf("line-size-%d", total), "pad-line", total)
		gen(fmt.Sprintf("frame-size-%d", total), "pad-frame", total)
	}
	gen("line-size-long-garbage", "garbage-line", limit+10)
	add("frame-claim-over-limit-short-body", fmt.Sprintf("Content-Length: %d\r\n\r\n%s", limit+1, ping))
	add("frame-claim-limit-short-body", fmt.Sprintf("Content-Length: %d\r\n\r\n%s", limit, ping))
	add("frame-claim-huge", "Content-Length: 4294967295\r\n\r\n"+ping)
	gen("line-size-oversized-then-ping", "garbage-line-then-ping", limit+10)
	gen("frame-oversized-then-ping", "oversized-frame-then-ping", limit+1)

	// Both modes of the registry answer initialize identically.
	cases = append(cases, mcpProtocolCase{ID: "init-read-write", AllowWrite: true, Input: init + "\n"})
	cases = append(cases, mcpProtocolCase{ID: "init-read-write-framed", AllowWrite: true, Input: mcpFrame(init)})

	// Non-UTF-8 and binary inputs use hex because JSON strings cannot carry them.
	hexCase := func(id string, raw []byte) {
		cases = append(cases, mcpProtocolCase{ID: id, InputHex: fmt.Sprintf("%x", raw)})
	}
	hexCase("line-invalid-utf8-id", []byte("{\"jsonrpc\":\"2.0\",\"id\":\"\xff\",\"method\":\"ping\"}\n"))
	hexCase("line-invalid-utf8-method", []byte("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"pi\xffng\"}\n"))
	hexCase("line-invalid-utf8-garbage", []byte("\xff\xfe\n{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}\n"))
	hexCase("frame-invalid-utf8-body", []byte("Content-Length: 40\r\n\r\n{\"jsonrpc\":\"2.0\",\"id\":\"\xff\",\"method\":\"ping\"}"))
	hexCase("frame-invalid-utf8-header", []byte("Content-Length: \xff\r\n\r\n{}"))
	hexCase("frame-binary-header", []byte("\x00\x01\x02\r\n\r\n"))
	hexCase("line-overlong-utf8", []byte("{\"jsonrpc\":\"2.0\",\"id\":\"\xc0\xaf\",\"method\":\"ping\"}\n"))
	return cases
}

func mcpProtocolPad(total int) string {
	prefix := `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"pad":"`
	suffix := `"}}`
	return prefix + strings.Repeat("a", total-len(prefix)-len(suffix)) + suffix
}

func mcpProtocolGenerated(g mcpProtocolGen) []byte {
	ping := `{"jsonrpc":"2.0","id":1,"method":"ping"}`
	switch g.Kind {
	case "pad-line":
		return []byte(mcpProtocolPad(g.N) + "\n")
	case "pad-frame":
		return []byte(mcpFrame(mcpProtocolPad(g.N)))
	case "garbage-line":
		return []byte(strings.Repeat("x", g.N) + "\n")
	case "garbage-line-then-ping":
		return []byte(strings.Repeat("x", g.N) + "\n" + ping + "\n")
	case "oversized-frame-then-ping":
		return []byte(fmt.Sprintf("Content-Length: %d\r\n\r\n", g.N) + strings.Repeat("x", g.N) + mcpFrame(ping))
	case "deep-line":
		return []byte(`{"jsonrpc":"2.0","id":1,"method":"ping","params":` + strings.Repeat("[", g.N) + strings.Repeat("]", g.N) + "}\n")
	}
	panic("unknown generator " + g.Kind)
}

func mcpProtocolInput(c mcpProtocolCase) []byte {
	if c.Gen != nil {
		return mcpProtocolGenerated(*c.Gen)
	}
	var input []byte
	if c.InputHex != "" {
		decoded := make([]byte, len(c.InputHex)/2)
		for i := range decoded {
			var b byte
			if _, err := fmt.Sscanf(c.InputHex[2*i:2*i+2], "%02x", &b); err != nil {
				panic(err)
			}
			decoded[i] = b
		}
		input = decoded
	} else {
		input = []byte(c.Input)
	}
	return input
}

var mcpProtocolVersion = regexp.MustCompile(`"serverInfo":\{"name":"symdesk","version":"[^"]*"\}`)

// mcpProtocolNormalize replaces the build-specific version with a placeholder
// and recomputes every Content-Length header of leading frames it invalidates.
func mcpProtocolNormalize(stdout string) string {
	const placeholder = `"serverInfo":{"name":"symdesk","version":"<VERSION>"}`
	var out strings.Builder
	rest := stdout
	for strings.HasPrefix(rest, "Content-Length: ") {
		header, after, ok := strings.Cut(rest, "\r\n\r\n")
		if !ok {
			break
		}
		n, err := strconv.Atoi(strings.TrimPrefix(header, "Content-Length: "))
		if err != nil || n > len(after) {
			break
		}
		body := mcpProtocolVersion.ReplaceAllString(after[:n], placeholder)
		fmt.Fprintf(&out, "Content-Length: %d\r\n\r\n%s", len(body), body)
		rest = after[n:]
	}
	return out.String() + mcpProtocolVersion.ReplaceAllString(rest, placeholder)
}

func TestMCPProtocolProcessPortFixture(t *testing.T) {
	binary := processFixtureBinary(t)
	fixture := mcpProtocolFixture{SchemaVersion: 1}
	for _, c := range mcpProtocolCases() {
		root := t.TempDir()
		for _, dir := range []string{"home", "config", "data", "cache", "tmp", "vault", "cwd"} {
			if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		args := []string{"--vault", filepath.Join(root, "vault"), "mcp"}
		if c.AllowWrite {
			args = append(args, "--allow-write")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		//nolint:gosec // binary is the locally built symdesk fixture; arguments come from fixed test cases.
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Dir = filepath.Join(root, "cwd")
		cmd.Env = []string{"HOME=" + filepath.Join(root, "home"), "USERPROFILE=" + filepath.Join(root, "home"), "XDG_CONFIG_HOME=" + filepath.Join(root, "config"), "XDG_DATA_HOME=" + filepath.Join(root, "data"), "XDG_CACHE_HOME=" + filepath.Join(root, "cache"), "TMPDIR=" + filepath.Join(root, "tmp"), "TMP=" + filepath.Join(root, "tmp"), "TEMP=" + filepath.Join(root, "tmp"), "TZ=UTC", "LANG=C", "LC_ALL=C", "PATH="}
		if systemRoot := os.Getenv("SYSTEMROOT"); systemRoot != "" {
			cmd.Env = append(cmd.Env, "SYSTEMROOT="+systemRoot)
		}
		var stdout, stderr bytes.Buffer
		cmd.Stdin = bytes.NewReader(mcpProtocolInput(c))
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		cancel()
		c.ExitCode = 0
		if err != nil {
			exit, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("%s: %v", c.ID, err)
			}
			c.ExitCode = exit.ExitCode()
		}
		if !utf8.Valid(stdout.Bytes()) || !utf8.Valid(stderr.Bytes()) {
			t.Fatalf("%s: process output is not UTF-8", c.ID)
		}
		c.Stdout = mcpProtocolNormalize(stdout.String())
		c.Stderr = strings.ReplaceAll(stderr.String(), root, "$ROOT")
		fixture.Cases = append(fixture.Cases, c)
	}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	checkMCPSurfaceBytes(t, "../../testdata/port/mcp/protocol-process.json", append(data, '\n'))
}
