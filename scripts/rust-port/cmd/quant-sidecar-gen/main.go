package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"runtime"
	"runtime/debug"

	"github.com/danieljustus/symaira-corekit/vectorkit/turboquant"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

const (
	corekitPath    = "github.com/danieljustus/symaira-corekit"
	corekitVersion = "v0.17.0"
	corekitSum     = "h1:pDtkMy0Pel1PWglNupMiLYpxo0WxUQ3CkjlR2LaUAZ4="
)

type fixture struct {
	SchemaVersion int          `json:"schema_version"`
	Oracle        oracleSource `json:"oracle"`
	Cases         []wireCase   `json:"cases"`
}

type oracleSource struct {
	Commit     string `json:"commit"`
	Release    string `json:"release"`
	GoVersion  string `json:"go_version"`
	Corekit    string `json:"corekit_module"`
	CorekitSum string `json:"corekit_sum"`
	Scope      string `json:"scope"`
}

type codecInput struct {
	Dimension int      `json:"dimension"`
	BitWidth  int      `json:"bit_width"`
	Seed      int      `json:"seed"`
	BlockSize int      `json:"block_size"`
	Vector    []uint32 `json:"vector_f32_bits"`
}

type wireCase struct {
	ID         string      `json:"id"`
	Operation  string      `json:"operation"`
	BlobHex    string      `json:"blob_hex"`
	BlobLen    int         `json:"blob_len"`
	MinF32Bits *uint32     `json:"min_f32_bits,omitempty"`
	MaxF32Bits *uint32     `json:"max_f32_bits,omitempty"`
	MinF64Bits *uint64     `json:"min_f64_bits,omitempty"`
	MaxF64Bits *uint64     `json:"max_f64_bits,omitempty"`
	PackedHex  string      `json:"packed_hex,omitempty"`
	PackedLen  int         `json:"packed_len,omitempty"`
	Codec      *codecInput `json:"codec,omitempty"`
	Error      string      `json:"error,omitempty"`
}

func main() {
	checkPath := flag.String("check", "", "compare generated fixture bytes with this file; never rewrite it")
	flag.Parse()
	if flag.NArg() != 0 {
		fatalf("unexpected positional arguments")
	}
	if runtime.Version() != "go1.26.6" {
		fatalf("oracle requires Go 1.26.6, got %s", runtime.Version())
	}

	generated, err := generate()
	if err != nil {
		fatalf("generate fixture: %v", err)
	}
	if *checkPath != "" {
		committed, err := os.ReadFile(*checkPath)
		if err != nil {
			fatalf("read fixture: %v", err)
		}
		if !bytes.Equal(generated, committed) {
			fatalf("fixture differs from pinned Go output")
		}
		fmt.Fprintf(os.Stderr, "Go oracle fixture check passed: %s (%d bytes)\n", *checkPath, len(generated))
		return
	}
	if _, err := os.Stdout.Write(generated); err != nil {
		fatalf("write fixture: %v", err)
	}
}

func generate() ([]byte, error) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return nil, fmt.Errorf("missing Go build identity")
	}
	var corekit *debug.Module
	for _, dependency := range info.Deps {
		if dependency.Path == corekitPath {
			corekit = dependency
			break
		}
	}
	if corekit == nil || corekit.Version != corekitVersion || corekit.Sum != corekitSum || corekit.Replace != nil {
		return nil, fmt.Errorf("CoreKit runtime module identity differs from pinned v0.17.0 source")
	}
	cases := make([]wireCase, 0, 16)
	inputs := []codecInput{
		{
			Dimension: 5, BitWidth: int(turboquant.BitWidth2), Seed: 17, BlockSize: 0,
			Vector: []uint32{0x00000000, 0x80000000, 0x3f800000, 0xbf800000, 0x3e000000},
		},
		{
			Dimension: 5, BitWidth: int(turboquant.BitWidth3), Seed: 81, BlockSize: 0,
			Vector: []uint32{0x3eaaaaab, 0xbf400000, 0x41200000, 0xc1200000, 0x3f000000},
		},
		{
			Dimension: 4, BitWidth: int(turboquant.BitWidth4), Seed: 99, BlockSize: 2,
			Vector: []uint32{0x3f000000, 0x40000000, 0xc0000000, 0x3e800000},
		},
		{
			Dimension: 3, BitWidth: int(turboquant.BitWidthHalf2and3), Seed: -5, BlockSize: 0,
			Vector: []uint32{0x3f800000, 0xbf000000, 0x3e000000},
		},
	}
	for i, input := range inputs {
		codec, err := turboquant.NewCodec(input.Dimension, turboquant.BitWidth(input.BitWidth), input.Seed, input.BlockSize)
		if err != nil {
			return nil, fmt.Errorf("case %d create codec: %w", i, err)
		}
		vector := make([]float32, len(input.Vector))
		for j, bits := range input.Vector {
			vector[j] = math.Float32frombits(bits)
		}
		blob, meta, err := codec.EncodeSidecar(vector, 0)
		if err != nil {
			return nil, fmt.Errorf("case %d encode sidecar: %w", i, err)
		}
		if meta == nil {
			return nil, fmt.Errorf("case %d returned nil sidecar metadata", i)
		}
		captured, err := captureUnpack(fmt.Sprintf("encode_bw%d_dim%d", input.BitWidth, input.Dimension), blob)
		if err != nil {
			return nil, err
		}
		captured.Operation = "encode_sidecar_then_unpack"
		captured.Codec = &input
		cases = append(cases, captured)
	}

	for n := 0; n < 8; n++ {
		blob := make([]byte, n)
		captured, err := captureUnpack(fmt.Sprintf("unpack_short_%d", n), blob)
		if err != nil {
			return nil, err
		}
		cases = append(cases, captured)
	}

	cases = append(cases,
		captureHeader("unpack_empty_payload", 0x00000000, 0x00000000, nil),
		captureHeader("unpack_opaque_payload", 0xc0200000, 0x3f800000, []byte{0x00, 0x80, 0xff, 0x7e, 0x01, 0x02}),
		captureHeader("unpack_min_negative_zero", 0x80000000, 0x00000000, []byte{0xa5}),
		captureHeader("unpack_max_negative_zero", 0x00000000, 0x80000000, []byte{0x5a, 0x00}),
	)

	result := fixture{
		SchemaVersion: 1,
		Oracle: oracleSource{
			Commit:     fixtureoracle.Current().Commit,
			Release:    fixtureoracle.Current().Release,
			GoVersion:  runtime.Version(),
			Corekit:    corekit.Path + "/" + corekit.Version,
			CorekitSum: corekit.Sum,
			Scope:      "Only persisted wire framing and opaque payload are asserted; generation invokes production Go EncodeSidecar. Intentional Rust API difference: read_blob owns a snapshot; Go UnpackSidecarBlob aliases mutable caller bytes. In-memory aliasing, NaN/infinity interpretation, decoded-vector accuracy, rotation/RNG parity, scoring, and performance are outside this bounded port.",
		},
		Cases: cases,
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func captureHeader(id string, minBits, maxBits uint32, packed []byte) wireCase {
	blob := make([]byte, 8+len(packed))
	binary.LittleEndian.PutUint32(blob[:4], minBits)
	binary.LittleEndian.PutUint32(blob[4:8], maxBits)
	copy(blob[8:], packed)
	captured, err := captureUnpack(id, blob)
	if err != nil {
		fatalf("capture %s: %v", id, err)
	}
	return captured
}

func captureUnpack(id string, blob []byte) (wireCase, error) {
	captured := wireCase{
		ID:        id,
		Operation: "unpack_sidecar_blob",
		BlobHex:   hex.EncodeToString(blob),
		BlobLen:   len(blob),
	}
	packed, err := turboquant.UnpackSidecarBlob(blob)
	if err != nil {
		captured.Error = err.Error()
		return captured, nil
	}
	minF32 := math.Float32bits(float32(packed.Min))
	maxF32 := math.Float32bits(float32(packed.Max))
	minF64 := math.Float64bits(packed.Min)
	maxF64 := math.Float64bits(packed.Max)
	captured.MinF32Bits = &minF32
	captured.MaxF32Bits = &maxF32
	captured.MinF64Bits = &minF64
	captured.MaxF64Bits = &maxF64
	captured.PackedHex = hex.EncodeToString(packed.Bytes)
	captured.PackedLen = len(packed.Bytes)
	return captured, nil
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
