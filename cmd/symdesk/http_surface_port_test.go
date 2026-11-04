package main

import (
	"fmt"
	"strings"
	"testing"
)

// RUST-015 HTTP-001/HTTP-002/HTTP-007/SEC-002 surface fixture. Replay:
// `crates/symdesk-cli/tests/http_surface.rs`.

const httpSurfaceFixturePath = "../../testdata/port/http/http-surface-process.json"

func TestPortHTTPSurfaceProcessContract(t *testing.T) {
	writeHTTPFixture(t, httpSurfaceFixturePath, observeHTTPFixture(t, httpSurfaceCases()))
}

// httpStdFiles is the vault every surface case starts from.
func httpStdFiles() map[string]string {
	return map[string]string{
		"Hello.md":              "---\ntitle: Hello\n---\nBody",
		"nested/Note.md":        "nested Body Body",
		"notebooks/research.md": "---\ntype: notebook\ntitle: Research\ncreated: 2026-01-02T03:04:05Z\nnotebook_id: research\ndescription: Research notes\nsources:\n  - Hello.md\n---\n",
		"inbox/c.png":           "worker input bytes",
	}
}

func httpStdSymlinks() map[string]string {
	return map[string]string{"internal.md": "Hello.md", "escape.md": "$ROOT/outside.md"}
}

// httpVolatile masks every volatile value class a surface response may carry.
var httpVolatile = []string{"time", "hex", "version", "date"}

func httpAuth(name string) [][2]string {
	if name == "" {
		return nil
	}
	return [][2]string{{"Authorization", "Bearer $" + name}}
}

func httpReq(label, method, target string, headers [][2]string, body string) httpStep {
	return httpStep{Label: label, Method: method, Target: target, Headers: headers, Body: body}
}

func httpCaseOf(name string, steps ...httpStep) httpCase {
	return httpCase{Name: name, Files: httpStdFiles(), Symlinks: httpStdSymlinks(), Steps: steps}
}

func httpTreeStep() httpStep {
	return httpStep{Label: "vault tree", Kind: "tree", TreeInclude: []string{".symdesk/users.json", ".symdesk/groups.json", ".symdesk/permissions.json"}, Mask: []string{"hex", "time"}}
}

// httpRoutes are the 21 method/path patterns of Server.routes with a request
// target that reaches the handler.
var httpRoutes = []struct{ method, target string }{
	{"GET", "/healthz"},
	{"GET", "/api/v1/status"},
	{"GET", "/api/v1/snapshot"},
	{"GET", "/api/v1/files?path=Hello.md"},
	{"PUT", "/api/v1/files?path=put-matrix.md"},
	{"POST", "/api/v1/ingest"},
	{"GET", "/api/v1/jobs"},
	{"POST", "/api/v1/jobs/retry?id=00000000000000000000000000000000"},
	{"POST", "/api/v1/command"},
	{"POST", "/api/v1/ai/ask"},
	{"POST", "/api/v1/ai/transform"},
	{"GET", "/api/v1/notebooks"},
	{"GET", "/api/v1/notebooks/research"},
	{"POST", "/api/v1/worker/lease"},
	{"GET", "/api/v1/worker/input?id=00000000000000000000000000000000"},
	{"POST", "/api/v1/worker/complete"},
	{"POST", "/api/v1/worker/fail"},
	{"POST", "/api/v1/share"},
	{"GET", "/api/v1/shares"},
	{"DELETE", "/api/v1/share/unknown"},
	{"GET", "/s/unknown-token"},
}

func httpRouteCases() []httpCase {
	var cases []httpCase
	methods := []string{"GET", "HEAD", "POST", "PUT", "DELETE", "PATCH", "OPTIONS", "TRACE"}
	for _, route := range httpRoutes {
		var steps []httpStep
		for _, auth := range []string{"", "ADMIN", "WORKER"} {
			for _, method := range methods {
				body := ""
				if method == "POST" || method == "PUT" || method == "PATCH" {
					body = "{}"
				}
				label := fmt.Sprintf("%s %s auth=%s", method, route.target, strings.ToLower(auth))
				step := httpReq(label, method, route.target, httpAuth(auth), body)
				// Snapshots and job/share documents carry volatile values.
				step.Mask = httpVolatile
				step.VolatileLength = true
				steps = append(steps, step)
			}
		}
		steps = append(steps, httpTreeStep())
		cases = append(cases, httpCaseOf("routes "+route.method+" "+route.target, steps...))
	}
	return cases
}

// httpPathTargets are request targets that exercise ServeMux path cleaning,
// escapes, wildcards and subtree handling.
var httpPathTargets = []string{
	"/", "/api", "/api/v1", "/api/v1/", "/healthz/", "/healthz?x=1", "/HEALTHZ", "//healthz", "/./healthz", "/api/../healthz",
	"/api/v1/status/", "/api/v1//status", "/api/v1/status?", "/%61pi/v1/status", "/api/v1/notebooks/", "/api/v1/notebooks/a/b",
	"/api/v1/notebooks/%2e%2e", "/api/v1/notebooks/..", "/api/v1/notebooks/a%2Fb", "/s/", "/s/a/b", "/s", "/api/v1/share/",
	"/api/v1/share", "/api/v1/shares/x", "/api/v1/worker", "/api/v1/worker/", "/api/v1/ai", "/api/v1/ai/", "/favicon.ico",
	"/robots.txt", "/healthz%00", "/healthz%", "/healthz%zz", "/api/v1/status;x=1", "/a%20b/../healthz", "//a%20b//c", "/../healthz",
	"/api/v1/notebooks/research%00", "/api/v1/notebooks/%72esearch", "/s/%2e", "/s/tok%2Fen", "/%2fhealthz", "/healthz%2f",
}

func httpPathCases() []httpCase {
	var cases []httpCase
	for chunk := 0; chunk < len(httpPathTargets); chunk += 12 {
		end := min(chunk+12, len(httpPathTargets))
		var steps []httpStep
		for _, target := range httpPathTargets[chunk:end] {
			for _, method := range []string{"GET", "POST"} {
				body := ""
				if method == "POST" {
					body = "{}"
				}
				step := httpReq(fmt.Sprintf("%s %s", method, target), method, target, httpAuth("ADMIN"), body)
				step.Mask = httpVolatile
				step.VolatileLength = true
				steps = append(steps, step)
			}
		}
		cases = append(cases, httpCaseOf(fmt.Sprintf("paths %d", chunk/12), steps...))
	}
	return cases
}

func httpSurfaceCases() []httpCase {
	var cases []httpCase
	cases = append(cases, httpRouteCases()...)
	cases = append(cases, httpPathCases()...)
	cases = append(cases, httpAuthCases()...)
	cases = append(cases, httpFileCases()...)
	cases = append(cases, httpCommandCases()...)
	cases = append(cases, httpShareCases()...)
	cases = append(cases, httpAICases()...)
	cases = append(cases, httpNetworkCases()...)
	cases = append(cases, httpServeCases()...)
	return cases
}
