package vault

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeASNTestNote(t *testing.T, root, name, frontmatter string) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	content := "---\ntitle: Test\n" + frontmatter + "---\n\nBody\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseFileASNContract(t *testing.T) {
	dir := t.TempDir()
	valid := writeASNTestNote(t, dir, "valid.md", "asn: 17\n")
	doc, err := ParseFile(valid)
	if err != nil {
		t.Fatal(err)
	}
	if doc.ASN == nil || *doc.ASN != 17 {
		t.Fatalf("expected ASN 17, got %#v", doc.ASN)
	}

	for name, value := range map[string]string{
		"zero":     "0",
		"negative": "-1",
		"decimal":  "1.0",
		"string":   "\"17\"",
	} {
		t.Run(name, func(t *testing.T) {
			path := writeASNTestNote(t, dir, name+".md", "asn: "+value+"\n")
			_, err := ParseFile(path)
			if err == nil {
				t.Fatal("expected invalid ASN error")
			}
			var asnErr *ASNValidationError
			if !strings.Contains(err.Error(), "invalid asn") || !strings.Contains(err.Error(), "positive integer") {
				t.Fatalf("expected descriptive ASN error, got %v", err)
			}
			if !errors.As(err, &asnErr) {
				t.Fatalf("expected ASNValidationError, got %T", err)
			}
		})
	}
}

func TestScanASNsReportsEveryMalformedAndDuplicateAssignment(t *testing.T) {
	dir := t.TempDir()
	writeASNTestNote(t, dir, "a.md", "asn: 7\n")
	writeASNTestNote(t, dir, "nested/b.md", "asn: 7\n")
	writeASNTestNote(t, dir, "bad.md", "asn: nope\n")
	writeASNTestNote(t, dir, ".symdesk/ignored.md", "asn: 99\n")

	report, err := ScanASNs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if report.FilesScanned != 3 {
		t.Errorf("expected three non-hidden files, got %d", report.FilesScanned)
	}
	if report.Assigned != 2 {
		t.Errorf("expected two valid assignments, got %d", report.Assigned)
	}
	if len(report.Malformed) != 1 || report.Malformed[0].Path != "bad.md" {
		t.Errorf("unexpected malformed diagnostics: %#v", report.Malformed)
	}
	if len(report.Duplicates) != 1 || report.Duplicates[0].ASN != 7 {
		t.Fatalf("unexpected duplicates: %#v", report.Duplicates)
	}
	if got, want := report.Duplicates[0].Paths, []string{"a.md", filepath.Join("nested", "b.md")}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("duplicate paths = %#v, want %#v", got, want)
	}
	if report.Healthy() {
		t.Error("report with malformed and duplicate ASN must be unhealthy")
	}
	if report.LowestFree() != 1 {
		t.Errorf("expected lowest free ASN 1, got %d", report.LowestFree())
	}
}

func TestWithASNLockSerializesConcurrentAllocators(t *testing.T) {
	dir := t.TempDir()
	entered := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- WithASNLock(dir, func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	secondDone := make(chan error, 1)
	go func() {
		secondDone <- WithASNLock(dir, func() error { return nil })
	}()
	select {
	case err := <-secondDone:
		t.Fatalf("second allocator entered before lock release: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
}
