package retrieval

import (
	"errors"
	"testing"

	seekapi "github.com/danieljustus/symaira-seek/api"
)

// Index/Delete/Search swallow failures on purpose. CurrentStatus must not:
// a caller asking whether retrieval is healthy has to hear "unknown" rather
// than a confident zero, which would read as "the index is empty".
func TestCurrentStatusPropagatesTheFailure(t *testing.T) {
	prev := StatusFunc
	t.Cleanup(func() { StatusFunc = prev })
	wantErr := errors.New("index unavailable")
	StatusFunc = func() (*seekapi.Status, error) { return nil, wantErr }

	status, err := CurrentStatus()

	if !errors.Is(err, wantErr) {
		t.Fatalf("CurrentStatus() error = %v, want %v", err, wantErr)
	}
	if status != nil {
		t.Errorf("CurrentStatus() = %+v, want nil on failure", status)
	}
}

func TestCurrentStatusReportsADegradedBackend(t *testing.T) {
	prev := StatusFunc
	t.Cleanup(func() { StatusFunc = prev })
	StatusFunc = func() (*seekapi.Status, error) {
		return &seekapi.Status{DocumentCount: 12, EmbeddingModel: "local-hash", BackendAvailable: false}, nil
	}

	status, err := CurrentStatus()
	if err != nil {
		t.Fatalf("CurrentStatus() error = %v", err)
	}
	if status.BackendAvailable {
		t.Error("BackendAvailable = true, want false for the fallback model")
	}
	if status.DocumentCount != 12 {
		t.Errorf("DocumentCount = %d, want 12", status.DocumentCount)
	}
}
