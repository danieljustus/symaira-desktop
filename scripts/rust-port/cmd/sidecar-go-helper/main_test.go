package main

import (
	"errors"
	"testing"
	"time"
)

func TestClassifyNormalizesProviderErrors(t *testing.T) {
	cases := []struct {
		name  string
		err   string
		class string
		busy  bool
	}{
		{"corrupt", "database disk image is malformed", "corrupt", false},
		{"locked", "database is locked", "locked", true},
		{"readonly", "attempt to write a readonly database", "readonly", false},
		{"constraint", "UNIQUE constraint failed: links.from_path", "constraint", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			class, busy := classify(errors.New(tc.err))
			if class != tc.class || busy != tc.busy {
				t.Fatalf("classify(%q)=(%q,%v)", tc.err, class, busy)
			}
		})
	}
}

func TestWaitFileTimeoutIsBounded(t *testing.T) {
	started := time.Now()
	if err := waitFile(t.TempDir()+"/never", 20*time.Millisecond); err == nil {
		t.Fatal("expected timeout")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("timeout was not bounded: %s", elapsed)
	}
}
