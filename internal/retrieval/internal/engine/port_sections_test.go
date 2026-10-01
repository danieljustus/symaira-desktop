package engine

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/retrieval/internal/parser"
)

type retrievalSectionsFixture struct {
	SchemaVersion int                     `json:"schema_version"`
	Cases         []retrievalSectionsCase `json:"cases"`
}

type retrievalSectionsCase struct {
	ID       string                   `json:"id"`
	Source   string                   `json:"source"`
	Markdown string                   `json:"markdown"`
	Sections []parser.Section         `json:"sections"`
	Chunks   []retrievalSectionsChunk `json:"chunks"`
}

type retrievalSectionsChunk struct {
	UUID        string `json:"uuid"`
	ChunkIndex  int    `json:"chunk_index"`
	Content     string `json:"content"`
	Hash        string `json:"hash"`
	CharStart   *int   `json:"char_start,omitempty"`
	CharEnd     *int   `json:"char_end,omitempty"`
	AnchorKind  string `json:"anchor_kind"`
	AnchorValue string `json:"anchor_value"`
}

// TestRetrievalSectionsFixture records real Go Markdown section parsing and
// the same synthetic search metadata section used by retrieval indexing.
func TestRetrievalSectionsFixture(t *testing.T) {
	root := t.TempDir()
	previousWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previousWD) })

	markdown := []retrievalSectionsCase{
		{
			ID:       "headings-inside-code-fence",
			Markdown: "Intro text before headings.\n\n# Guide\nBody one.\n\n```md\n## Fence Heading\ncode content\n```\n\n### Install 🧪\nBody two.\n\n## Next\nFinal.\n",
		},
		{
			ID:       "unicode-frontmatter-offsets-crlf",
			Markdown: strings.ReplaceAll("---\ntitle: Überblick 🧭\ntags: [Müller, Forschung]\naliases: [Leitfaden]\nstatus: offen\n---\nVorspann äΩ\n# Abschnitt 🗂\nText beginnt hier.\n## Unterpunkt\nNoch Text.\n", "\n", "\r\n"),
		},
		{
			ID:       "plain-unicode-without-headings",
			Markdown: "  Grüß Gott\nZeile αβγ 🧭\n ",
		},
		{
			ID:       "empty-body",
			Markdown: "---\ntitle: \"\"\ntags: []\naliases: []\n---\n \n\t\n",
		},
		{
			ID:       "unterminated-frontmatter",
			Markdown: "---\ntitle: name\n# Heading\nBody stays after malformed delimiter.\n",
		},
	}

	fixture := retrievalSectionsFixture{SchemaVersion: 1}
	for _, testCase := range markdown {
		filename := testCase.ID + ".md"
		if err := os.WriteFile(filename, []byte(testCase.Markdown), 0o600); err != nil {
			t.Fatal(err)
		}
		sections, err := parser.ParseMarkdownSections([]byte(testCase.Markdown))
		if err != nil {
			t.Fatalf("%s: parse Markdown sections: %v", testCase.ID, err)
		}
		testCase.Source = filepath.ToSlash(filepath.Join("$VAULT", filename))
		sections = prependSearchMetadata(sections, searchMetadataForPath(filename))
		testCase.Sections = sections
		for _, chunk := range buildChunksFromSections(&fakeEmbedder{dim: 8}, testCase.Source, sections) {
			testCase.Chunks = append(testCase.Chunks, retrievalSectionsChunk{
				UUID: chunk.UUID, ChunkIndex: chunk.ChunkIndex, Content: chunk.Content,
				Hash: chunk.Hash, CharStart: chunk.CharStart, CharEnd: chunk.CharEnd,
				AnchorKind: chunk.AnchorKind, AnchorValue: chunk.AnchorValue,
			})
		}
		fixture.Cases = append(fixture.Cases, testCase)
	}
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source path unavailable")
	}
	path := filepath.Join(filepath.Dir(source), "../../../../testdata/port/retrieval/retrieval-sections.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	fixtureRoot, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fixtureRoot.Close() }()
	current, err := fixtureRoot.ReadFile(filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, encoded) {
		t.Fatal("retrieval sections fixture is stale; regenerate explicitly with PORT_GENERATE=1 go test ./internal/retrieval/internal/engine -run '^TestRetrievalSectionsFixture$'")
	}
}
