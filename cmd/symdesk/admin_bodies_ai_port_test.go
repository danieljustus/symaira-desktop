package main

// `ai autofill` and `notebook generate`: the two one-shot LLM commands. The
// provider is a loopback fake of Ollama's /api/generate that every case
// reaches through $LLM_URL; the answer is derived from marker words in the
// prompt (adminLLMAnswer), and the Rust replay runs the same rules.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func init() {
	adminBodiesCaseBuilders = append(adminBodiesCaseBuilders, adminAICases)
}

// adminLLMAnswer returns the fake provider's answer; ok=false is an HTTP 500.
func adminLLMAnswer(prompt string) (string, bool) {
	if strings.Contains(prompt, "LLM-FAIL") {
		return "", false
	}
	if strings.Contains(prompt, "Extract the value for the property") {
		if strings.Contains(prompt, "LLM-EMPTY") {
			return "", true
		}
		if strings.Contains(prompt, "LLM-PADDED") {
			return "  \n padded value \n", true
		}
		title := "untitled"
		for _, line := range strings.Split(prompt, "\n") {
			if rest, found := strings.CutPrefix(line, "Title: "); found {
				title = rest
				break
			}
		}
		return "filled:" + title, true
	}
	if strings.Contains(prompt, "LLM-BLANK") {
		return "  \n", true
	}
	var paths []string
	for _, line := range strings.Split(prompt, "\n") {
		if rest, found := strings.CutPrefix(line, "--- Source [["); found {
			if path, _, ok := strings.Cut(rest, "]]"); ok {
				paths = append(paths, path)
			}
		}
	}
	out := "# Generated\n\nsources=" + strconv.Itoa(len(paths)) + "\n\n## Sources\n"
	for _, path := range paths {
		out += "[[" + path + "]]\n"
	}
	if strings.Contains(prompt, "LLM-STRAY") {
		out += "[[stray/outside.md]]\n"
	}
	return out, true
}

func startAdminLLMServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Prompt string `json:"prompt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		answer, ok := adminLLMAnswer(body.Prompt)
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"fake failure"}`))
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "fake", "response": answer, "done": true})
	}))
	t.Cleanup(server.Close)
	return server
}

func adminLLMEnv() map[string]string {
	return map[string]string{"SYMDESK_OLLAMA_URL": "$LLM_URL", "SYMDESK_OLLAMA_MODEL": "fake"}
}

func adminAICases() []adminBodiesCase {
	note := func(title, extra, body string) string {
		return "---\ntitle: " + title + "\n" + extra + "---\n" + body
	}
	invoices := map[string]string{
		"invoices/one.md":      note("One", "status: open\n", "first\n"),
		"invoices/two.md":      note("Two", "status: paid\nsummary: already there\n", "second\n"),
		"invoices/three.md":    note("Three", "", "third LLM-EMPTY\n"),
		"invoices/four.md":     note("Four", "", "fourth LLM-FAIL\n"),
		"invoices/five.md":     note("Five", "", "fifth LLM-PADDED\n"),
		"invoices/sub/six.md":  "no frontmatter\n",
		"other/outside.md":     note("Outside", "", "not in the view\n"),
		"invoices/seven.md":    note("Seven & <co>", "", "seventh\n"),
		"invoices/eight.md":    note("Eight", "summary:\n", "eighth\n"),
		"invoices/nine.md":     note("Nine", "summary: 0\n", "ninth\n"),
		"invoices/ten-list.md": note("Ten", "summary: [a, b]\n", "tenth\n"),
	}
	const (
		viewAll  = `{"id":"all","name":"All invoices","source":"invoices/"}`
		viewOne  = `{"id":"ones","name":"Only one","source":"invoices/","filters":[{"key":"title","operator":"equals","value":"One"}]}`
		viewNone = `{"id":"none","name":"No match","source":"invoices/","filters":[{"key":"title","operator":"equals","value":"nothing"}]}`
		viewDSet = `{"id":"dset","name":"Orders","source":"dataset:orders"}`
		viewBad  = `{"id":"baddir","name":"Bad dir","source":"nowhere/"}`
	)
	save := func(spec string) []adminBodiesStep { return adminBoth("views", "save", spec) }
	autofill := func(args ...string) []adminBodiesStep {
		return adminBoth(append([]string{"ai", "autofill"}, args...)...)
	}
	sources := map[string]string{
		"a.md":                              note("Alpha", "", "alpha text\n"),
		"b.md":                              note("Beta", "", "beta text\n"),
		"c.md":                              note("Gamma", "", "gamma LLM-STRAY\n"),
		"notebooks/team.md":                 "---\ntype: notebook\ntitle: Team notes\nnotebook_id: team\ncreated: 2026-01-02T03:04:05Z\nsources:\n  - a.md\n  - b.md\n  - missing.md\n---\n\n# Team notes\n",
		"notebooks/ghost.md":                "---\ntype: notebook\ntitle: Ghost\nnotebook_id: ghost\ncreated: 2026-01-02T03:04:05Z\nsources:\n  - nowhere.md\n---\n\n# Ghost\n",
		"notebooks/none.md":                 "---\ntype: notebook\ntitle: Nothing\nnotebook_id: none\ncreated: 2026-01-02T03:04:05Z\nsources: []\n---\n\n# Nothing\n",
		"notebooks/odd.md":                  "---\ntype: notebook\ntitle: 'Odd: title #1'\nnotebook_id: odd\ncreated: 2026-01-02T03:04:05Z\nsources:\n  - c.md\n---\n\n# Odd\n",
		"templates/notebook-custom_kind.md": "---\ntitle: Custom\n---\nWrite a haiku about the sources.\n",
		"templates/notebook-stray.md":       "Cite everything. LLM-STRAY\n",
		"templates/notebook-blank.md":       "Reply with nothing. LLM-BLANK\n",
		"templates/notebook-broken.md":      "Please fail. LLM-FAIL\n",
		"templates/notebook-empty.md":       "---\ntitle: Only frontmatter\n---\n",
		"templates/notebook-briefing.md":    "Vault-local override of the briefing kind.\n",
	}
	generate := func(args ...string) []adminBodiesStep {
		return adminBoth(append([]string{"notebook", "generate"}, args...)...)
	}
	return []adminBodiesCase{
		{Name: "autofill-lifecycle", Files: invoices, Env: adminLLMEnv(), SnapshotVault: true, Volatile: true, Steps: adminConcat(
			save(viewAll), save(viewOne), save(viewNone), save(viewBad),
			autofill("--view", "all", "--property", "summary", "--dry-run"),
			autofill("--view", "all", "--property", "summary"),
			autofill("--view", "all", "--property", "summary"),
			autofill("--view", "ones", "--property", "kind", "--prompt", "Answer in one word."),
			autofill("--view", "none", "--property", "summary"),
			autofill("--view", "all", "--property", "  status  "),
		)},
		{Name: "autofill-errors", Files: invoices, Env: adminLLMEnv(), SnapshotVault: true, Volatile: true, Steps: adminConcat(
			save(viewAll), save(viewBad), save(viewDSet),
			autofill("--view", "all", "--property", " "),
			autofill("--view", "ghost", "--property", "summary"),
			autofill("--view", "baddir", "--property", "summary"),
			autofill("--view", "dset", "--property", "summary"),
			autofill("--view", "all"),
			autofill("--property", "summary"),
			autofill("--view", "all", "--property", "summary", "extra"),
			autofill("--bogus"),
		)},
		{Name: "autofill-no-provider", Files: invoices, SnapshotVault: true, Volatile: true, Steps: adminConcat(
			save(viewOne),
			autofill("--view", "ones", "--property", "summary"),
		)},
		{Name: "autofill-without-vault", NoVault: true, SkipIndex: true, Steps: autofill("--view", "all", "--property", "summary")},
		{Name: "generate-kinds", Files: sources, Env: adminLLMEnv(), SnapshotVault: true, Volatile: true, Steps: adminConcat(
			generate("team", "--kind", "briefing", "--dry-run"),
			generate("team", "--kind", "briefing"),
			generate("team", "--kind", "briefing"),
			generate("team", "--kind", "faq"),
			generate("team.md", "--kind", "study-guide"),
			generate("notebooks/team.md", "--kind", "timeline"),
			generate("team", "--kind", "custom_kind"),
			generate("team", "--kind", "stray"),
			generate("odd", "--kind", "briefing"),
		)},
		{Name: "generate-errors", Files: sources, Env: adminLLMEnv(), SnapshotVault: true, Volatile: true, Steps: adminConcat(
			generate("team", "--kind", " "),
			generate("team", "--kind", "unknown-kind"),
			generate("team", "--kind", "blank"),
			generate("team", "--kind", "broken"),
			generate("team", "--kind", "empty"),
			generate("ghost", "--kind", "faq"),
			generate("none", "--kind", "faq"),
			generate("absent", "--kind", "faq"),
			generate("team", "--kind", "../escape"),
			generate("team"),
			generate("--kind", "faq"),
			generate("team", "other", "--kind", "faq"),
		)},
		{Name: "generate-no-provider", Files: sources, SnapshotVault: true, Volatile: true, Steps: adminConcat(
			generate("team", "--kind", "faq"),
		)},
		{Name: "generate-without-vault", NoVault: true, SkipIndex: true, Steps: generate("team", "--kind", "faq")},
	}
}
