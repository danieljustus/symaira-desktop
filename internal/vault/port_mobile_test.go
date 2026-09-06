package vault

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMobileWriterFixture(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "testdata", "port", "vault", "mobile-writer.json")
	//nolint:gosec // fixturePath is a fixed repo-relative path
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		SchemaVersion int    `json:"schema_version"`
		Filename      string `json:"filename"`
		Document      string `json:"document"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.SchemaVersion != 1 || fixture.Filename != "Käse___日本.md" {
		t.Fatalf("unexpected mobile writer fixture header: %#v", fixture)
	}
	document, err := ParseBytes(fixture.Filename, []byte(fixture.Document))
	if err != nil {
		t.Fatal(err)
	}
	if document.Title != "Käse \"Crème\" \\ 日本" || document.Created != "2025-07-15T17:20:00Z" || len(document.Tags) != 0 || document.Body != "\n## Einkauf\n\nMilch und 日本語" {
		t.Fatalf("unexpected parsed mobile document: %#v", document)
	}
}
