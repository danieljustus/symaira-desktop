package paperlessimport

import (
	"encoding/json"
	"io"
	"testing"
)

// mustEncode and mustWrite keep HTTP fixture failures visible instead of
// silently discarding response-writer errors.
func mustEncode(w io.Writer, value any) {
	if err := json.NewEncoder(w).Encode(value); err != nil {
		panic(err)
	}
}

func mustWrite(w io.Writer, data []byte) {
	if _, err := w.Write(data); err != nil {
		panic(err)
	}
}

func closeTestResource(t *testing.T, resource io.Closer) {
	t.Helper()
	if err := resource.Close(); err != nil {
		t.Errorf("close test resource: %v", err)
	}
}

func closeTestServer(t *testing.T, server interface{ Close() }) {
	t.Helper()
	server.Close()
}
