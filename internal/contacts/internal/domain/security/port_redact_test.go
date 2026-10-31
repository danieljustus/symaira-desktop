package security

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

// CONTACT-001: the last-line-of-defense Redact masks, recorded from the real
// Go regexp (RE2) implementation. ASCII-only \d and \s semantics, dotted-quad
// preservation and leftmost matching are the points a Rust port can get wrong.
//
// Regenerate with:
// PORT_GENERATE=1 go test ./internal/contacts/internal/domain/security -run '^TestPortContactsRedactContract$'
func TestPortContactsRedactContract(t *testing.T) {
	inputs := []string{
		"",
		"plain text without contact data",
		"mail ada@example.com now",
		"ADA.LOVELACE+tag@Sub.Example.CO.UK and bob@x.io",
		"ends with dot ada@example.com.",
		"not an email: @example.com, ada@, ada@example, ada@.com",
		"ada@exa_mple.com",
		"unicode local ädä@example.com and ada@exämple.com",
		"call +49 170 1234567 today",
		"call (555) 123-4567 or 555.123.4567",
		"+1-800-555-0199x",
		"short 123456 and 1234567 and 12345678",
		"exactly eight 12345678 digits and 123456789",
		"date 2026-01-02 and 2026-01-02 03:04:05",
		"ip 192.168.1.1 stays",
		"ip with port 10.0.0.1:8080 stays",
		"address bind: 127.0.0.1 phone 0170 123 4567 end",
		"three digits 1 2 3 and 12 34 56 78 90",
		"tab\tseparated 555\t123\t4567 phone",
		"newline 555\n123\n4567 split",
		"vertical\vtab 555\v123\v4567 phone",
		"form\ffeed 555\f123\f4567 phone",
		"nbsp 555\u00a0123\u00a04567 phone",
		"arabic-indic digits ٠١٢٣٤٥٦٧٨٩ and fullwidth １２３４５６７８９０",
		"+٤٩ 170 1234567",
		"mixed ada@example.com and +49 170 1234567 and 10.1.1.1",
		"error: lookup failed for ada@example.com (+1 555 123 4567): timeout",
		"trailing digits 99 and a long run 123456789012345678901234567890",
		"minus-only 1-2-3-4-5-6-7-8-9",
		"+ alone, + 1, ++1234567890",
		"double@@example.com and x@y@z.com",
		"e-mail: <ada@example.com>",
		"json {\"email\":\"ada@example.com\",\"phone\":\"+4917012345678\"}",
		"ipv4-looking phone 123.456.789.012 stays dotted quad",
		"1.2.3.4 and 1.2.3.4.5",
	}
	type pair struct {
		Input  string `json:"input"`
		Output string `json:"output"`
	}
	cases := make([]pair, 0, len(inputs))
	changed := 0
	for _, in := range inputs {
		out := Redact(in)
		if out != in {
			changed++
		}
		cases = append(cases, pair{in, out})
	}
	if changed < 15 {
		t.Fatalf("only %d redactions exercised", changed)
	}
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../../.."))
	b, err := os.ReadFile(filepath.Join(root, "internal/contacts/internal/domain/security/redact.go")) //nolint:gosec // fixed repository path
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	fixture := map[string]any{"oracle": fixtureoracle.Current(), "source_hashes": map[string]string{"internal/contacts/internal/domain/security/redact.go": hex.EncodeToString(sum[:])}, "cases": cases}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(root, "testdata/port/contacts/redact.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	expected, err := os.ReadFile(path) //nolint:gosec // fixed fixture path
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(expected, data) {
		t.Fatal("contacts redact fixture drift")
	}
}
