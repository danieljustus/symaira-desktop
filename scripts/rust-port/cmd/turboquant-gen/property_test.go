package main

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestPropertyCheckAcceptsItsOwnOutput(t *testing.T) {
	generated, err := generateProperty()
	if err != nil {
		t.Fatal(err)
	}
	if err := checkProperty(generated, generated); err != nil {
		t.Fatalf("own output rejected: %v", err)
	}
}

func TestPropertyCheckRejectsDecodeDriftBeyondTolerance(t *testing.T) {
	generated, mutated := mutatedProperty(t, func(c *propertyCase) {
		raw, err := hex.DecodeString(c.DecodedF32Hex)
		if err != nil {
			t.Fatal(err)
		}
		binary.LittleEndian.PutUint32(raw, math.Float32bits(1e9))
		c.DecodedF32Hex = hex.EncodeToString(raw)
	})
	if err := checkProperty(mutated, generated); err == nil || !strings.Contains(err.Error(), "decoded") {
		t.Fatalf("decode drift accepted: %v", err)
	}
}

func TestPropertyCheckRejectsScoreDriftBeyondTolerance(t *testing.T) {
	generated, mutated := mutatedProperty(t, func(c *propertyCase) {
		c.ScoreF32Bits = math.Float32bits(1e9)
	})
	if err := checkProperty(mutated, generated); err == nil || !strings.Contains(err.Error(), "score") {
		t.Fatalf("score drift accepted: %v", err)
	}
}

func TestPropertyCheckRejectsExactFieldDrift(t *testing.T) {
	generated, mutated := mutatedProperty(t, func(c *propertyCase) {
		c.PackedHex = "00" + c.PackedHex[2:]
	})
	if err := checkProperty(mutated, generated); err == nil || !strings.Contains(err.Error(), "exact") {
		t.Fatalf("packed-byte drift accepted: %v", err)
	}
}

func TestPropertyCheckRejectsToleranceChange(t *testing.T) {
	generated, mutated := mutatedProperty(t, func(c *propertyCase) {
		c.DecodeTolerance *= 2
	})
	if err := checkProperty(mutated, generated); err == nil || !strings.Contains(err.Error(), "tolerance") {
		t.Fatalf("tolerance change accepted: %v", err)
	}
}

func TestPropertyCheckRejectsMalformedInput(t *testing.T) {
	generated, err := generateProperty()
	if err != nil {
		t.Fatal(err)
	}
	if err := checkProperty([]byte("{"), generated); err == nil {
		t.Fatal("malformed committed corpus accepted")
	}
	if err := checkProperty(generated, []byte("{")); err == nil {
		t.Fatal("malformed generated corpus accepted")
	}
}

func TestDecodeF32sRejectsMalformedHex(t *testing.T) {
	if _, err := decodeF32s("abc"); err == nil {
		t.Fatal("odd-length hex accepted")
	}
	if _, err := decodeF32s("0102030405"); err == nil {
		t.Fatal("hex that is not whole f32 words accepted")
	}
	values, err := decodeF32s("0000803f")
	if err != nil || len(values) != 1 || values[0] != 1 {
		t.Fatalf("decodeF32s(1.0) = %v, %v", values, err)
	}
}

// mutatedProperty returns the generated corpus and a copy with one case edited.
func mutatedProperty(t *testing.T, edit func(*propertyCase)) ([]byte, []byte) {
	t.Helper()
	generated, err := generateProperty()
	if err != nil {
		t.Fatal(err)
	}
	var fixture propertyFixture
	if err := json.Unmarshal(generated, &fixture); err != nil {
		t.Fatal(err)
	}
	edit(&fixture.Cases[0])
	mutated, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	return generated, mutated
}
