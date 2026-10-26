package engine

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Reuse the deterministic Go parser corpus's input bytes, never its expected
// text: this oracle independently runs the real re-embedding engine on them.
func extractionReembedCases(t *testing.T) []reembedHTTPFixtureCase {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(file), "../../../../testdata/port/retrieval/document-extraction.json")
	bytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Cases []struct {
			ID        string `json:"id"`
			Extension string `json:"extension"`
			Input     string `json:"input_base64"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(bytes, &corpus); err != nil {
		t.Fatal(err)
	}
	selected := map[string]bool{"entities.html": true, "paragraphs.rtf": true, "blocks.docx": true, "blocks.odt": true, "blocks.ods": true, "blocks.odp": true, "sheets-order.xlsx": true, "slides-order.pptx": true, "chapters-sorted.epub": true, "operators.pdf": true}
	cases := []reembedHTTPFixtureCase{}
	for _, in := range corpus.Cases {
		if !selected[in.ID] {
			continue
		}
		body, err := base64.StdEncoding.DecodeString(in.Input)
		if err != nil {
			t.Fatal(err)
		}
		cases = append(cases, reembedHTTPFixtureCase{ID: "provider-resolves-extraction-" + in.ID, DocumentPath: "doc" + in.Extension, DocumentBody: string(body), ResponseStatus: http.StatusOK, EmbeddingDim: 3})
	}
	if len(cases) != len(selected) {
		t.Fatalf("extraction reembed cases: %d, want %d", len(cases), len(selected))
	}
	return cases
}
