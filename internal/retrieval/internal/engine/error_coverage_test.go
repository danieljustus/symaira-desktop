package engine

import (
	"errors"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/retrieval/internal/db"
)

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("reader failed")
}

func TestIndexStdinReportsReaderFailure(t *testing.T) {
	store, err := db.OpenAt(t.TempDir() + "/index.db")
	if err != nil {
		t.Fatalf("db.OpenAt: %v", err)
	}
	defer func() { _ = store.Close() }()

	err = IndexStdin(store, &fakeEmbedder{dim: 8}, failingReader{}, "test://reader-error")
	if err == nil {
		t.Fatal("IndexStdin swallowed a reader error")
	}
	if !strings.Contains(err.Error(), "failed to read from stdin") {
		t.Fatalf("IndexStdin error = %v, want reader context", err)
	}
}

func TestValidatePublicURLReportsMalformedURL(t *testing.T) {
	if err := validatePublicURLString("://not-a-url"); err == nil {
		t.Fatal("validatePublicURLString accepted a malformed URL")
	} else if !strings.Contains(err.Error(), "invalid URL") {
		t.Fatalf("validatePublicURLString error = %v, want invalid URL context", err)
	}
}
