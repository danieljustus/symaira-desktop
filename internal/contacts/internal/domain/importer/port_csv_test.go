package importer

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	contact "github.com/danieljustus/symaira-desktop/internal/contacts/internal/domain/contact"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

func TestPortContactsCSVContract(t *testing.T) {
	type sample struct {
		ID      string
		Input   string
		Header  []string
		Mapping ColumnMapping
		Rows    []ImportRow
		Issues  []ValidationIssue
		Error   string
	}
	inputs := []struct{ id, text string }{
		{"empty", ""}, {"header-only", "name,email\n"},
		{"basic", "name,email,phone,company,title,url\nAda, ADA@Example.COM ; second@example.com ,+49 (123) 45,Example,Engineer,https://EXAMPLE.com/\nAda,ada@example.com,,,,\n"},
		{"unicode", "first name,last name,email\n Zoë , 李 ,zoe@example.com\n, Müller ,\n,,nobody@example.com\n"},
		{"bom", "\ufeffname,email\nAda,ada@example.com\n"},
		{"ragged", "name,email\nAda\nBob,bob@example.com,extra\n,missing@example.com\n"},
		{"quoting", "name,email\r\n\"Ada, A\",\"a@example.com;b@example.com\"\r\n\"Two\nLines\",x\r\n\"A \"\"quote\"\"\",q\r\n"},
		{"malformed", "name,email\nA\"da,x\nGood,good@example.com\n\"Bad\"tail,b\nLast,last@example.com\n\"unterminated\n"},
		{"bad-header", "\"name\"oops,email\nAda,a\n"},
		{"alias-priority", "full name,name,Email,email address\nOther,Primary,A@b,C@d\n"},
		{"duplicate-header", "name,name,email\nFirst,Last,x\n"},
		{"blank-lines", "\nname,email\n\nAda,x\n\r\n , \n"},
		{"multi-empty", "name,email\nAda, ; ; \n"},
	}
	type norm struct {
		Kind     string
		Raw      string
		Expected string
	}
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../../.."))
	fixture := struct {
		Oracle         any               `json:"oracle"`
		SourceHashes   map[string]string `json:"source_hashes"`
		Cases          []sample          `json:"cases"`
		Normalizations []norm            `json:"normalizations"`
	}{Oracle: fixtureoracle.Current(), SourceHashes: map[string]string{}}
	for _, path := range []string{"internal/contacts/internal/domain/importer/csv.go", "internal/contacts/internal/domain/importer/types.go", "internal/contacts/internal/domain/contact/normalize.go"} {
		//nolint:gosec // path is derived from repository fixtures or the test-owned temporary root, not external input.
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256(data)
		fixture.SourceHashes[path] = hex.EncodeToString(h[:])
	}
	for _, kind := range []string{"email", "phone", "url", "handle", "address", "other"} {
		for _, raw := range []string{"  ADA@Example.COM  ", "ΟΣ İ K", "+49 (١٢٣) 456Ⅳ²", "https://EXAMPLE.COM///", "@@USER", " A\t B\n C\u00a0D "} {
			fixture.Normalizations = append(fixture.Normalizations, norm{kind, raw, contact.Normalize(contact.ContactPointKind(kind), raw)})
		}
	}
	fragments := []string{"a", "\"a\"", "\"a", "a\"", "a,b", "\"a\nb\"", "\r", "\"\""}
	for i, a := range fragments {
		for j, b := range fragments {
			inputs = append(inputs, struct{ id, text string }{fmt.Sprintf("grammar-%d-%d", i, j), "name,email\n" + a + "," + b + "\nLast,last@example.com\n"})
		}
	}
	for _, input := range inputs {
		// Detection receives the same production CSV decoder's first record.
		// Fixed mappings in malformed-header cases avoid hiding parser errors.
		var header []string
		mapping := ColumnMapping{DisplayName: "name", Email: "email"}
		if input.id != "bad-header" && input.id != "empty" {
			// encoding/csv is the production parser's header grammar.
			header, _ = csv.NewReader(strings.NewReader(input.text)).Read()
			mapping = DetectColumnMapping(header)
		}
		rows, issues, err := ParseCSV(strings.NewReader(input.text), mapping)
		c := sample{ID: input.id, Input: input.text, Header: header, Mapping: mapping, Rows: rows, Issues: issues}
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
	path := filepath.Join(root, "testdata/port/contacts/csv.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	} else {
		expected, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, expected) {
			t.Fatal("contacts CSV fixture drift")
		}
	}
}
