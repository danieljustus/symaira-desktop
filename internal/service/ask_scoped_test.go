package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/ai"
)

func collectAskEvents(t *testing.T, out <-chan interface{}) []ai.AIEvent {
	t.Helper()
	var events []ai.AIEvent
	for e := range out {
		evt, ok := e.(ai.AIEvent)
		if !ok {
			t.Fatalf("expected ai.AIEvent, got %T", e)
		}
		events = append(events, evt)
	}
	return events
}

func TestAskScoped_RestrictsCitationsToNotebookSources(t *testing.T) {
	t.Setenv("SYMDESK_OLLAMA_URL", "")
	svc := newTestService(t)

	inScope, err := svc.NoteNew("In Scope Doc", "shared-search-term appears here", "")
	if err != nil {
		t.Fatal(err)
	}
	outOfScope, err := svc.NoteNew("Out Of Scope Doc", "shared-search-term appears here too", "")
	if err != nil {
		t.Fatal(err)
	}

	nb, err := svc.NotebookNew("Scoped Notebook", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.NotebookAddSource(nb.ID, inScope); err != nil {
		t.Fatal(err)
	}

	out := make(chan interface{})
	go svc.AskScoped(context.Background(), nb.ID, "shared-search-term", out)
	events := collectAskEvents(t, out)

	var citedPaths []string
	for _, e := range events {
		if e.Type == ai.AIEventCitation {
			citedPaths = append(citedPaths, e.Path)
		}
	}
	if len(citedPaths) != 1 || citedPaths[0] != inScope {
		t.Fatalf("citations = %v, want exactly [%s]", citedPaths, inScope)
	}
	for _, p := range citedPaths {
		if p == outOfScope {
			t.Fatalf("out-of-scope document %s must never be cited", outOfScope)
		}
	}
}

func TestAskScoped_EmptyNotebookRefBehavesLikeAsk(t *testing.T) {
	t.Setenv("SYMDESK_OLLAMA_URL", "")
	svc := newTestService(t)

	if _, err := svc.NoteNew("Unscoped Doc", "unscoped-term content", ""); err != nil {
		t.Fatal(err)
	}

	out := make(chan interface{})
	go svc.AskScoped(context.Background(), "", "unscoped-term", out)
	events := collectAskEvents(t, out)

	hasCitation := false
	hasDone := false
	for _, e := range events {
		if e.Type == ai.AIEventCitation {
			hasCitation = true
		}
		if e.Type == ai.AIEventDone {
			hasDone = true
		}
	}
	if !hasCitation || !hasDone {
		t.Fatalf("expected citation and done events for the unscoped path, got %+v", events)
	}
}

func TestAskScoped_GroundsSourceWithoutLiteralKeywordMatch(t *testing.T) {
	t.Setenv("SYMDESK_OLLAMA_URL", "")
	svc := newTestService(t)

	// The source body never contains the literal query terms — a
	// conceptual question ("summarize this") must still ground in it.
	docPath, err := svc.NoteNew("Conceptual Doc", "completely unrelated wording", "")
	if err != nil {
		t.Fatal(err)
	}
	nb, err := svc.NotebookNew("Conceptual Notebook", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.NotebookAddSource(nb.ID, docPath); err != nil {
		t.Fatal(err)
	}

	out := make(chan interface{})
	go svc.AskScoped(context.Background(), nb.ID, "give me a summary please", out)
	events := collectAskEvents(t, out)

	found := false
	for _, e := range events {
		if e.Type == ai.AIEventCitation && e.Path == docPath {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the notebook's only source to be cited even without a literal keyword match, events=%+v", events)
	}
}

func TestAskScoped_FlagsOutOfScopeCitationAsWarning(t *testing.T) {
	svc := newTestService(t)

	inScope, err := svc.NoteNew("Cited In Scope", "notebook body", "")
	if err != nil {
		t.Fatal(err)
	}
	outOfScope, err := svc.NoteNew("Cited Out Of Scope", "other body", "")
	if err != nil {
		t.Fatal(err)
	}
	nb, err := svc.NotebookNew("Warning Notebook", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.NotebookAddSource(nb.ID, inScope); err != nil {
		t.Fatal(err)
	}

	// A mocked LLM answer that cites both an in-scope and an out-of-scope
	// note — only the latter must be flagged, since only in-scope paths
	// were part of this run's readPaths.
	// The heading must be a recognized citation section ("## Sources") —
	// CheckCitationWarnings only treats wikilinks inside such a section (or
	// frontmatter source fields) as citations; ordinary body links are not
	// (see internal/ai/citations.go doc comment). The `\n` sequences below
	// are literal backslash-n (this is a raw string), i.e. valid JSON
	// escapes decoding to real newlines inside the "response" field — a raw
	// newline byte in the HTTP body itself would break the NDJSON framing.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"response":"Answer text.\n\n## Sources\n- [[%s]]\n- [[%s]]\n","done":true}`+"\n", inScope, outOfScope)
	}))
	defer srv.Close()
	t.Setenv("SYMDESK_OLLAMA_URL", srv.URL)

	out := make(chan interface{})
	go svc.AskScoped(context.Background(), nb.ID, "notebook", out)
	events := collectAskEvents(t, out)

	var doneEvent *ai.AIEvent
	for i := range events {
		if events[i].Type == ai.AIEventDone {
			doneEvent = &events[i]
		}
	}
	if doneEvent == nil {
		t.Fatal("expected a done event")
	}
	if len(doneEvent.CitationWarnings) != 1 || doneEvent.CitationWarnings[0].Path != outOfScope {
		t.Fatalf("CitationWarnings = %+v, want exactly one warning for %s", doneEvent.CitationWarnings, outOfScope)
	}
}
