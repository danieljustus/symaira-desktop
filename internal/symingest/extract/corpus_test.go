package extract

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestMalformedCorpusOutcome enforces the recovery policy encoded in
// testdata/malformed: every fixture is named <name>--<outcome>.<ext> and the
// extraction result must match the outcome class.
//
//	errors   - extraction fails with an error (document is quarantined)
//	recovers - extraction succeeds and returns partial text
//	skips    - extraction succeeds with empty text (the offending part is skipped)
func TestMalformedCorpusOutcome(t *testing.T) {
	kindByExt := map[string]Kind{
		".html": KindHTML, ".rtf": KindRTF, ".docx": KindDOCX, ".xlsx": KindXLSX,
		".pptx": KindPPTX, ".odt": KindODT, ".ods": KindODS, ".odp": KindODP,
		".epub": KindEPUB, ".eml": KindEML,
	}
	err := filepath.WalkDir("testdata/malformed", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		ext := strings.ToLower(filepath.Ext(base))
		stem := strings.TrimSuffix(base, filepath.Ext(base))
		idx := strings.LastIndex(stem, "--")
		if idx < 0 {
			t.Fatalf("fixture %q must follow the name--outcome.ext convention", path)
		}
		outcome := stem[idx+2:]
		kind, ok := kindByExt[ext]
		if !ok {
			t.Fatalf("fixture %q has no kind mapping for extension %q", path, ext)
		}
		res, err := ReadStructuredKind(context.Background(), path, kind)
		switch outcome {
		case "errors":
			if err == nil {
				t.Errorf("%s: expected an error, got text %q", base, res.Text)
			}
		case "recovers":
			if err != nil {
				t.Errorf("%s: expected recovery, got error %v", base, err)
				return nil
			}
			if strings.TrimSpace(res.Text) == "" {
				t.Errorf("%s: expected partial text, got empty", base)
			}
		case "skips":
			if err != nil {
				t.Errorf("%s: expected skip, got error %v", base, err)
			}
		default:
			t.Fatalf("fixture %q has unknown outcome class %q", base, outcome)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
