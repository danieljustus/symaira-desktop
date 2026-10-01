package engine

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"unicode"
)

type localHashFixture struct {
	SchemaVersion            int                    `json:"schema_version"`
	ProductionSourceSHA256   string                 `json:"production_source_sha256"`
	SimpleLowerMappingSHA256 string                 `json:"simple_lower_mapping_sha256"`
	Cases                    []localHashFixtureCase `json:"cases"`
}

type localHashFixtureCase struct {
	ID         string    `json:"id"`
	Text       string    `json:"text"`
	Dimensions int       `json:"dimensions"`
	Vector     []float32 `json:"vector"`
}

func TestLocalHashPortFixture(t *testing.T) {
	root := localHashRepoRoot(t)
	cases := localHashCases()
	fixture := localHashFixture{SchemaVersion: 1, Cases: cases}
	if os.Getenv("PORT_GENERATE") == "1" {
		fixture.ProductionSourceSHA256 = localHashFileSHA256(t, filepath.Join(root, "internal/retrieval/internal/engine/embeddings.go"))
		fixture.SimpleLowerMappingSHA256 = goSimpleLowerMappingSHA256()
		for index := range fixture.Cases {
			fixture.Cases[index].Vector = GenerateLocalHashVector(fixture.Cases[index].Text, fixture.Cases[index].Dimensions)
		}
		encoded, err := json.MarshalIndent(fixture, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		encoded = append(encoded, '\n')
		path := filepath.Join(root, "testdata/port/retrieval/local-hash.json")
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}

	path := filepath.Join(root, "testdata/port/retrieval/local-hash.json")
	fixtureRoot, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fixtureRoot.Close() }()
	data, err := fixtureRoot.ReadFile(filepath.Base(path))
	if err != nil {
		t.Fatalf("read Go-generated local-hash fixture (generate with PORT_GENERATE=1): %v", err)
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if fixture.SchemaVersion != 1 {
		t.Fatalf("fixture schema_version = %d, want 1", fixture.SchemaVersion)
	}
	if got := localHashFileSHA256(t, filepath.Join(root, "internal/retrieval/internal/engine/embeddings.go")); fixture.ProductionSourceSHA256 != got {
		t.Fatalf("Go production source SHA256 = %s, fixture pins %s; regenerate the oracle", got, fixture.ProductionSourceSHA256)
	}
	if got := goSimpleLowerMappingSHA256(); fixture.SimpleLowerMappingSHA256 != got {
		t.Fatalf("Go Unicode simple-lower mapping SHA256 = %s, fixture pins %s; regenerate the oracle", got, fixture.SimpleLowerMappingSHA256)
	}
	if len(fixture.Cases) != len(cases) {
		t.Fatalf("fixture contains %d cases, generator defines %d", len(fixture.Cases), len(cases))
	}
	for index, want := range cases {
		got := fixture.Cases[index]
		if got.ID != want.ID || got.Text != want.Text || got.Dimensions != want.Dimensions {
			t.Fatalf("fixture case %d identity changed: got %q/%q/%d, want %q/%q/%d", index, got.ID, got.Text, got.Dimensions, want.ID, want.Text, want.Dimensions)
		}
		wantVector := GenerateLocalHashVector(want.Text, want.Dimensions)
		if !reflect.DeepEqual(got.Vector, wantVector) {
			t.Fatalf("fixture vector %q is stale; regenerate the oracle", want.ID)
		}
	}
}

func localHashCases() []localHashFixtureCase {
	long := []string{"the"}
	for index := 0; index < 40; index++ {
		long = append(long, fmt.Sprintf("word%02d", index))
	}
	return []localHashFixtureCase{
		{ID: "empty-default-dimension", Text: "", Dimensions: 768},
		{ID: "ascii-whitespace-only", Text: " \t\n\r", Dimensions: 3},
		{ID: "unicode-whitespace", Text: "alpha\u00a0beta\u0085gamma\u2003delta", Dimensions: 7},
		{ID: "all-english-stopwords", Text: "AND the a an of to in is it that", Dimensions: 8},
		{ID: "all-german-stopwords", Text: "UND DER die das ein eine ist es dass von zu mit auf für den dem des im am", Dimensions: 8},
		{ID: "punctuation-and-small-dimension-collisions", Text: "Alpha, alpha! ALPHA—beta? x/y can't re-enter (Gamma) [delta] {epsilon} underscore_name", Dimensions: 3},
		{ID: "single-slot-collisions", Text: "alpha the beta und gamma delta", Dimensions: 1},
		{ID: "unicode-simple-lowercase", Text: "İ Σ ΟΣΟΣ Straße ÄÖÜ ß Καλημέρα \u1C89\uA7CB\U00010D50\U00016EA0", Dimensions: 17},
		{ID: "token-position-after-sha-bytes", Text: strings.Join(long, " "), Dimensions: 11},
		{ID: "default-768-nontrivial", Text: "A local retrieval query with punctuation, German Wörter und Unicode 🧪", Dimensions: 768},
	}
}

func goSimpleLowerMappingSHA256() string {
	hash := sha256.New()
	var encoded [8]byte
	for value := rune(0); value <= unicode.MaxRune; value++ {
		if 0xD800 <= value && value <= 0xDFFF {
			continue
		}
		binary.BigEndian.PutUint32(encoded[:4], runeAsUint32(value))
		binary.BigEndian.PutUint32(encoded[4:], runeAsUint32(unicode.ToLower(value)))
		_, _ = hash.Write(encoded[:])
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// runeAsUint32 is used only for the Unicode scalar range, bounded by the loop
// above to [0, unicode.MaxRune] and excluding surrogate code points.
//
//nolint:gosec // G115: callers bound values to Unicode scalar range before conversion.
func runeAsUint32(value rune) uint32 {
	return uint32(value)
}

func localHashFileSHA256(t *testing.T, path string) string {
	t.Helper()
	fixtureRoot, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fixtureRoot.Close() }()
	data, err := fixtureRoot.ReadFile(filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func localHashRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve Go fixture source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../../.."))
}
