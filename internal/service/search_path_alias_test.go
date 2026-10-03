package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

func TestSearchAliasRootSidecarContainment(t *testing.T) {
	home := canonicalOracleTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	actual := filepath.Join(t.TempDir(), "vault")
	if err := os.MkdirAll(actual, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(filepath.Dir(actual), "alias")
	if err := os.Symlink(actual, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	body := "---\ntitle: Alias Note\ntags: [alias]\n---\n\nA coordinate needle remains searchable."
	if err := os.WriteFile(filepath.Join(actual, "note.md"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sidecar.OpenForVault(alias)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	// Legacy alias identities must project safely, independently of RefreshIndex.
	doc, err := vault.ParseFileInRoot(alias, filepath.Join(alias, "note.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.IndexDocument(doc); err != nil {
		t.Fatal(err)
	}
	svc := New(alias, db)
	defer func() { _ = svc.Close() }()
	_ = svc.Close() // force unavailable-hybrid fallback, without provider calls
	for _, query := range []string{"coordinate needle", "tag:alias"} {
		response, err := svc.SearchWithMeta(query)
		if err != nil {
			t.Fatal(err)
		}
		if len(response.Results) != 1 || response.Results[0].Path != "note.md" || response.Results[0].Title != "Alias Note" || response.Results[0].Score != 0 {
			t.Fatalf("%q alias projection = %+v", query, response)
		}
		if !strings.Contains(response.Results[0].Snippet, "coordinate needle") {
			t.Fatalf("snippet lost: %+v", response)
		}
	}
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	escape := filepath.Join(actual, "escape.md")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{outside, escape} {
		if _, ok := svc.searchVaultPath(path); ok {
			t.Fatalf("accepted unsafe path %q", path)
		}
	}
	// Even an injected stale or unconfined sidecar row must not leak through FTS.
	doc.Path = outside
	if err := db.IndexDocument(doc); err != nil {
		t.Fatal(err)
	}
	doc.Path = escape
	if err := db.IndexDocument(doc); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"coordinate needle", "tag:alias"} {
		response, err := svc.SearchWithMeta(query)
		if err != nil {
			t.Fatal(err)
		}
		if len(response.Results) != 1 || response.Results[0].Path != "note.md" {
			t.Fatalf("unconfined sidecar hit: %+v", response)
		}
	}
}

func TestSearchAliasRootRefreshPreservesStorageAndRelativeResults(t *testing.T) {
	home := canonicalOracleTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	actual := filepath.Join(t.TempDir(), "vault")
	if err := os.MkdirAll(actual, 0700); err != nil {
		t.Fatal(err)
	}
	aliasParent := filepath.Join(filepath.Dir(actual), "alias-parent")
	if err := os.Symlink(filepath.Dir(actual), aliasParent); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	alias := filepath.Join(aliasParent, "vault")
	if err := os.WriteFile(filepath.Join(actual, "note.md"), []byte("---\ntitle: Canonical Note\n---\n\nneedle"), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sidecar.OpenForVault(alias)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.RefreshIndex(alias); err != nil {
		t.Fatal(err)
	}
	docs, err := db.ListFiles("")
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 || docs[0].Path != filepath.Join(alias, "note.md") {
		t.Fatalf("alias refresh identity = %+v", docs)
	}
	svc := New(alias, db)
	_ = svc.Close()
	response, err := svc.SearchWithMeta("needle")
	if err != nil || len(response.Results) != 1 || response.Results[0].Path != "note.md" || response.Results[0].Title != "Canonical Note" {
		t.Fatalf("alias refresh projection = %+v %v", response, err)
	}
}
