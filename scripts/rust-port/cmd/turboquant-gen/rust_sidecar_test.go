package main

import (
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"testing"

	"github.com/danieljustus/symaira-corekit/vectorkit/turboquant"
)

type rustSidecarFixture struct {
	SchemaVersion int               `json:"schema_version"`
	Writer        string            `json:"writer"`
	Cases         []rustSidecarCase `json:"cases"`
}

type rustSidecarCase struct {
	ID       string `json:"id"`
	BlobHex  string `json:"blob_hex"`
	MetaJSON string `json:"meta_json"`
}

// TestGoReadsRustSidecars feeds sidecars written by the Rust port into the
// pinned CoreKit Go decoder. Each Rust blob and metadata pair must unpack,
// validate against the codec, decode to exactly the values Go decodes from its
// own blob, score identically, and serialise to the same JSON as Go.
func TestGoReadsRustSidecars(t *testing.T) {
	property := readPropertyCases(t)
	rustRaw, err := os.ReadFile("../../../../testdata/port/retrieval/turboquant-rust-sidecars.json")
	if err != nil {
		t.Fatal(err)
	}
	var rust rustSidecarFixture
	if err := json.Unmarshal(rustRaw, &rust); err != nil {
		t.Fatal(err)
	}
	if rust.SchemaVersion != 1 || rust.Writer != "symdesk-index turboquant-sidecar-writer" {
		t.Fatalf("unexpected Rust sidecar fixture identity: %d %q", rust.SchemaVersion, rust.Writer)
	}
	if len(rust.Cases) != len(property) {
		t.Fatalf("rust sidecar cases = %d, property cases = %d", len(rust.Cases), len(property))
	}
	for i, pc := range property {
		rc := rust.Cases[i]
		if rc.ID != pc.ID {
			t.Fatalf("case %d id %s != %s", i, rc.ID, pc.ID)
		}
		codec, err := turboquant.NewCodec(pc.Dim, turboquant.BitWidth(pc.BitWidth), int(pc.Seed), 0)
		if err != nil {
			t.Fatalf("%s: %v", pc.ID, err)
		}
		vector := decodeF32s32(t, pc.VectorF32Hex)
		query := decodeF32s32(t, pc.QueryF32Hex)

		goBlob, goMeta, err := codec.EncodeSidecar(vector, 0)
		if err != nil {
			t.Fatalf("%s go encode: %v", pc.ID, err)
		}
		rustBlob, err := hex.DecodeString(rc.BlobHex)
		if err != nil {
			t.Fatalf("%s rust blob hex: %v", pc.ID, err)
		}
		var rustMeta turboquant.SidecarMeta
		if err := json.Unmarshal([]byte(rc.MetaJSON), &rustMeta); err != nil {
			t.Fatalf("%s rust meta json: %v", pc.ID, err)
		}
		goMetaJSON, err := json.Marshal(goMeta)
		if err != nil {
			t.Fatal(err)
		}
		if string(goMetaJSON) != rc.MetaJSON {
			t.Fatalf("%s: Rust metadata JSON %s != Go %s", pc.ID, rc.MetaJSON, goMetaJSON)
		}
		if err := turboquant.ValidateSidecarMeta(&rustMeta, codec); err != nil {
			t.Fatalf("%s validate Rust metadata: %v", pc.ID, err)
		}
		fromRust, err := turboquant.DecodeSidecar(rustBlob, &rustMeta)
		if err != nil {
			t.Fatalf("%s go decode of Rust sidecar: %v", pc.ID, err)
		}
		fromGo, err := turboquant.DecodeSidecar(goBlob, goMeta)
		if err != nil {
			t.Fatalf("%s go decode of Go sidecar: %v", pc.ID, err)
		}
		for j := range fromGo {
			if math.Float32bits(fromRust[j]) != math.Float32bits(fromGo[j]) {
				t.Fatalf("%s: decoded[%d] from Rust bytes differs from Go decode", pc.ID, j)
			}
		}
		rustPacked, err := turboquant.UnpackSidecarBlob(rustBlob)
		if err != nil {
			t.Fatalf("%s unpack Rust sidecar: %v", pc.ID, err)
		}
		goPacked, err := turboquant.UnpackSidecarBlob(goBlob)
		if err != nil {
			t.Fatalf("%s unpack Go sidecar: %v", pc.ID, err)
		}
		if math.Float32bits(codec.Score(query, rustPacked)) != math.Float32bits(codec.Score(query, goPacked)) {
			t.Fatalf("%s: score from Rust bytes differs from Go score", pc.ID)
		}
	}
}

func readPropertyCases(t *testing.T) []propertyCase {
	t.Helper()
	raw, err := os.ReadFile("../../../../testdata/port/retrieval/turboquant-property.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture propertyFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Cases) == 0 {
		t.Fatal("property corpus is empty")
	}
	return fixture.Cases
}

func decodeF32s32(t *testing.T, hexText string) []float32 {
	t.Helper()
	values, err := decodeF32s(hexText)
	if err != nil {
		t.Fatal(err)
	}
	return values
}
