package db

import "testing"

func TestSearchBM25GermanCompoundParts(t *testing.T) {
	d := openTestDB(t)
	defer func() { _ = d.Close() }()
	const docPath = "/docs/german-compound.md"
	if err := d.SaveDocument(&Document{Path: docPath, Hash: "hash-german-compound"}); err != nil {
		t.Fatalf("SaveDocument: %v", err)
	}
	if err := d.SaveChunks([]*Chunk{{
		UUID:         "german-compound-1",
		DocumentPath: docPath,
		ChunkIndex:   0,
		Content:      "Der Krankenversicherungsbeitrag wurde angepasst. Die Beitragsbemessungsgrenze steigt.",
		Embedding:    []float32{1, 0},
		Hash:         "chunk-german-compound",
	}}); err != nil {
		t.Fatalf("SaveChunks: %v", err)
	}
	for _, query := range []string{"Bemessungsgrenze", "Versicherungsbeitrag"} {
		results, err := d.SearchBM25(query, 10)
		if err != nil {
			t.Fatalf("SearchBM25(%q): %v", query, err)
		}
		if len(results) != 1 || results[0].Chunk.DocumentPath != docPath {
			t.Fatalf("SearchBM25(%q) = %#v, want %s", query, results, docPath)
		}
	}
}
