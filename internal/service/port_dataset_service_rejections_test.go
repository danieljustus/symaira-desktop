package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// This small native oracle has no POSIX-mode dependency. Rust consumes fresh
// errors from the real Go DatasetImport service, including Windows os.Stat's
// operation name and native error text, rather than translating a Unix fixture.
func TestPortDatasetImportServiceRejections(t *testing.T) {
	cases := runPortDatasetErrorCases(t)
	if len(cases) != 5 {
		t.Fatalf("native service rejection count: got %d, want 5", len(cases))
	}
	data, err := os.ReadFile(portDatasetFixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var fixture portDatasetFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.ErrorCases) != len(cases) {
		t.Fatal("recorded and executable rejection sets differ")
	}
	for i, actual := range cases {
		expected := fixture.ErrorCases[i]
		if actual.Name != expected.Name || actual.Detail != expected.Detail {
			t.Fatalf("rejection %d: got %+v, want %+v", i, actual, expected)
		}
		if runtime.GOOS != "windows" || actual.Name != "missing-source-file" {
			if actual.Error != expected.Error {
				t.Fatalf("%s: got %q, want %q", actual.Name, actual.Error, expected.Error)
			}
		}
	}
	if path := os.Getenv("PORT_DATASET_ERROR_FIXTURE"); path != "" {
		data, err := json.Marshal(cases)
		if err != nil {
			t.Fatal(err)
		}
		// The replay owns this directory; restrict the write to one fixed child
		// through os.Root rather than accepting an arbitrary destination file.
		if !filepath.IsAbs(path) || filepath.Base(path) != "native-service-errors.json" {
			t.Fatal("native service error fixture requires an absolute path with the fixed filename")
		}
		root, err := os.OpenRoot(filepath.Dir(path))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = root.Close() }()
		if err := root.WriteFile("native-service-errors.json", data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
