package store

import (
	"io"
	"testing"
)

// closeTestResource keeps cleanup failures visible in store tests.
func closeTestResource(t *testing.T, resource io.Closer) {
	t.Helper()
	if err := resource.Close(); err != nil {
		t.Errorf("close test resource: %v", err)
	}
}

func mustStoreCall(t *testing.T, call func() error) {
	t.Helper()
	if err := call(); err != nil {
		t.Fatal(err)
	}
}
