// Command turboquant-gen records pinned CoreKit vectorkit/turboquant codec
// observations for the local Rust port (#1137).
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
	corekitVersion = "v0.18.3-0.20261012110002-27d2daf8ec1e"
	corekitSum     = "h1:lEwB4izhkSVb8fREvaPOiXuVHx7CF6fymsYGC8O1bsc="
)

type fixture struct {
	SchemaVersion int          `json:"schema_version"`
	Oracle        oracleSource `json:"oracle"`
	Signs         []signCase   `json:"signs"`
	Hadamard      []vectorCase `json:"hadamard"`
	Rotations     []rotateCase `json:"rotations"`
	Codecs        []codecCase  `json:"codecs"`
	MetaJSON      []metaCase   `json:"sidecar_meta_json"`
	Errors        []errorCase  `json:"errors"`
}

type oracleSource struct {
	Commit     string `json:"commit"`
	Release    string `json:"release"`
	GoVersion  string `json:"go_version"`
	Corekit    string `json:"corekit_module"`
	CorekitSum string `json:"corekit_sum"`
	Scope      string `json:"scope"`
}

// signCase records the rotation sign flips: InverseApplyRotation(e0) equals
// signs/sqrt(padded), so each sign is exactly recoverable.
type signCase struct {
	Dim   int    `json:"dim"`
	Seed  int64  `json:"seed"`
	Signs string `json:"signs"`
}

type vectorCase struct {
	InputF64Hex  string `json:"input_f64_hex"`
	OutputF64Hex string `json:"output_f64_hex"`
}

type rotateCase struct {
	Dim           int    `json:"dim"`
	Seed          int64  `json:"seed"`
	InputF64Hex   string `json:"input_f64_hex"`
	ForwardF64Hex string `json:"forward_f64_hex"`
	InverseF64Hex string `json:"inverse_f64_hex"`
}

type codecCase struct {
	ID                 string  `json:"id"`
	Dim                int     `json:"dim"`
	BitWidth           int     `json:"bit_width"`
	Seed               int64   `json:"seed"`
	BlockSize          int     `json:"block_size"`
	VectorF32Hex       string  `json:"vector_f32_hex"`
	QueryF32Hex        string  `json:"query_f32_hex"`
	Norm               uint32  `json:"norm_f32_bits"`
	RotDim             int     `json:"rot_dim"`
	PackedHex          string  `json:"packed_hex"`
	MinF64Bits         uint64  `json:"min_f64_bits"`
	MaxF64Bits         uint64  `json:"max_f64_bits"`
	DecodedGrid        []int64 `json:"decoded_grid"`
	ScoreF32Bits       uint32  `json:"score_f32_bits"`
	CosineF32Bits      uint32  `json:"score_cosine_f32_bits"`
	MetadataHex        string  `json:"metadata_hex"`
	MetadataPackedHex  string  `json:"metadata_roundtrip_packed_hex"`
	SidecarBlobHex     string  `json:"sidecar_blob_hex"`
	SidecarMetaJSON    string  `json:"sidecar_meta_json"`
	SidecarDecodedGrid []int64 `json:"sidecar_decoded_grid"`
	SidecarScore       uint32  `json:"sidecar_score_f32_bits"`
	SidecarCosine      uint32  `json:"sidecar_score_cosine_f32_bits"`
}

type metaCase struct {
	NormF32Bits    uint32 `json:"norm_f32_bits"`
	ProjectionSeed int64  `json:"projection_seed"`
	QuantizerMode  string `json:"quantizer_mode"`
	JSON           string `json:"json"`
}

type errorCase struct {
	ID        string `json:"id"`
	Operation string `json:"operation"`
	Dim       int    `json:"dim,omitempty"`
	BitWidth  int    `json:"bit_width,omitempty"`
	Seed      int64  `json:"seed,omitempty"`
	InputHex  string `json:"input_hex,omitempty"`
	MetaJSON  string `json:"meta_json,omitempty"`
	Error     string `json:"error"`
}

func main() {
	output := flag.String("output", "testdata/port/retrieval/turboquant-codec.json", "fixture path")
	propertyOutput := flag.String("property-output", "testdata/port/retrieval/turboquant-property.json", "property corpus path")
	check := flag.Bool("check", false, "compare generated fixtures with committed ones; never rewrite them")
	flag.Parse()
	if flag.NArg() != 0 {
		fatalf("unexpected positional arguments")
	}
	if runtime.Version() != "go1.26.9" {
		fatalf("oracle requires Go 1.26.9, got %s", runtime.Version())
	}
	generated, err := generate()
	if err != nil {
		fatalf("generate fixture: %v", err)
	}
	property, err := generateProperty()
	if err != nil {
		fatalf("generate property corpus: %v", err)
	}
	if *check {
		committed, err := os.ReadFile(*output)
		if err != nil {
			fatalf("read fixture: %v", err)
		}
		if !bytes.Equal(generated, committed) {
			fatalf("fixture differs from pinned Go output (%s/%s)", runtime.GOOS, runtime.GOARCH)
		}
		committedProperty, err := os.ReadFile(*propertyOutput)
		if err != nil {
			fatalf("read property corpus: %v", err)
		}
		if err := checkProperty(committedProperty, property); err != nil {
			fatalf("property corpus differs from pinned Go output (%s/%s): %v", runtime.GOOS, runtime.GOARCH, err)
		}
		fmt.Fprintf(os.Stderr, "Go oracle fixture check passed: %s and %s (%s/%s)\n", *output, *propertyOutput, runtime.GOOS, runtime.GOARCH)
		return
	}
	if err := os.WriteFile(*output, generated, 0o644); err != nil { //nolint:gosec // committed fixture
		fatalf("write fixture: %v", err)
	}
	if err := os.WriteFile(*propertyOutput, property, 0o644); err != nil { //nolint:gosec // committed fixture
		fatalf("write property corpus: %v", err)
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
		return nil, fmt.Errorf("CoreKit runtime module identity differs from pinned %s source", corekitVersion)
	}

	result := fixture{
		SchemaVersion: 1,
		Oracle: oracleSource{
			Commit:     fixtureoracle.Current().Commit,
			Release:    fixtureoracle.Current().Release,
			GoVersion:  runtime.Version(),
			Corekit:    corekit.Path + "/" + corekit.Version,
			CorekitSum: corekit.Sum,
			Scope: "Pinned CoreKit vectorkit/turboquant: math/rand rotation signs, Walsh-Hadamard, forward/inverse rotation, " +
				"Encode/Decode/Score/ScoreCosine, 32-byte metadata, sidecar blob/JSON and error strings. " +
				"Generator --check must pass on arm64 (Go fuses FMA) and amd64; encode bytes and scores are exact, decode is recorded on a scale/4096 grid because Go decode bits are architecture-dependent.",
		},
		Signs:     signCases(),
		Hadamard:  hadamardCases(),
		Rotations: rotateCases(),
	}
	codecs, err := codecCases()
	if err != nil {
		return nil, err
	}
	result.Codecs = codecs
	if result.MetaJSON, err = metaCases(); err != nil {
		return nil, err
	}
	if result.Errors, err = errorCases(); err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func signCases() []signCase {
	type input struct {
		dim  int
		seed int64
	}
	inputs := []input{
		{1, 0}, {2, 1}, {64, -1}, {64, 5}, {64, -5}, {64, 17}, {64, 42}, {64, 89482311},
		{64, math.MaxInt32}, {64, math.MinInt32}, {64, 4294967291}, {64, 1 << 40}, {64, -(1 << 40)},
		{64, math.MaxInt64}, {64, math.MinInt64}, {768, 42}, {1024, 7}, {2048, -123456789},
	}
	cases := make([]signCase, 0, len(inputs))
	for _, in := range inputs {
		rotation := turboquant.NewRandomRotation(in.dim, int(in.seed))
		padded := nextPow2(in.dim)
		basis := make([]float64, padded)
		basis[0] = 1
		out := make([]float64, padded)
		rotation.InverseApplyRotation(basis, out)
		signs := make([]byte, padded)
		for i, v := range out {
			if v > 0 {
				signs[i] = '+'
			} else {
				signs[i] = '-'
			}
		}
		cases = append(cases, signCase{Dim: in.dim, Seed: in.seed, Signs: string(signs)})
	}
	return cases
}

func hadamardCases() []vectorCase {
	inputs := [][]float64{
		{}, {2.5}, {1, -2}, {0.1, 0.2, 0.3}, {1, 2, 3, 4, 5, 6, 7, 8},
		{math.Copysign(0, -1), 1e-300, -1e300, math.SmallestNonzeroFloat64, 3, -3, 0.5, 0.25},
		floats64(64, 3),
	}
	cases := make([]vectorCase, 0, len(inputs))
	for _, in := range inputs {
		out := append([]float64(nil), in...)
		turboquant.HadamardTransform(out)
		cases = append(cases, vectorCase{InputF64Hex: hex64(in), OutputF64Hex: hex64(out)})
	}
	return cases
}

func rotateCases() []rotateCase {
	type input struct {
		dim  int
		seed int64
	}
	inputs := []input{{1, 3}, {5, 17}, {13, -9}, {64, 42}, {100, 1 << 33}}
	cases := make([]rotateCase, 0, len(inputs))
	for i, in := range inputs {
		rotation := turboquant.NewRandomRotation(in.dim, int(in.seed))
		padded := nextPow2(in.dim)
		vec := floats64(in.dim, uint64(i)+11)
		forward := make([]float64, padded)
		rotation.ApplyRotation(vec, forward)
		inverse := make([]float64, padded)
		rotation.InverseApplyRotation(forward, inverse)
		cases = append(cases, rotateCase{
			Dim: in.dim, Seed: in.seed, InputF64Hex: hex64(vec),
			ForwardF64Hex: hex64(forward), InverseF64Hex: hex64(inverse),
		})
	}
	return cases
}

func codecCases() ([]codecCase, error) {
	widths := []turboquant.BitWidth{
		turboquant.BitWidth2, turboquant.BitWidth3, turboquant.BitWidth4,
		turboquant.BitWidthHalf2and3, turboquant.BitWidthHalf3and4,
	}
	type input struct {
		id        string
		dim       int
		seed      int64
		blockSize int
		vector    []float32
	}
	inputs := []input{
		{"dim1", 1, 3, 0, []float32{0.75}},
		{"dim2", 2, -5, 0, []float32{1, -1}},
		{"dim3", 3, 0, 0, floats32(3, 1, 1)},
		{"dim5", 5, 17, 0, floats32(5, 2, 1)},
		{"dim8_block", 8, 99, 2, floats32(8, 3, 4)},
		{"dim13", 13, 4294967291, 0, floats32(13, 4, 0.01)},
		{"dim64", 64, 42, 0, floats32(64, 5, 1)},
		{"dim100", 100, 1 << 40, 0, floats32(100, 6, 1000)},
		{"dim768", 768, 42, 0, floats32(768, 7, 0.05)},
		{"zeros", 6, 8, 0, make([]float32, 6)},
		{"constant", 4, 1, 0, []float32{0.5, 0.5, 0.5, 0.5}},
		{"extreme", 4, 2, 0, []float32{math.MaxFloat32, -math.MaxFloat32, math.SmallestNonzeroFloat32, -0}},
	}
	cases := make([]codecCase, 0, len(inputs)*len(widths))
	for i, in := range inputs {
		query := floats32(in.dim, uint64(i)+100, 1)
		for _, bw := range widths {
			id := fmt.Sprintf("%s_bw%d", in.id, bw)
			codec, err := turboquant.NewCodec(in.dim, bw, int(in.seed), in.blockSize)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", id, err)
			}
			code, err := codec.Encode(in.vector)
			if err != nil {
				return nil, fmt.Errorf("%s encode: %w", id, err)
			}
			decoded, err := codec.Decode(code)
			if err != nil {
				return nil, fmt.Errorf("%s decode: %w", id, err)
			}
			metaBytes := codec.MarshalMetadata()
			meta, err := turboquant.UnmarshalMetadata(metaBytes)
			if err != nil {
				return nil, fmt.Errorf("%s metadata: %w", id, err)
			}
			fromMeta, err := turboquant.CodecFromMetadata(meta)
			if err != nil {
				return nil, fmt.Errorf("%s codec from metadata: %w", id, err)
			}
			metaCode, err := fromMeta.Encode(in.vector)
			if err != nil {
				return nil, fmt.Errorf("%s metadata encode: %w", id, err)
			}
			// Explicit float64 conversions forbid FMA fusion, keeping the norm
			// identical on every architecture.
			sum := 0.0
			for _, v := range in.vector {
				sum += float64(float64(v) * float64(v))
			}
			norm := float32(math.Sqrt(sum))
			if math.IsInf(float64(norm), 0) {
				norm = 0
			}
			blob, sideMeta, err := codec.EncodeSidecar(in.vector, norm)
			if err != nil {
				return nil, fmt.Errorf("%s sidecar: %w", id, err)
			}
			sideJSON, err := json.Marshal(sideMeta)
			if err != nil {
				return nil, fmt.Errorf("%s sidecar json: %w", id, err)
			}
			sideDecoded, err := turboquant.DecodeSidecar(blob, sideMeta)
			if err != nil {
				return nil, fmt.Errorf("%s decode sidecar: %w", id, err)
			}
			unpacked, err := turboquant.UnpackSidecarBlob(blob)
			if err != nil {
				return nil, fmt.Errorf("%s unpack sidecar: %w", id, err)
			}
			cases = append(cases, codecCase{
				ID: id, Dim: in.dim, BitWidth: int(bw), Seed: in.seed, BlockSize: in.blockSize,
				VectorF32Hex: hex32(in.vector), QueryF32Hex: hex32(query), Norm: math.Float32bits(norm),
				RotDim: codec.RotDim, PackedHex: hex.EncodeToString(code.Bytes),
				MinF64Bits: math.Float64bits(code.Min), MaxF64Bits: math.Float64bits(code.Max),
				DecodedGrid:        decodeGrid(decoded, code),
				ScoreF32Bits:       math.Float32bits(codec.Score(query, code)),
				CosineF32Bits:      math.Float32bits(codec.ScoreCosine(query, code)),
				MetadataHex:        hex.EncodeToString(metaBytes),
				MetadataPackedHex:  hex.EncodeToString(metaCode.Bytes),
				SidecarBlobHex:     hex.EncodeToString(blob),
				SidecarMetaJSON:    string(sideJSON),
				SidecarDecodedGrid: decodeGrid(sideDecoded, code),
				SidecarScore:       math.Float32bits(codec.Score(query, unpacked)),
				SidecarCosine:      math.Float32bits(codec.ScoreCosine(query, unpacked)),
			})
		}
	}
	return cases, nil
}

func metaCases() ([]metaCase, error) {
	norms := []float32{
		0, float32(math.Copysign(0, -1)), 1, 1.5, 0.1, 1e-7, 1.2345e-6, 9.99e-7, 1e-6, 3e21, 1e21, 9.9e20,
		768, 27.712812, math.SmallestNonzeroFloat32, math.MaxFloat32, 123456789, -2.5,
	}
	cases := make([]metaCase, 0, len(norms)+3)
	add := func(norm float32, seed int64, mode string) error {
		meta := turboquant.SidecarMeta{
			CodecVersion: turboquant.CodecVersion, Dimension: 768, BitWidth: 4,
			QuantizerMode: mode, ProjectionSeed: seed, Norm: norm,
		}
		encoded, err := json.Marshal(meta)
		if err != nil {
			return err
		}
		cases = append(cases, metaCase{NormF32Bits: math.Float32bits(norm), ProjectionSeed: seed, QuantizerMode: mode, JSON: string(encoded)})
		return nil
	}
	for _, norm := range norms {
		if err := add(norm, 42, "scalar"); err != nil {
			return nil, err
		}
	}
	for _, extra := range []struct {
		seed int64
		mode string
	}{{0, "scalar"}, {math.MinInt64, "a<b>&c"}, {-7, "ünïcode  "}} {
		if err := add(1, extra.seed, extra.mode); err != nil {
			return nil, err
		}
	}
	return cases, nil
}

func errorCases() ([]errorCase, error) {
	cases := []errorCase{}
	for _, in := range []struct {
		id      string
		dim, bw int
	}{{"new_dim0", 0, 4}, {"new_dim_negative", -3, 4}, {"new_bw0", 5, 0}, {"new_bw5", 5, 5}, {"new_bw8", 5, 8}} {
		_, err := turboquant.NewCodec(in.dim, turboquant.BitWidth(in.bw), 1, 0)
		cases = append(cases, errorCase{ID: in.id, Operation: "new_codec", Dim: in.dim, BitWidth: in.bw, Seed: 1, Error: errString(err)})
	}
	codec, err := turboquant.NewCodec(5, turboquant.BitWidth3, 9, 0)
	if err != nil {
		return nil, err
	}
	_, err = codec.Encode(make([]float32, 4))
	cases = append(cases, errorCase{ID: "encode_dim_mismatch", Operation: "encode", Dim: 5, BitWidth: 3, Seed: 9, InputHex: hex32(make([]float32, 4)), Error: errString(err)})
	_, err = codec.Decode(&turboquant.PackedCode{Bytes: []byte{1, 2}})
	cases = append(cases, errorCase{ID: "decode_short", Operation: "decode", Dim: 5, BitWidth: 3, Seed: 9, InputHex: "0102", Error: errString(err)})

	short := make([]byte, 23)
	_, err = turboquant.UnmarshalMetadata(short)
	cases = append(cases, errorCase{ID: "metadata_short", Operation: "unmarshal_metadata", InputHex: hex.EncodeToString(short), Error: errString(err)})
	v2 := make([]byte, 24)
	binary.LittleEndian.PutUint32(v2, 2)
	_, err = turboquant.UnmarshalMetadata(v2)
	cases = append(cases, errorCase{ID: "metadata_version", Operation: "unmarshal_metadata", InputHex: hex.EncodeToString(v2), Error: errString(err)})
	badDim := make([]byte, 32)
	binary.LittleEndian.PutUint32(badDim, 1)
	binary.LittleEndian.PutUint32(badDim[8:], 4)
	meta, err := turboquant.UnmarshalMetadata(badDim)
	if err != nil {
		return nil, err
	}
	_, err = turboquant.CodecFromMetadata(meta)
	cases = append(cases, errorCase{ID: "metadata_codec_dim0", Operation: "codec_from_metadata", InputHex: hex.EncodeToString(badDim), Error: errString(err)})

	for _, in := range []struct {
		id   string
		meta turboquant.SidecarMeta
	}{
		{"sidecar_version0", turboquant.SidecarMeta{Dimension: 5, BitWidth: 3}},
		{"sidecar_dim0", turboquant.SidecarMeta{CodecVersion: 1, BitWidth: 3}},
		{"sidecar_bw0", turboquant.SidecarMeta{CodecVersion: 1, Dimension: 5}},
		{"sidecar_bw7", turboquant.SidecarMeta{CodecVersion: 1, Dimension: 5, BitWidth: 7}},
	} {
		raw, _ := json.Marshal(in.meta)
		_, err := turboquant.CodecFromSidecarMeta(&in.meta)
		cases = append(cases, errorCase{ID: in.id, Operation: "codec_from_sidecar_meta", MetaJSON: string(raw), Error: errString(err)})
	}
	for _, in := range []struct {
		id   string
		meta turboquant.SidecarMeta
	}{
		{"validate_ok", turboquant.SidecarMeta{CodecVersion: 1, Dimension: 5, BitWidth: 3, ProjectionSeed: 9}},
		{"validate_version", turboquant.SidecarMeta{CodecVersion: 2, Dimension: 5, BitWidth: 3, ProjectionSeed: 9}},
		{"validate_dim", turboquant.SidecarMeta{CodecVersion: 1, Dimension: 6, BitWidth: 3, ProjectionSeed: 9}},
		{"validate_bw", turboquant.SidecarMeta{CodecVersion: 1, Dimension: 5, BitWidth: 25, ProjectionSeed: 9}},
		{"validate_seed", turboquant.SidecarMeta{CodecVersion: 1, Dimension: 5, BitWidth: 3, ProjectionSeed: -9}},
	} {
		raw, _ := json.Marshal(in.meta)
		err := turboquant.ValidateSidecarMeta(&in.meta, codec)
		cases = append(cases, errorCase{ID: in.id, Operation: "validate_sidecar_meta", Dim: 5, BitWidth: 3, Seed: 9, MetaJSON: string(raw), Error: errString(err)})
	}
	okMeta := turboquant.SidecarMeta{CodecVersion: 1, Dimension: 5, BitWidth: 3, ProjectionSeed: 9}
	rawOK, _ := json.Marshal(okMeta)
	for _, blob := range [][]byte{{1, 2, 3}, make([]byte, 9)} {
		_, err := turboquant.DecodeSidecar(blob, &okMeta)
		cases = append(cases, errorCase{
			ID: fmt.Sprintf("decode_sidecar_len%d", len(blob)), Operation: "decode_sidecar",
			InputHex: hex.EncodeToString(blob), MetaJSON: string(rawOK), Error: errString(err),
		})
	}
	return cases, nil
}

// decodeGrid snaps decoded values to scale/4096 steps, scale = max(|min|,|max|).
// Go fuses decode's min+(max-min)*mid into FMA on arm64 but not on amd64, so
// exact decoded bits differ by architecture (observed in 5 of 60 cases); the
// grid is identical on both and Rust must reproduce it exactly.
func decodeGrid(values []float32, code *turboquant.PackedCode) []int64 {
	scale := math.Max(math.Abs(code.Min), math.Abs(code.Max))
	if scale == 0 {
		scale = 1
	}
	out := make([]int64, len(values))
	for i, v := range values {
		out[i] = int64(math.Round(float64(v) / scale * 4096))
	}
	return out
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func nextPow2(n int) int {
	p := 1
	for p < n {
		p <<= 1
	}
	return p
}

// splitmix64 keeps the corpus deterministic without math/rand state.
func splitmix64(state *uint64) uint64 {
	*state += 0x9e3779b97f4a7c15
	z := *state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

func floats64(n int, seed uint64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = float64(splitmix64(&seed)>>11)/float64(1<<53)*2 - 1
	}
	return out
}

func floats32(n int, seed uint64, scale float32) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = (float32(splitmix64(&seed)>>40)/float32(1<<24)*2 - 1) * scale
	}
	return out
}

func hex64(values []float64) string {
	buf := make([]byte, 8*len(values))
	for i, v := range values {
		binary.LittleEndian.PutUint64(buf[8*i:], math.Float64bits(v))
	}
	return hex.EncodeToString(buf)
}

func hex32(values []float32) string {
	buf := make([]byte, 4*len(values))
	for i, v := range values {
		binary.LittleEndian.PutUint32(buf[4*i:], math.Float32bits(v))
	}
	return hex.EncodeToString(buf)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
