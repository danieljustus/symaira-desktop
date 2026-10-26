package importer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

func TestPortContactsVCardContract(t *testing.T) {
	type sample struct {
		ID     string
		Input  string
		Rows   []ImportRow
		Issues []ValidationIssue
		Error  string
	}
	fixtures := []struct{ id, input string }{
		{"empty", ""}, {"garbage", "garbage\n\nnot-a-property\n"},
		{"basic", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Ada Lovelace\r\nN:Lovelace;Ada;;;\r\nORG:Example;Research\r\nTITLE:Engineer\r\nEMAIL:ADA@example.com\r\nTEL:+49 (123)\r\nURL:https://example.com/\r\nUID:one\r\nEND:VCARD\r\n"},
		{"fallback", "BEGIN:VCARD\nN:李;Zoë\nEND:VCARD\n"},
		{"nameless", "BEGIN:VCARD\nEMAIL:x@y\nEND:VCARD\n"},
		{"no-end", "BEGIN:VCARD\nFN:Test\n"},
		{"folded", "BEGIN:VCARD\r\nFN:Long\r\n Name\r\n\t More\r\nEND:VCARD\r\n"},
		{"folded-no-newline", "BEGIN:VCARD\nFN:Long\n Name"},
		{"no-begin", "FN:Ada\nEND:VCARD\n"},
		{"bad-begin", "BEGIN:OTHER\nFN:Ada\nEND:VCARD\n"},
		{"bad-end", "BEGIN:VCARD\nFN:Ada\nEND:OTHER\n"},
		{"nested-begin", "BEGIN:VCARD\nBEGIN:VCARD\nFN:Ada\nEND:VCARD"},
		{"preferences", "BEGIN:VCARD\nFN:First\nFN;TYPE=PREF:Apple\nFN;PREF=bad:InvalidWins\nN;PREF=2:Family;Given\nEMAIL:a@b\nEMAIL:c@d\nEND:VCARD\n"},
		{"escapes", "BEGIN:VCARD\nFN:Comma\\, New\\nLine\\\\Slash\nN:Fa\\;m;Gi\\,ven\nORG:Org\\;Unit;Team\nEMAIL:\nEND:VCARD\n"},
		{"groups", "begin:vcard\nitem1.FN:Grouped\nitem2.EMAIL;TYPE=HOME,INTERNET:a@b\nend:vcard\n"},
		{"multiple", "BEGIN:VCARD\nFN:One\nEND:VCARD\n\nBEGIN:VCARD\nN:Two;Second\nEND:VCARD\n"},
		{"bom", "\ufeffBEGIN:VCARD\nFN:Name\nEND:VCARD\n"},
		{"whitespace", "BEGIN:VCARD\nFN:  \nN: Family ; Given \nEND:VCARD\n"},
	}
	params := []string{"", ";PREF=1", ";PREF=-1", ";PREF=101", ";TYPE=pref", ";TYPE=HOME,pref", ";PREF=\"2\"", ";PREF=\"bad\"", ";PREF=\"a\\n\"", ";PREF=\"\\x31\"", ";PREF=\"\\u0031\"", ";PREF=\"\\061\"", ";PREF=\"\\q\"", ";TYPE;HOME", ";BROKEN", ";PREF=\"unterminated", ";PREF=\"1\"x", ";PREF=", ";TYPE=\"pref,home\"", ";PREF=1;PREF=2", ";PREF=9999999999999999999999999", ";PREF=-9999999999999999999999999"}
	for _, param := range params {
		fixtures = append(fixtures, struct{ id, input string }{"parameter:" + param, "BEGIN:VCARD\nFN:Default\nFN" + param + ":Chosen\nEND:VCARD\n"})
	}
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../../.."))
	fixture := struct {
		Oracle       any               `json:"oracle"`
		SourceHashes map[string]string `json:"source_hashes"`
		Cases        []sample          `json:"cases"`
	}{Oracle: fixtureoracle.Current(), SourceHashes: map[string]string{}}
	for _, p := range []string{"internal/contacts/internal/domain/importer/vcard.go", "internal/contacts/internal/domain/importer/types.go", "go.mod", "go.sum"} {
		data, err := os.ReadFile(filepath.Join(root, p))
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256(data)
		fixture.SourceHashes[p] = hex.EncodeToString(h[:])
	}
	for _, input := range fixtures {
		rows, issues, err := ParseVCard(strings.NewReader(input.input))
		c := sample{ID: input.id, Input: input.input, Rows: rows, Issues: issues}
		if err != nil {
			c.Error = err.Error()
		}
		fixture.Cases = append(fixture.Cases, c)
	}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(root, "testdata/port/contacts/vcard.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
	} else {
		expected, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, expected) {
			t.Fatal("contacts vCard fixture drift")
		}
	}
}
