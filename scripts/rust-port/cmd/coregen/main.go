// Command coregen freezes deterministic pure-core Go behavior for the Rust port.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/danieljustus/symaira-desktop/internal/documentformat"
	"github.com/danieljustus/symaira-desktop/internal/searchquery"
	"github.com/danieljustus/symaira-desktop/internal/simhash"
	"github.com/danieljustus/symaira-desktop/internal/textnorm"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

type fixtureDocument[T any] struct {
	SchemaVersion int              `json:"schema_version"`
	Oracle        inventory.Oracle `json:"oracle"`
	Cases         T                `json:"cases"`
}

type simhashCase struct {
	ID            string `json:"id"`
	Text          string `json:"text"`
	Hash          uint64 `json:"hash"`
	Hex           string `json:"hex"`
	ContentLength int    `json:"content_length"`
}

type simhashPair struct {
	ID                string `json:"id"`
	Left              string `json:"left"`
	Right             string `json:"right"`
	HammingDistance   int    `json:"hamming_distance"`
	Similarity        int    `json:"similarity"`
	ContentSimilarity int    `json:"content_similarity"`
}

type simhashParseCase struct {
	Input string `json:"input"`
	Value uint64 `json:"value,omitempty"`
	Error string `json:"error,omitempty"`
}

type simhashCases struct {
	MinimumBodyLength      int                `json:"minimum_body_length"`
	ShortBodySimilarityCap int                `json:"short_body_similarity_cap"`
	Fingerprints           []simhashCase      `json:"fingerprints"`
	Pairs                  []simhashPair      `json:"pairs"`
	Parse                  []simhashParseCase `json:"parse"`
}

type formatSpec struct {
	Extension string `json:"extension"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Supported bool   `json:"supported"`
	Reason    string `json:"reason,omitempty"`
	Error     string `json:"error,omitempty"`
}

type formatLookup struct {
	Input      string `json:"input"`
	Normalized string `json:"normalized"`
	Kind       string `json:"kind,omitempty"`
	Recognized bool   `json:"recognized"`
	Supported  bool   `json:"supported"`
}

type formatCases struct {
	Registry            []formatSpec   `json:"registry"`
	SupportedExtensions []string       `json:"supported_extensions"`
	Lookups             []formatLookup `json:"lookups"`
	UnknownError        string         `json:"unknown_error"`
	DRMError            string         `json:"drm_error"`
}

type textNormCase struct {
	ID     string `json:"id"`
	Input  string `json:"input"`
	Output string `json:"output"`
}

type languageCase struct {
	ID       string `json:"id"`
	Input    string `json:"input"`
	Language string `json:"language"`
}

type textNormCases struct {
	Dehyphenate []textNormCase `json:"dehyphenate"`
	Language    []languageCase `json:"language"`
}

type germanCase struct {
	ID            string   `json:"id"`
	Input         string   `json:"input"`
	Tokens        []string `json:"tokens"`
	Normalized    string   `json:"normalized"`
	FTSQuery      string   `json:"fts_query"`
	TrigramQuery  string   `json:"trigram_query"`
	FTSTerm       string   `json:"fts_term"`
	TrigramTerm   string   `json:"trigram_term"`
	PhraseFTSTerm string   `json:"phrase_fts_term"`
	PhraseTrigram string   `json:"phrase_trigram_term"`
}

func main() {
	outputDir := flag.String("output-dir", "testdata/port/core", "fixture directory")
	check := flag.Bool("check", false, "fail if generated fixtures differ")
	commit := flag.String("oracle-commit", "ae86331930fdfa2b128b68ae5af7437091b9949a", "Go oracle commit")
	release := flag.String("oracle-release", "v0.12.2", "Go oracle release")
	flag.Parse()
	oracle := inventory.Oracle{Commit: *commit, Release: *release}

	fixtures := map[string]any{
		"simhash.json":          fixtureDocument[simhashCases]{SchemaVersion: 1, Oracle: oracle, Cases: buildSimhashCases()},
		"document-formats.json": fixtureDocument[formatCases]{SchemaVersion: 1, Oracle: oracle, Cases: buildFormatCases()},
		"textnorm.json":         fixtureDocument[textNormCases]{SchemaVersion: 1, Oracle: oracle, Cases: buildTextNormCases()},
		"german-search.json":    fixtureDocument[[]germanCase]{SchemaVersion: 1, Oracle: oracle, Cases: buildGermanCases()},
	}
	for name, value := range fixtures {
		if err := writeOrCheck(filepath.Join(*outputDir, name), value, *check); err != nil {
			fatal("%s: %v", name, err)
		}
	}
	fmt.Printf("PASS pure-core fixtures (%d files)\n", len(fixtures))
}

func buildSimhashCases() simhashCases {
	texts := []struct{ id, text string }{
		{"empty", ""},
		{"ascii", "The quick brown fox jumps over the lazy dog"},
		{"case-and-space", "  HELLO   hello\tWorld  "},
		{"unicode", "Ärger über große Rechnungen 東京"},
		{"punctuation", "invoice-2026, invoice-2026! paid?"},
	}
	fingerprints := make([]simhashCase, 0, len(texts))
	for _, item := range texts {
		fingerprints = append(fingerprints, simhashCase{ID: item.id, Text: item.text, Hash: simhash.Compute(item.text), Hex: simhash.ComputeHex(item.text), ContentLength: simhash.ContentLength(item.text)})
	}
	pairs := []struct{ id, left, right string }{
		{"identical-short", "same short note", "same short note"},
		{"near", "Monthly utility bill for Alice from Power Co. Amount due: $150.00", "Monthly utility bill for Alice from Power Co. Amount due: $155.00"},
		{"different", "Monthly utility bill for Alice from Power Co.", "Car insurance renewal notice for Bob from SafeDrive Inc."},
		{"long", repeat("monthly utility bill amount due ", 8), repeat("monthly utility bill amount paid ", 8)},
	}
	pairResults := make([]simhashPair, 0, len(pairs))
	for _, item := range pairs {
		left, right := simhash.Compute(item.left), simhash.Compute(item.right)
		pairResults = append(pairResults, simhashPair{ID: item.id, Left: item.left, Right: item.right, HammingDistance: simhash.HammingDistance(left, right), Similarity: simhash.Similarity(left, right), ContentSimilarity: simhash.SimilarityForContent(left, right, item.left, item.right)})
	}
	parse := make([]simhashParseCase, 0, 4)
	for _, input := range []string{"0000000000000000", simhash.ComputeHex("hello"), "short", "zzzzzzzzzzzzzzzz"} {
		value, err := simhash.ParseHex(input)
		item := simhashParseCase{Input: input, Value: value}
		if err != nil {
			item.Error = err.Error()
		}
		parse = append(parse, item)
	}
	return simhashCases{MinimumBodyLength: simhash.MinimumBodyLength, ShortBodySimilarityCap: simhash.ShortBodySimilarityCap, Fingerprints: fingerprints, Pairs: pairResults, Parse: parse}
}

func buildFormatCases() formatCases {
	registry := make([]formatSpec, 0, len(documentformat.SupportedFormats)+len(documentformat.UnsupportedFormats))
	for _, item := range documentformat.SupportedFormats {
		registry = append(registry, formatSpec{Extension: item.Extension, Kind: string(item.Kind), Name: item.Name, Supported: true})
	}
	for _, item := range documentformat.UnsupportedFormats {
		registry = append(registry, formatSpec{Extension: item.Extension, Kind: string(item.Kind), Name: item.Name, Reason: item.Reason, Error: documentformat.UnsupportedFormatError(item.Kind).Error()})
	}
	lookups := make([]formatLookup, 0)
	for _, input := range []string{"PDF", " .Md ", ".markdown", "AZW3", ".pages", "", ".unknown"} {
		kind, recognized := documentformat.KindForExtension(input)
		lookups = append(lookups, formatLookup{Input: input, Normalized: documentformat.NormalizeExtension(input), Kind: string(kind), Recognized: recognized, Supported: documentformat.IsSupported(input)})
	}
	return formatCases{Registry: registry, SupportedExtensions: documentformat.SupportedExtensions(), Lookups: lookups, UnknownError: documentformat.UnsupportedFormatError(documentformat.Kind("application/x-unknown")).Error(), DRMError: documentformat.ErrDRMProtected.Error()}
}

func buildTextNormCases() textNormCases {
	dehyphenateInputs := []struct{ id, input string }{
		{"joined", "Die Kranken-\nversicherung zahlt. Der Miet-\nvertrag läuft."},
		{"many-lines", "Ver-\nsi-\nche-\nrung"},
		{"uppercase", "Baden-\nWürttemberg"},
		{"coordinator", "Vor-\nund Nachname"},
		{"indented", "Vor-\n und Nachname"},
		{"digit", "1-\n2 Punkte"},
		{"windows-newline", "Kranken-\r\nversicherung"},
		{"no-newline", "Kranken-versicherung"},
	}
	dehyphenate := make([]textNormCase, 0, len(dehyphenateInputs))
	for _, item := range dehyphenateInputs {
		dehyphenate = append(dehyphenate, textNormCase{ID: item.id, Input: item.input, Output: textnorm.Dehyphenate(item.input)})
	}
	languageInputs := []struct{ id, input string }{
		{"german", "Der Vertrag wurde zwischen den Parteien geschlossen und ist gültig. Die Beiträge für die Krankenversicherung werden monatlich gezahlt. Wir können die Änderung nicht übernehmen, wenn die Frist schon abgelaufen ist."},
		{"english", "The contract was signed between the parties and is valid. The contributions for the insurance are paid monthly and this will not change. We cannot accept the modification when the deadline has already passed."},
		{"short", "kurz"},
		{"ambiguous", "der the und and die is das are und the der is und the der is und the der is"},
		{"umlaut-signal", "alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu nu xi omicron pi rho sigma über größe für"},
	}
	language := make([]languageCase, 0, len(languageInputs))
	for _, item := range languageInputs {
		language = append(language, languageCase{ID: item.id, Input: item.input, Language: textnorm.DetectLanguage(item.input)})
	}
	return textNormCases{Dehyphenate: dehyphenate, Language: language}
}

func buildGermanCases() []germanCase {
	inputs := []struct{ id, input string }{
		{"invoice", "Die Rechnungen sind geprüft"},
		{"duplicates", "Rechnung rechnungen RECHNUNG"},
		{"umlauts", "größere Überweisungen"},
		{"punctuation", "(Krankenversicherung), bezahlt!"},
		{"stopwords", "die und der"},
		{"short-trigram", "AI"},
		{"compound", "Beitragsbemessungsgrenze"},
		{"phrase", "jährliche Beiträge"},
	}
	result := make([]germanCase, 0, len(inputs))
	for _, item := range inputs {
		result = append(result, germanCase{ID: item.id, Input: item.input, Tokens: searchquery.GermanSearchTokens(item.input), Normalized: searchquery.GermanNormText(item.input), FTSQuery: searchquery.GermanFTSQuery(item.input), TrigramQuery: searchquery.GermanTrigramQuery(item.input), FTSTerm: searchquery.GermanFTSTerm(item.input, false), TrigramTerm: searchquery.GermanTrigramTerm(item.input, false), PhraseFTSTerm: searchquery.GermanFTSTerm(item.input, true), PhraseTrigram: searchquery.GermanTrigramTerm(item.input, true)})
	}
	return result
}

func repeat(value string, count int) string {
	var output bytes.Buffer
	for range count {
		output.WriteString(value)
	}
	return output.String()
}

func writeOrCheck(path string, value any, check bool) error {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	if check {
		existing, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(existing, content) {
			return fmt.Errorf("fixture drift; regenerate deliberately")
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0o600)
}

func fatal(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "FAIL "+format+"\n", args...)
	os.Exit(1)
}
