package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/notebook"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// Exercise output boundaries with multiple native separator levels on Windows.
// No path normalizer is allowed between the service and these assertions.
func TestReadDocumentIdentifiersArePortable(t *testing.T) {
	svc := newTestService(t)
	const relative = "notebooks/nested/hidden.md"
	absolute := filepath.Join(svc.VaultRoot, filepath.FromSlash(relative))
	body := []byte("---\ntitle: Hidden\ntype: meeting\n---\nneedle\n")
	if err := os.MkdirAll(filepath.Dir(absolute), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absolute, body, 0600); err != nil {
		t.Fatal(err)
	}
	doc, err := vault.ParseBytes(absolute, body)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DB.IndexDocument(doc); err != nil {
		t.Fatal(err)
	}
	docs, err := svc.DocsList(sidecar.DocsFilter{})
	if err != nil || len(docs) != 1 || docs[0].Path != relative {
		t.Fatalf("docs: %#v, %v", docs, err)
	}
	ls, err := svc.Ls("")
	if err != nil || len(ls) != 1 || ls[0].Path != relative {
		t.Fatalf("ls: %#v, %v", ls, err)
	}
	meetings, err := svc.MeetingList()
	if err != nil || len(meetings) != 1 || meetings[0].Path != relative {
		t.Fatalf("meetings: %#v, %v", meetings, err)
	}
	// The same absolute path must remain native in host-path APIs.
	review, err := svc.DocsReview(85)
	if err != nil || len(review) != 1 || review[0].Path != absolute {
		t.Fatalf("review host path: %#v, %v", review, err)
	}
	notebookBody := []byte("---\ntype: notebook\ntitle: Hidden\n---\n")
	if err := os.WriteFile(absolute, notebookBody, 0600); err != nil {
		t.Fatal(err)
	}
	nb, err := notebook.Load(svc.VaultRoot, filepath.FromSlash(relative))
	if err != nil || nb.Path != relative {
		t.Fatalf("notebook: %#v, %v", nb, err)
	}
}
