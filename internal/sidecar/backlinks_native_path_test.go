package sidecar

import (
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// A basename query deliberately misses the exact indexed path. This exercises
// the suffix lookup that must recover the target's title and aliases on Windows.
func TestGetBacklinksBasenameUsesNativePathSeparator(t *testing.T) {
	for _, native := range []bool{false, true} {
		name := "slash"
		if native {
			name = "native"
		}
		t.Run(name, func(t *testing.T) {
			db := setupTestDB(t)
			path := "vault/nested/target.md"
			if native {
				path = filepath.FromSlash(path)
			}
			for _, doc := range []*vault.Document{
				{Path: path, Title: "Target Title", Aliases: []string{"Alias Name"}, SHA256: "target"},
				{Path: "title.md", Title: "Title link", Links: []string{"Target Title"}, SHA256: "title"},
				{Path: "alias.md", Title: "Alias link", Links: []string{"Alias Name"}, SHA256: "alias"},
				{Path: "basename.md", Title: "Basename link", Links: []string{"target"}, SHA256: "basename"},
			} {
				if err := db.IndexDocument(doc); err != nil {
					t.Fatal(err)
				}
			}
			got, err := db.GetBacklinks("target.md")
			if err != nil {
				t.Fatal(err)
			}
			sort.Strings(got)
			want := []string{"alias.md", "basename.md", "title.md"}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("GetBacklinks(target.md) = %v, want %v (indexed path %q)", got, want, path)
			}
		})
	}
}
