package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"reflect"

	"github.com/danieljustus/symaira-corekit/vectorkit/turboquant"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

// Decode and score values depend on FMA fusion, which Go applies on arm64 but
// not amd64. The property corpus therefore records the pinned Go values and a
// bound derived from the exact min/max and rotation, instead of exact bits.
const (
	propertyCaseCount = 200
	// decodeToleranceExp bounds |Rust-Go| for decoded components at
	// 2^-20 * max(|min|,|max|), about 1 f32 ulp of the code range.
	decodeToleranceExp = -20
	// scoreToleranceExp bounds |Rust-Go| for scores at
	// 2^-16 * sum|rotated query| * max(|min|,|max|).
	scoreToleranceExp = -16
)

type propertyFixture struct {
	SchemaVersion int            `json:"schema_version"`
	Oracle        oracleSource   `json:"oracle"`
	Cases         []propertyCase `json:"cases"`
}

type propertyCase struct {
	ID              string  `json:"id"`
	Dim             int     `json:"dim"`
	BitWidth        int     `json:"bit_width"`
	Seed            int64   `json:"seed"`
	VectorF32Hex    string  `json:"vector_f32_hex"`
	QueryF32Hex     string  `json:"query_f32_hex"`
	RotDim          int     `json:"rot_dim"`
	PackedHex       string  `json:"packed_hex"`
	MinF64Bits      uint64  `json:"min_f64_bits"`
	MaxF64Bits      uint64  `json:"max_f64_bits"`
	DecodedF32Hex   string  `json:"decoded_f32_hex"`
	DecodeTolerance float64 `json:"decode_abs_tolerance"`
	ScoreF32Bits    uint32  `json:"score_f32_bits"`
	ScoreTolerance  float64 `json:"score_abs_tolerance"`
}

// propertyCases builds a deterministic corpus across dimensions 1..128 plus
// one 768-dim case, all bit widths, full-range int64 seeds and scales.
func propertyCases() ([]propertyCase, error) {
	widths := []turboquant.BitWidth{
		turboquant.BitWidth2, turboquant.BitWidth3, turboquant.BitWidth4,
		turboquant.BitWidthHalf2and3, turboquant.BitWidthHalf3and4,
	}
	scales := []float32{1e-3, 1, 1e3, 1e6}
	cases := make([]propertyCase, 0, propertyCaseCount)
	for i := 0; i < propertyCaseCount; i++ {
		state := uint64(i)*0x9e3779b97f4a7c15 + 0x1234
		dim := 1 + int(splitmix64(&state)%128)
		if i == propertyCaseCount-1 {
			dim = 768
		}
		bw := widths[i%len(widths)]
		seed := int64(splitmix64(&state)) //nolint:gosec // full-range seed by design
		vector := floats32(dim, splitmix64(&state), scales[i%len(scales)])
		query := floats32(dim, splitmix64(&state), 1)

		codec, err := turboquant.NewCodec(dim, bw, int(seed), 0)
		if err != nil {
			return nil, fmt.Errorf("property %d: %w", i, err)
		}
		code, err := codec.Encode(vector)
		if err != nil {
			return nil, fmt.Errorf("property %d encode: %w", i, err)
		}
		decoded, err := codec.Decode(code)
		if err != nil {
			return nil, fmt.Errorf("property %d decode: %w", i, err)
		}

		// Score tolerance uses the same rotation CoreKit applies to the query.
		rotation := turboquant.NewRandomRotation(dim, int(seed))
		floatsQuery := make([]float64, dim)
		for j, v := range query {
			floatsQuery[j] = float64(v)
		}
		rotated := make([]float64, nextPow2(dim))
		rotation.ApplyRotation(floatsQuery, rotated)
		sumAbs := 0.0
		for _, r := range rotated {
			sumAbs += math.Abs(r)
		}
		scale := math.Max(math.Abs(code.Min), math.Abs(code.Max))

		cases = append(cases, propertyCase{
			ID:  fmt.Sprintf("prop_%03d_dim%d_bw%d", i, dim, bw),
			Dim: dim, BitWidth: int(bw), Seed: seed,
			VectorF32Hex: hex32(vector), QueryF32Hex: hex32(query),
			RotDim: codec.RotDim, PackedHex: hex.EncodeToString(code.Bytes),
			MinF64Bits: math.Float64bits(code.Min), MaxF64Bits: math.Float64bits(code.Max),
			DecodedF32Hex:   hex32(decoded),
			DecodeTolerance: math.Ldexp(scale, decodeToleranceExp),
			ScoreF32Bits:    math.Float32bits(codec.Score(query, code)),
			ScoreTolerance:  math.Ldexp(sumAbs*scale, scoreToleranceExp),
		})
	}
	return cases, nil
}

func generateProperty() ([]byte, error) {
	cases, err := propertyCases()
	if err != nil {
		return nil, err
	}
	fixture := propertyFixture{
		SchemaVersion: 1,
		Oracle: oracleSource{
			Commit:     fixtureoracle.Current().Commit,
			Release:    fixtureoracle.Current().Release,
			GoVersion:  "go1.26.9",
			Corekit:    "github.com/danieljustus/symaira-corekit/v0.18.3-0.20261012110002-27d2daf8ec1e",
			CorekitSum: "h1:lEwB4izhkSVb8fREvaPOiXuVHx7CF6fymsYGC8O1bsc=",
			Scope: "Property corpus: 200 generated cases (dims 1..128 plus 768, all bit widths, full-range seeds, scales 1e-3..1e6). " +
				"Encoded bytes and min/max are exact. Decoded components and scores are compared within the recorded " +
				"tolerances, which bound the arm64 FMA-fusion difference against amd64 and any Rust rounding difference.",
		},
		Cases: cases,
	}
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

// checkProperty compares a generated corpus with the committed one. Everything
// except decoded and score values must be identical; those must stay within
// the committed tolerances.
func checkProperty(committed, generated []byte) error {
	var want, got propertyFixture
	if err := json.Unmarshal(committed, &want); err != nil {
		return fmt.Errorf("committed property fixture: %w", err)
	}
	if err := json.Unmarshal(generated, &got); err != nil {
		return fmt.Errorf("generated property fixture: %w", err)
	}
	if !reflect.DeepEqual(want.Oracle, got.Oracle) || len(want.Cases) != len(got.Cases) {
		return fmt.Errorf("property oracle identity or case count differs")
	}
	for i := range want.Cases {
		w, g := want.Cases[i], got.Cases[i]
		if w.DecodeTolerance != g.DecodeTolerance || w.ScoreTolerance != g.ScoreTolerance {
			return fmt.Errorf("%s: tolerance differs", g.ID)
		}
		wDec, err := decodeF32s(w.DecodedF32Hex)
		if err != nil {
			return fmt.Errorf("%s committed decode: %w", w.ID, err)
		}
		gDec, err := decodeF32s(g.DecodedF32Hex)
		if err != nil {
			return fmt.Errorf("%s generated decode: %w", g.ID, err)
		}
		if len(wDec) != len(gDec) {
			return fmt.Errorf("%s: decoded length differs", g.ID)
		}
		for j := range wDec {
			if math.Abs(float64(wDec[j])-float64(gDec[j])) > g.DecodeTolerance {
				return fmt.Errorf("%s: decoded[%d] differs beyond tolerance %g", g.ID, j, g.DecodeTolerance)
			}
		}
		if math.Abs(float64(math.Float32frombits(w.ScoreF32Bits))-float64(math.Float32frombits(g.ScoreF32Bits))) > g.ScoreTolerance {
			return fmt.Errorf("%s: score differs beyond tolerance %g", g.ID, g.ScoreTolerance)
		}
		w.DecodedF32Hex, g.DecodedF32Hex = "", ""
		w.ScoreF32Bits, g.ScoreF32Bits = 0, 0
		if !reflect.DeepEqual(w, g) {
			return fmt.Errorf("%s: exact property fields differ", g.ID)
		}
	}
	return nil
}

func decodeF32s(hexText string) ([]float32, error) {
	raw, err := hex.DecodeString(hexText)
	if err != nil {
		return nil, err
	}
	if len(raw)%4 != 0 {
		return nil, fmt.Errorf("f32 hex length %d", len(raw))
	}
	out := make([]float32, len(raw)/4)
	for i := range out {
		out[i] = math.Float32frombits(uint32(raw[4*i]) | uint32(raw[4*i+1])<<8 | uint32(raw[4*i+2])<<16 | uint32(raw[4*i+3])<<24)
	}
	return out, nil
}
