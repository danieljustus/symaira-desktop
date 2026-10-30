package archive

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/font"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

const archivePDFFixturePath = "testdata/port/render/archive-pdf.json"
const archivePDFCanonTool = "scripts/rust-port/pdfops/pdfcanon.py"

type archivePDFFixture struct {
	SchemaVersion int                  `json:"schema_version"`
	Oracle        archivePDFOracle     `json:"oracle"`
	Helvetica     archivePDFHelvetica  `json:"helvetica"`
	Import        []archivePDFImport   `json:"import"`
	Stamp         []archivePDFStampCas `json:"stamp"`
}

type archivePDFOracle struct {
	Commit       string `json:"commit"`
	GoVersion    string `json:"go_version"`
	PDFCPU       string `json:"pdfcpu"`
	CanonTool    string `json:"canon_tool"`
	CanonSHA256  string `json:"canon_tool_sha256"`
	Normalisaton string `json:"normalisation"`
}

// Helvetica widths for bytes 0..255 and the font box as pdfcpu reports them
// through its public font API (WinAnsi glyph map; unknown glyphs are 1000).
type archivePDFHelvetica struct {
	Widths []int      `json:"widths"`
	FBox   [4]float64 `json:"fbox"`
}

// Image is the base64 of the input file; Ext selects Generate's dispatch.
type archivePDFImport struct {
	ID    string         `json:"id"`
	Ext   string         `json:"ext"`
	Image string         `json:"image"`
	OCR   string         `json:"ocr_text"`
	Want  archivePDFWant `json:"want"`
	// GoPDF is the PDF Go wrote, so the Rust test can render both files and
	// compare pixels. Its bytes are not reproducible (dates, ids); the
	// validator checks that its canonical description equals Want.Canon.
	GoPDF string `json:"go_pdf_b64,omitempty"`
}

type archivePDFStampCas struct {
	ID       string         `json:"id"`
	InputPDF string         `json:"input_pdf"`
	OCR      string         `json:"ocr_text"`
	Want     archivePDFWant `json:"want"`
	GoPDF    string         `json:"go_pdf_b64,omitempty"`
}

// ErrorKind is the text before the first colon of Generate's error. Canon is
// the pdfcanon.py description of the PDF Generate wrote.
type archivePDFWant struct {
	OK        bool            `json:"ok"`
	ErrorKind string          `json:"error_kind"`
	Canon     json.RawMessage `json:"canon,omitempty"`
}

// TestPortArchivePDFFixture records what Generate really writes: the PDF that
// pdfcpu image import builds from a scan, and the watermark structure pdfcpu
// stamps on top of it, as writer-independent canonical descriptions
// (scripts/rust-port/pdfops/pdfcanon.py). Regenerate deliberately with:
//
//	PORT_RENDER_ARCHIVE_PDF_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/archive -run '^TestPortArchivePDFFixture$'
//
// Needs python3 and qpdf. Windows validates the stored oracle without
// regenerating it.
func TestPortArchivePDFFixture(t *testing.T) {
	generate := os.Getenv("PORT_RENDER_ARCHIVE_PDF_GENERATE") == "1" || os.Getenv("PORT_GENERATE") == "1"
	root := archiveContractRepoRoot(t)
	path := filepath.Join(root, filepath.FromSlash(archivePDFFixturePath))
	if !generate && runtime.GOOS == "windows" {
		current, err := os.ReadFile(path) //nolint:gosec // fixed repository-relative fixture path
		if err != nil {
			t.Fatalf("read fixture: %v", err)
		}
		var stored archivePDFFixture
		if err := json.Unmarshal(current, &stored); err != nil {
			t.Fatalf("decode stored oracle: %v", err)
		}
		canonical, err := encodeArchivePDFFixture(stored)
		if err != nil || !bytes.Equal(current, canonical) {
			t.Fatalf("stored oracle is not canonical: %v", err)
		}
		t.Logf("PASS %s on Windows: stored Go oracle validated, not regenerated (no qpdf)", archivePDFFixturePath)
		return
	}
	for _, tool := range []string{"python3", "qpdf"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("archive PDF oracle needs %s: %v", tool, err)
		}
	}
	if generate {
		fixture := buildArchivePDFFixture(t, root)
		encoded, err := encodeArchivePDFFixture(fixture)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateArchivePDFFixture(root, encoded); err != nil {
			t.Fatalf("generated Go oracle fixture failed its own validator: %v", err)
		}
		var mutated archivePDFFixture
		if err := json.Unmarshal(encoded, &mutated); err != nil {
			t.Fatal(err)
		}
		mutated.Stamp[0].Want.Canon = json.RawMessage(strings.Replace(string(mutated.Stamp[0].Want.Canon), "/Artifact", "/Artefact", 1))
		mutatedBytes, err := encodeArchivePDFFixture(mutated)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateArchivePDFFixture(root, mutatedBytes); err == nil || !(strings.Contains(err.Error(), "differs from Go") || strings.Contains(err.Error(), "recorded canonical description")) {
			t.Fatalf("canonical-structure mutation control = %v, want Go-output rejection", err)
		}
		if err := os.WriteFile(path, encoded, 0o644); err != nil { //nolint:gosec // test fixture file
			t.Fatalf("write fixture: %v", err)
		}
		t.Logf("GENERATED %s: %d import, %d stamp cases (Go %s)", archivePDFFixturePath, len(fixture.Import), len(fixture.Stamp), runtime.Version())
		return
	}
	current, err := os.ReadFile(path) //nolint:gosec // fixed repository-relative fixture path
	if err != nil {
		t.Fatalf("read fixture %s (regenerate deliberately with PORT_RENDER_ARCHIVE_PDF_GENERATE=1): %v", archivePDFFixturePath, err)
	}
	if err := validateArchivePDFFixture(root, current); err != nil {
		t.Fatalf("Go oracle fixture is stale; regenerate deliberately: %v", err)
	}
	t.Logf("PASS %s", archivePDFFixturePath)
}

func encodeArchivePDFFixture(f archivePDFFixture) ([]byte, error) {
	encoded, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func archivePDFCanon(root, pdfPath string) (json.RawMessage, error) {
	out, err := exec.Command("python3", "-I", filepath.Join(root, filepath.FromSlash(archivePDFCanonTool)), pdfPath).Output() //nolint:gosec // fixed tool, disposable paths
	if err != nil {
		return nil, fmt.Errorf("pdfcanon: %w", err)
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, out, "", "  "); err != nil {
		return nil, err
	}
	return json.RawMessage(buf.Bytes()), nil
}

func archivePDFObserve(root, inputName string, input []byte, ocr string) (archivePDFWant, error) {
	want, _, err := archivePDFObserveOutput(root, inputName, input, ocr)
	return want, err
}

func archivePDFObserveOutput(root, inputName string, input []byte, ocr string) (archivePDFWant, []byte, error) {
	dir, err := os.MkdirTemp("", "archive-pdf-")
	if err != nil {
		return archivePDFWant{}, nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	in := filepath.Join(dir, inputName)
	out := filepath.Join(dir, "output.pdf")
	if err := os.WriteFile(in, input, 0o600); err != nil {
		return archivePDFWant{}, nil, err
	}
	if err := Generate(in, out, ocr); err != nil {
		return archivePDFWant{ErrorKind: strings.SplitN(err.Error(), ":", 2)[0]}, nil, nil
	}
	canon, err := archivePDFCanon(root, out)
	if err != nil {
		return archivePDFWant{}, nil, err
	}
	written, err := os.ReadFile(out) //nolint:gosec // disposable temp path
	if err != nil {
		return archivePDFWant{}, nil, err
	}
	return archivePDFWant{OK: true, Canon: canon}, written, nil
}

func validateArchivePDFFixture(root string, encoded []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var actual archivePDFFixture
	if err := decoder.Decode(&actual); err != nil {
		return fmt.Errorf("decode fixture: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("fixture has trailing JSON data: %v", err)
	}
	tool, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(archivePDFCanonTool))) //nolint:gosec // fixed repository path
	if err != nil {
		return err
	}
	sum := sha256.Sum256(tool)
	if actual.Oracle.CanonSHA256 != hex.EncodeToString(sum[:]) {
		return fmt.Errorf("canon tool digest changed; regenerate")
	}
	helv := archivePDFHelveticaMetrics()
	if fmt.Sprint(helv) != fmt.Sprint(actual.Helvetica) {
		return fmt.Errorf("Helvetica metrics differ from Go")
	}
	same := func(id string, want archivePDFWant, got archivePDFWant) error {
		a, _ := json.Marshal(want)
		b, _ := json.Marshal(got)
		var ac, bc bytes.Buffer
		_ = json.Compact(&ac, a)
		_ = json.Compact(&bc, b)
		if !bytes.Equal(ac.Bytes(), bc.Bytes()) {
			return fmt.Errorf("case %q output differs from Go", id)
		}
		return nil
	}
	storedPDF := func(id string, want archivePDFWant, b64 string) error {
		if want.OK != (b64 != "") {
			return fmt.Errorf("case %q: stored Go PDF presence does not match its outcome", id)
		}
		if !want.OK {
			return nil
		}
		data, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return fmt.Errorf("case %q: %w", id, err)
		}
		dir, err := os.MkdirTemp("", "archive-pdf-stored-")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(dir) }()
		path := filepath.Join(dir, "go.pdf")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return err
		}
		canon, err := archivePDFCanon(root, path)
		if err != nil {
			return fmt.Errorf("case %q: %w", id, err)
		}
		var recorded, stored any
		if err := json.Unmarshal(want.Canon, &recorded); err != nil {
			return err
		}
		if err := json.Unmarshal(canon, &stored); err != nil {
			return err
		}
		if !reflect.DeepEqual(recorded, stored) {
			return fmt.Errorf("case %q: stored Go PDF does not match its recorded canonical description", id)
		}
		return nil
	}
	for _, c := range actual.Import {
		if err := storedPDF(c.ID, c.Want, c.GoPDF); err != nil {
			return err
		}
		data, err := base64.StdEncoding.DecodeString(c.Image)
		if err != nil {
			return fmt.Errorf("case %q: %w", c.ID, err)
		}
		got, err := archivePDFObserve(root, "scan"+c.Ext, data, c.OCR)
		if err != nil {
			return fmt.Errorf("case %q: %w", c.ID, err)
		}
		if err := same(c.ID, c.Want, got); err != nil {
			return err
		}
	}
	for _, c := range actual.Stamp {
		if err := storedPDF(c.ID, c.Want, c.GoPDF); err != nil {
			return err
		}
		data, err := base64.StdEncoding.DecodeString(c.InputPDF)
		if err != nil {
			return fmt.Errorf("case %q: %w", c.ID, err)
		}
		got, err := archivePDFObserve(root, "input.pdf", data, c.OCR)
		if err != nil {
			return fmt.Errorf("case %q: %w", c.ID, err)
		}
		if err := same(c.ID, c.Want, got); err != nil {
			return err
		}
	}
	canonical, err := encodeArchivePDFFixture(actual)
	if err != nil {
		return err
	}
	if !bytes.Equal(encoded, canonical) {
		return fmt.Errorf("fixture bytes differ from deterministic Go JSON encoding")
	}
	return nil
}

func archivePDFHelveticaMetrics() archivePDFHelvetica {
	var h archivePDFHelvetica
	for c := 0; c < 256; c++ {
		w, err := font.CharWidth(context.Background(), "Helvetica", rune(c))
		if err != nil {
			w = -1
		}
		h.Widths = append(h.Widths, w)
	}
	bb, err := font.BoundingBox(context.Background(), "Helvetica")
	if err == nil {
		h.FBox = [4]float64{bb.LL.X, bb.LL.Y, bb.UR.X, bb.UR.Y}
	}
	return h
}

func buildArchivePDFFixture(t *testing.T, root string) archivePDFFixture {
	t.Helper()
	if runtime.Version() != "go1.26.10" {
		t.Fatalf("archive PDF oracle requires Go go1.26.10, running %s", runtime.Version())
	}
	tool, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(archivePDFCanonTool))) //nolint:gosec // fixed repository path
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(tool)
	f := archivePDFFixture{
		SchemaVersion: 1,
		Oracle: archivePDFOracle{
			Commit:      fixtureoracle.Current().Commit,
			GoVersion:   runtime.Version(),
			PDFCPU:      "v0.16.2",
			CanonTool:   archivePDFCanonTool,
			CanonSHA256: hex.EncodeToString(sum[:]),
			Normalisaton: "reachable tree from /Root; /Info,/ID,/Parent,/Length ignored; refs expanded; reals rounded to 9 decimals " +
				"(comparison allows 1.1e-5); decoded streams compared as text tokens or SHA-256; see pdfcanon.py",
		},
		Helvetica: archivePDFHelveticaMetrics(),
	}
	for _, c := range archivePDFImportCorpus(t) {
		want, out, err := archivePDFObserveOutput(root, "scan"+c.Ext, c.data, c.ocr)
		if err != nil {
			t.Fatalf("case %q: %v", c.id, err)
		}
		f.Import = append(f.Import, archivePDFImport{ID: c.id, Ext: c.Ext, Image: base64.StdEncoding.EncodeToString(c.data), OCR: c.ocr, Want: want, GoPDF: base64.StdEncoding.EncodeToString(out)})
	}
	for _, c := range archivePDFStampCorpus(t) {
		want, out, err := archivePDFObserveOutput(root, "input.pdf", c.pdf, c.ocr)
		if err != nil {
			t.Fatalf("case %q: %v", c.id, err)
		}
		f.Stamp = append(f.Stamp, archivePDFStampCas{ID: c.id, InputPDF: base64.StdEncoding.EncodeToString(c.pdf), OCR: c.ocr, Want: want, GoPDF: base64.StdEncoding.EncodeToString(out)})
	}
	return f
}

type archivePDFImportInput struct {
	id   string
	Ext  string
	data []byte
	ocr  string
}

func mustEncode(t *testing.T, f func(w io.Writer) error) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := f(&b); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// ---- hand-built PNG so every colour type, depth, tRNS and interlace is exercised

// bmpV4 builds a 32 bit BMP with a 108 byte BITMAPV4HEADER (alpha honoured by Go).
func bmpV4(w, h int, pix func(x, y int) [4]byte) []byte {
	var b bytes.Buffer
	const info = 108
	b.WriteString("BM")
	_ = binary.Write(&b, binary.LittleEndian, uint32(14+info+w*h*4))
	b.Write([]byte{0, 0, 0, 0})
	_ = binary.Write(&b, binary.LittleEndian, uint32(14+info))
	_ = binary.Write(&b, binary.LittleEndian, uint32(info))
	_ = binary.Write(&b, binary.LittleEndian, int32(w))
	_ = binary.Write(&b, binary.LittleEndian, int32(h))
	_ = binary.Write(&b, binary.LittleEndian, uint16(1))
	_ = binary.Write(&b, binary.LittleEndian, uint16(32))
	_ = binary.Write(&b, binary.LittleEndian, uint32(0))
	b.Write(make([]byte, info-24))
	for y := h - 1; y >= 0; y-- {
		for x := 0; x < w; x++ {
			c := pix(x, y)
			b.Write([]byte{c[2], c[1], c[0], c[3]})
		}
	}
	return b.Bytes()
}

type pngSpec struct {
	w, h      int
	depth     int
	ctype     int
	interlace bool
	plte      []byte
	trns      []byte
	sample    func(x, y int) []int // samples per pixel
}

func pngChunk(typ string, data []byte) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, uint32(len(data)))
	b.WriteString(typ)
	b.Write(data)
	crc := crc32.NewIEEE()
	crc.Write([]byte(typ))
	crc.Write(data)
	_ = binary.Write(&b, binary.BigEndian, crc.Sum32())
	return b.Bytes()
}

func packSamples(samples []int, depth int) []byte {
	if depth >= 8 {
		var out []byte
		for _, s := range samples {
			if depth == 16 {
				out = append(out, byte(s>>8), byte(s))
			} else {
				out = append(out, byte(s))
			}
		}
		return out
	}
	var out []byte
	var cur byte
	n := 0
	for _, s := range samples {
		cur = cur<<uint(depth) | byte(s)&(1<<uint(depth)-1)
		n += depth
		if n == 8 {
			out = append(out, cur)
			cur, n = 0, 0
		}
	}
	if n > 0 {
		out = append(out, cur<<uint(8-n))
	}
	return out
}

func buildPNG(s pngSpec) []byte {
	spp := map[int]int{0: 1, 2: 3, 3: 1, 4: 2, 6: 4}[s.ctype]
	var raw bytes.Buffer
	rowOf := func(xs []int, y int) {
		var samples []int
		for _, x := range xs {
			samples = append(samples, s.sample(x, y)...)
		}
		raw.WriteByte(0)
		raw.Write(packSamples(samples, s.depth))
	}
	if !s.interlace {
		for y := 0; y < s.h; y++ {
			xs := make([]int, s.w)
			for x := range xs {
				xs[x] = x
			}
			rowOf(xs, y)
		}
	} else {
		type pass struct{ x0, y0, dx, dy int }
		for _, p := range []pass{{0, 0, 8, 8}, {4, 0, 8, 8}, {0, 4, 4, 8}, {2, 0, 4, 4}, {0, 2, 2, 4}, {1, 0, 2, 2}, {0, 1, 1, 2}} {
			var xs []int
			for x := p.x0; x < s.w; x += p.dx {
				xs = append(xs, x)
			}
			if len(xs) == 0 {
				continue
			}
			for y := p.y0; y < s.h; y += p.dy {
				rowOf(xs, y)
			}
		}
	}
	_ = spp
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	_, _ = zw.Write(raw.Bytes())
	_ = zw.Close()
	var ihdr bytes.Buffer
	_ = binary.Write(&ihdr, binary.BigEndian, uint32(s.w))
	_ = binary.Write(&ihdr, binary.BigEndian, uint32(s.h))
	il := byte(0)
	if s.interlace {
		il = 1
	}
	ihdr.Write([]byte{byte(s.depth), byte(s.ctype), 0, 0, il})
	var out bytes.Buffer
	out.WriteString("\x89PNG\r\n\x1a\n")
	out.Write(pngChunk("IHDR", ihdr.Bytes()))
	if s.plte != nil {
		out.Write(pngChunk("PLTE", s.plte))
	}
	if s.trns != nil {
		out.Write(pngChunk("tRNS", s.trns))
	}
	out.Write(pngChunk("IDAT", z.Bytes()))
	out.Write(pngChunk("IEND", nil))
	return out.Bytes()
}

// ---- baseline TIFF writer (multi page capable)

type tiffPage struct {
	w, h     int
	spp      int // 1 gray, 3 rgb, 4 rgba (unassociated)
	palette  bool
	cmyk     bool // 4 samples, PhotometricInterpretation CMYK
	packbits bool
	lzw      bool
	pix      func(x, y int) []byte
}

// lzwTIFF is the TIFF flavour of LZW: MSB-first codes, 9 to 12 bits, a clear
// code first, an end code last and the code width growing one entry early.
func lzwTIFF(data []byte) []byte {
	var out []byte
	var acc uint32
	var nbits uint
	width := uint(9)
	put := func(code int) {
		acc = acc<<width | uint32(code)
		nbits += width
		for nbits >= 8 {
			out = append(out, byte(acc>>(nbits-8)))
			nbits -= 8
		}
		acc &= 1<<nbits - 1
	}
	table := map[string]int{}
	next := 258
	reset := func() {
		table = map[string]int{}
		next = 258
		width = 9
	}
	put(256)
	cur := ""
	for _, c := range data {
		ext := cur + string([]byte{c})
		if len(ext) == 1 {
			cur = ext
			continue
		}
		if _, ok := table[ext]; ok {
			cur = ext
			continue
		}
		if len(cur) == 1 {
			put(int(cur[0]))
		} else {
			put(table[cur])
		}
		table[ext] = next
		next++
		switch next {
		case 512, 1024, 2048:
			width++
		case 4094:
			put(256)
			reset()
		}
		cur = string([]byte{c})
	}
	if cur != "" {
		if len(cur) == 1 {
			put(int(cur[0]))
		} else {
			put(table[cur])
		}
	}
	put(257)
	if nbits > 0 {
		out = append(out, byte(acc<<(8-nbits)))
	}
	return out
}

// packBits is the TIFF PackBits encoding with literal runs only.
func packBits(data []byte) []byte {
	var out []byte
	for len(data) > 0 {
		n := min(len(data), 128)
		out = append(out, byte(n-1))
		out = append(out, data[:n]...)
		data = data[n:]
	}
	return out
}

func buildTIFF(pages []tiffPage) []byte {
	var b bytes.Buffer
	b.Write([]byte{'I', 'I', 42, 0, 8, 0, 0, 0})
	// layout: header(8) then for each page: pixel strip, bps array (if spp>1), IFD
	type entry struct {
		tag, typ uint16
		count    uint32
		val      uint32
	}
	prevNext := -1
	for i, p := range pages {
		for len(b.Bytes())%2 != 0 {
			b.WriteByte(0)
		}
		stripOff := uint32(b.Len())
		var raw bytes.Buffer
		for y := 0; y < p.h; y++ {
			for x := 0; x < p.w; x++ {
				raw.Write(p.pix(x, y))
			}
		}
		strip, compression := raw.Bytes(), uint32(1)
		if p.packbits {
			strip, compression = packBits(strip), 32773
		}
		if p.lzw {
			strip, compression = lzwTIFF(strip), 5
		}
		b.Write(strip)
		stripLen := uint32(len(strip))
		for len(b.Bytes())%2 != 0 {
			b.WriteByte(0)
		}
		bpsOff := uint32(b.Len())
		for k := 0; k < p.spp; k++ {
			_ = binary.Write(&b, binary.LittleEndian, uint16(8))
		}
		var cmapOff uint32
		if p.palette {
			cmapOff = uint32(b.Len())
			for ch := 0; ch < 3; ch++ {
				for k := 0; k < 256; k++ {
					v := uint16((k*(ch+1)*37)&0xFF) * 257
					_ = binary.Write(&b, binary.LittleEndian, v)
				}
			}
		}
		photometric := uint32(1)
		switch {
		case p.palette:
			photometric = 3
		case p.cmyk:
			photometric = 5
		case p.spp >= 3:
			photometric = 2
		}
		bpsVal := uint32(8)
		if p.spp > 2 {
			bpsVal = bpsOff
		}
		es := []entry{
			{256, 4, 1, uint32(p.w)}, {257, 4, 1, uint32(p.h)}, {258, 3, uint32(p.spp), bpsVal}, {259, 3, 1, compression},
			{262, 3, 1, photometric}, {273, 4, 1, stripOff}, {277, 3, 1, uint32(p.spp)}, {278, 4, 1, uint32(p.h)},
			{279, 4, 1, stripLen},
		}
		if p.spp == 4 && !p.cmyk {
			es = append(es, entry{338, 3, 1, 2})
		}
		if p.palette {
			es = append(es, entry{320, 3, 768, cmapOff})
		}
		// sort by tag
		for a := 0; a < len(es); a++ {
			for c := a + 1; c < len(es); c++ {
				if es[c].tag < es[a].tag {
					es[a], es[c] = es[c], es[a]
				}
			}
		}
		ifdOff := uint32(b.Len())
		if i == 0 {
			binary.LittleEndian.PutUint32(b.Bytes()[4:], ifdOff)
		} else {
			binary.LittleEndian.PutUint32(b.Bytes()[prevNext:], ifdOff)
		}
		_ = binary.Write(&b, binary.LittleEndian, uint16(len(es)))
		for _, e := range es {
			_ = binary.Write(&b, binary.LittleEndian, e.tag)
			_ = binary.Write(&b, binary.LittleEndian, e.typ)
			_ = binary.Write(&b, binary.LittleEndian, e.count)
			if e.typ == 3 && e.count == 1 {
				_ = binary.Write(&b, binary.LittleEndian, uint16(e.val))
				_ = binary.Write(&b, binary.LittleEndian, uint16(0))
			} else {
				_ = binary.Write(&b, binary.LittleEndian, e.val)
			}
		}
		prevNext = b.Len()
		_ = binary.Write(&b, binary.LittleEndian, uint32(0))
	}
	return b.Bytes()
}

func archivePDFImportCorpus(t *testing.T) []archivePDFImportInput {
	t.Helper()
	var cs []archivePDFImportInput
	add := func(id, ext string, data []byte, ocr string) {
		cs = append(cs, archivePDFImportInput{id: id, Ext: ext, data: data, ocr: ocr})
	}
	rgba := func(w, h int, f func(x, y int) color.RGBA) *image.RGBA {
		m := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				m.SetRGBA(x, y, f(x, y))
			}
		}
		return m
	}
	nrgba := func(w, h int, f func(x, y int) color.NRGBA) *image.NRGBA {
		m := image.NewNRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				m.SetNRGBA(x, y, f(x, y))
			}
		}
		return m
	}
	encPNG := func(img image.Image) []byte {
		return mustEncode(t, func(w io.Writer) error { return png.Encode(w, img) })
	}
	opaque := func(x, y int) color.RGBA {
		return color.RGBA{uint8(20 + 25*x), uint8(200 - 20*y), uint8(10 * (x + y)), 255}
	}
	// --- PNG, Go encoder
	add("png-transparent-rgba-8x8", ".png", encPNG(image.NewRGBA(image.Rect(0, 0, 8, 8))), "")
	add("png-transparent-with-text", ".png", encPNG(image.NewRGBA(image.Rect(0, 0, 8, 8))), "Archive regression text")
	add("png-rgb-opaque", ".png", encPNG(rgba(9, 7, opaque)), "")
	add("png-rgb-opaque-with-text", ".png", encPNG(rgba(9, 7, opaque)), "hello\n\n--- Page ---\n\nworld")
	add("png-nrgba-alpha", ".png", encPNG(nrgba(9, 7, func(x, y int) color.NRGBA {
		return color.NRGBA{uint8(20 + 25*x), uint8(200 - 20*y), uint8(10 * (x + y)), uint8(40 + 25*((x+y)%9))}
	})), "")
	add("png-nrgba-first-pixel-transparent", ".png", encPNG(nrgba(4, 3, func(x, y int) color.NRGBA {
		a := uint8(255)
		if x == 0 && y == 0 {
			a = 0
		}
		return color.NRGBA{uint8(x * 60), uint8(y * 90), 7, a}
	})), "")
	add("png-nrgba-opaque", ".png", encPNG(nrgba(5, 4, func(x, y int) color.NRGBA { return color.NRGBA{uint8(x * 50), uint8(y * 60), 9, 255} })), "")
	g8 := image.NewGray(image.Rect(0, 0, 7, 5))
	for i := range g8.Pix {
		g8.Pix[i] = uint8(i * 7)
	}
	add("png-gray8", ".png", encPNG(g8), "")
	g16 := image.NewGray16(image.Rect(0, 0, 5, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 5; x++ {
			g16.SetGray16(x, y, color.Gray16{uint16(x*13000 + y*1111)})
		}
	}
	add("png-gray16", ".png", encPNG(g16), "")
	rgba64 := image.NewRGBA64(image.Rect(0, 0, 4, 3))
	nrgba64 := image.NewNRGBA64(image.Rect(0, 0, 4, 3))
	for y := 0; y < 3; y++ {
		for x := 0; x < 4; x++ {
			rgba64.SetRGBA64(x, y, color.RGBA64{uint16(x * 9000), uint16(y * 12000), 4000, 0xFFFF})
			nrgba64.SetNRGBA64(x, y, color.NRGBA64{uint16(x * 9000), uint16(y * 12000), 4000, uint16(20000 + x*9000 + y*100)})
		}
	}
	add("png-rgb16-opaque", ".png", encPNG(rgba64), "")
	add("png-rgba16-alpha", ".png", encPNG(nrgba64), "")
	for _, n := range []int{2, 3, 4, 16, 200} {
		pal := make(color.Palette, n)
		for i := range pal {
			pal[i] = color.NRGBA{uint8(i * 17), uint8(255 - i*5), uint8(i * 3), uint8(255 - (i%3)*60)}
		}
		if n == 2 || n == 16 {
			for i := range pal {
				pal[i] = color.RGBA{uint8(i * 17), uint8(255 - i*5), uint8(i * 3), 255}
			}
		}
		pm := image.NewPaletted(image.Rect(0, 0, 6, 5), pal)
		for i := range pm.Pix {
			pm.Pix[i] = uint8(i % n)
		}
		add(fmt.Sprintf("png-paletted-%d", n), ".png", encPNG(pm), "")
	}
	add("png-1x1", ".png", encPNG(rgba(1, 1, func(x, y int) color.RGBA { return color.RGBA{1, 2, 3, 255} })), "")
	add("png-1000x1000-interpolate-off", ".png", encPNG(rgba(1000, 1000, func(x, y int) color.RGBA { return color.RGBA{9, 8, 7, 255} })), "")
	add("png-1000x999-interpolate-on", ".png", encPNG(rgba(1000, 999, func(x, y int) color.RGBA { return color.RGBA{9, 8, 7, 255} })), "")
	add("png-extension-jpg", ".jpg", encPNG(rgba(4, 4, opaque)), "")
	add("png-extension-upper", ".PNG", encPNG(rgba(4, 4, opaque)), "")
	// --- PNG, hand built variants Go's encoder never writes
	ramp := func(max int) func(x, y int) []int {
		return func(x, y int) []int { return []int{(x*3 + y*5) % (max + 1)} }
	}
	for _, d := range []int{1, 2, 4} {
		add(fmt.Sprintf("png-gray%d-bit", d), ".png", buildPNG(pngSpec{w: 11, h: 5, depth: d, ctype: 0, sample: ramp(1<<d - 1)}), "")
	}
	add("png-gray8-trns", ".png", buildPNG(pngSpec{w: 6, h: 4, depth: 8, ctype: 0, trns: []byte{0, 12}, sample: func(x, y int) []int { return []int{(x + y*2) * 6 % 40} }}), "")
	add("png-rgb8-trns", ".png", buildPNG(pngSpec{w: 6, h: 4, depth: 8, ctype: 2, trns: []byte{0, 3, 0, 4, 0, 5}, sample: func(x, y int) []int {
		if (x+y)%3 == 0 {
			return []int{3, 4, 5}
		}
		return []int{x * 40, y * 50, 99}
	}}), "")
	add("png-graya8", ".png", buildPNG(pngSpec{w: 6, h: 4, depth: 8, ctype: 4, sample: func(x, y int) []int { return []int{x*40 + y, 255 - x*30} }}), "")
	add("png-graya16", ".png", buildPNG(pngSpec{w: 4, h: 3, depth: 16, ctype: 4, sample: func(x, y int) []int { return []int{x*9000 + y, 65535 - x*7000} }}), "")
	add("png-graya8-opaque", ".png", buildPNG(pngSpec{w: 4, h: 3, depth: 8, ctype: 4, sample: func(x, y int) []int { return []int{x * 50, 255} }}), "")
	add("png-gray16-trns", ".png", buildPNG(pngSpec{w: 4, h: 3, depth: 16, ctype: 0, trns: []byte{0x10, 0x00}, sample: func(x, y int) []int {
		if x == 1 {
			return []int{0x1000}
		}
		return []int{x*9000 + 3}
	}}), "")
	add("png-palette4-trns", ".png", buildPNG(pngSpec{w: 7, h: 3, depth: 4, ctype: 3, plte: []byte{255, 0, 0, 0, 255, 0, 0, 0, 255, 9, 9, 9}, trns: []byte{255, 128, 0}, sample: func(x, y int) []int { return []int{(x + y) % 4} }}), "")
	add("png-palette8-short", ".png", buildPNG(pngSpec{w: 3, h: 2, depth: 8, ctype: 3, plte: []byte{1, 2, 3, 4, 5, 6, 7, 8, 9}, sample: func(x, y int) []int { return []int{x} }}), "")
	add("png-interlaced-rgb8", ".png", buildPNG(pngSpec{w: 13, h: 9, depth: 8, ctype: 2, interlace: true, sample: func(x, y int) []int { return []int{x * 19, y * 27, (x * y) % 256} }}), "")
	add("png-interlaced-rgba16", ".png", buildPNG(pngSpec{w: 5, h: 6, depth: 16, ctype: 6, interlace: true, sample: func(x, y int) []int { return []int{x * 9000, y * 8000, 77, 20000 + x*5000} }}), "")
	// --- PNG corrupt
	good := encPNG(rgba(4, 4, opaque))
	add("png-truncated", ".png", good[:len(good)/2], "")
	add("png-gray8-too-much-data", ".png", buildPNG(pngSpec{w: 4, h: 3, depth: 8, ctype: 0, sample: func(x, y int) []int { return []int{x, y} }}), "")
	add("png-palette-missing-plte", ".png", buildPNG(pngSpec{w: 4, h: 3, depth: 8, ctype: 3, sample: func(x, y int) []int { return []int{x} }}), "")
	add("png-palette-index-beyond-plte", ".png", buildPNG(pngSpec{w: 4, h: 2, depth: 8, ctype: 3, plte: []byte{9, 8, 7, 1, 2, 3}, sample: func(x, y int) []int { return []int{x} }}), "")
	add("png-palette-too-many-entries-for-depth", ".png", buildPNG(pngSpec{w: 4, h: 2, depth: 1, ctype: 3, plte: []byte{9, 8, 7, 1, 2, 3, 4, 5, 6}, sample: func(x, y int) []int { return []int{x % 2} }}), "")
	add("png-trns-longer-than-plte", ".png", buildPNG(pngSpec{w: 4, h: 2, depth: 8, ctype: 3, plte: []byte{9, 8, 7, 1, 2, 3}, trns: []byte{200, 100, 50}, sample: func(x, y int) []int { return []int{x % 3} }}), "")
	add("png-gray-trns-bad-length", ".png", buildPNG(pngSpec{w: 4, h: 2, depth: 8, ctype: 0, trns: []byte{0, 1, 2}, sample: func(x, y int) []int { return []int{x} }}), "")
	add("png-gray4-trns", ".png", buildPNG(pngSpec{w: 7, h: 3, depth: 4, ctype: 0, trns: []byte{0, 0x11}, sample: func(x, y int) []int { return []int{(x + y) % 16} }}), "")
	add("png-gray1-trns", ".png", buildPNG(pngSpec{w: 9, h: 3, depth: 1, ctype: 0, trns: []byte{0, 1}, sample: func(x, y int) []int { return []int{(x + y) % 2} }}), "")
	add("png-rgb16-trns", ".png", buildPNG(pngSpec{w: 4, h: 3, depth: 16, ctype: 2, trns: []byte{0x10, 0, 0x20, 0, 0x30, 0}, sample: func(x, y int) []int {
		if x == 2 {
			return []int{0x1000, 0x2000, 0x3000}
		}
		return []int{x * 9000, y * 8000, 5}
	}}), "")
	bad := append([]byte{}, good...)
	bad[len(bad)-20] ^= 0x55
	add("png-bad-crc", ".png", bad, "")
	add("png-bad-signature", ".png", append([]byte("X"), good[1:]...), "")
	add("png-empty-file", ".png", nil, "")
	// --- JPEG
	enc := func(img image.Image, q int) []byte {
		return mustEncode(t, func(w io.Writer) error { return jpeg.Encode(w, img, &jpeg.Options{Quality: q}) })
	}
	add("jpeg-color", ".jpg", enc(rgba(16, 12, opaque), 90), "")
	add("jpeg-color-with-text", ".jpeg", enc(rgba(16, 12, opaque), 75), "page one text")
	add("jpeg-gray", ".jpg", enc(g8, 90), "")
	add("jpeg-1000x1000", ".jpg", enc(rgba(1000, 1000, func(x, y int) color.RGBA { return color.RGBA{9, 8, 7, 255} }), 50), "")
	jb := enc(rgba(8, 8, opaque), 90)
	sos := bytes.Index(jb, []byte{0xFF, 0xDA})
	add("jpeg-truncated-before-sos", ".jpg", jb[:sos], "")
	add("jpeg-truncated-after-sos", ".jpg", jb[:sos+20], "")
	add("jpeg-cmyk-header-only", ".jpg", []byte{0xFF, 0xD8, 0xFF, 0xC0, 0, 20, 8, 0, 3, 0, 5, 4, 1, 0x11, 0, 2, 0x11, 0, 3, 0x11, 0, 4, 0x11, 0, 0xFF, 0xDA, 0, 14, 4, 1, 0, 2, 0, 3, 0, 4, 0, 0, 0x3F, 0, 0xFF, 0xD9}, "")
	add("jpeg-jfif-header-only-after-sof", ".jpg", []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 16, 'J', 'F', 'I', 'F', 0, 1, 1, 0, 0, 1, 0, 1, 0, 0, 0xFF, 0xC0, 0, 17, 8, 0, 6, 0, 9, 3, 1, 0x22, 0, 2, 0x11, 1, 3, 0x11, 1}, "")
	add("jpeg-adobe-rgb", ".jpg", []byte{0xFF, 0xD8, 0xFF, 0xEE, 0, 14, 'A', 'd', 'o', 'b', 'e', 0, 100, 0, 0, 0, 0, 0, 0xFF, 0xC0, 0, 17, 8, 0, 6, 0, 9, 3, 1, 0x11, 0, 2, 0x11, 0, 3, 0x11, 0, 0xFF, 0xDA, 0, 12, 3, 1, 0, 2, 0, 3, 0, 0, 63, 0}, "")
	add("jpeg-rgb-component-ids", ".jpg", []byte{0xFF, 0xD8, 0xFF, 0xC0, 0, 17, 8, 0, 6, 0, 9, 3, 'R', 0x11, 0, 'G', 0x11, 0, 'B', 0x11, 0, 0xFF, 0xDA, 0, 12, 3, 'R', 0, 'G', 0, 'B', 0, 0, 63, 0}, "")
	add("jpeg-bad-precision", ".jpg", []byte{0xFF, 0xD8, 0xFF, 0xC0, 0, 11, 12, 0, 6, 0, 9, 1, 1, 0x11, 0, 0xFF, 0xDA, 0, 8, 1, 1, 0, 0, 63, 0}, "")
	add("jpeg-garbage", ".jpg", []byte{0xFF, 0xD8, 0x00, 0x01, 0x02}, "")
	add("jpeg-extension-png", ".png", enc(rgba(6, 6, opaque), 90), "")
	// --- BMP
	encBMP := func(img image.Image) []byte {
		return mustEncode(t, func(w io.Writer) error { return bmp.Encode(w, img) })
	}
	add("bmp-rgb24", ".bmp", encBMP(rgba(9, 7, opaque)), "")
	add("bmp-rgba32", ".bmp", encBMP(nrgba(9, 7, func(x, y int) color.NRGBA { return color.NRGBA{uint8(x * 25), uint8(y * 30), 5, uint8(60 + x*20)} })), "")
	pb := image.NewPaletted(image.Rect(0, 0, 6, 5), color.Palette{color.RGBA{255, 0, 0, 255}, color.RGBA{0, 255, 0, 255}, color.RGBA{0, 0, 255, 255}})
	for i := range pb.Pix {
		pb.Pix[i] = uint8(i % 3)
	}
	add("bmp-paletted8", ".bmp", encBMP(pb), "")
	add("bmp-gray", ".bmp", encBMP(g8), "")
	add("bmp-v4-alpha", ".bmp", bmpV4(5, 3, func(x, y int) [4]byte { return [4]byte{byte(x * 40), byte(y * 60), 9, byte(30 + x*50)} }), "")
	add("bmp-wrong-planes", ".bmp", func() []byte { b := encBMP(rgba(5, 3, opaque)); b[26] = 2; return b }(), "")
	add("bmp-truncated", ".bmp", encBMP(rgba(9, 7, opaque))[:60], "")
	// --- TIFF
	encTIFF := func(img image.Image, opts *tiff.Options) []byte {
		return mustEncode(t, func(w io.Writer) error { return tiff.Encode(w, img, opts) })
	}
	add("tiff-rgba-uncompressed", ".tif", encTIFF(nrgba(9, 7, func(x, y int) color.NRGBA {
		return color.NRGBA{uint8(x * 25), uint8(y * 30), 5, uint8(60 + x*20)}
	}), &tiff.Options{Compression: tiff.Uncompressed}), "")
	add("tiff-rgb-deflate", ".tiff", encTIFF(rgba(9, 7, opaque), &tiff.Options{Compression: tiff.Deflate, Predictor: true}), "")
	add("tiff-gray", ".tif", encTIFF(g8, &tiff.Options{Compression: tiff.Uncompressed}), "")
	add("tiff-gray16", ".tif", encTIFF(g16, &tiff.Options{Compression: tiff.Uncompressed}), "")
	add("tiff-paletted", ".tif", encTIFF(pb, &tiff.Options{Compression: tiff.Uncompressed}), "")
	pix3 := func(x, y int) []byte { return []byte{byte(x * 20), byte(y * 30), byte(x + y)} }
	pix1 := func(x, y int) []byte { return []byte{byte(x*9 + y)} }
	pix4 := func(x, y int) []byte { return []byte{byte(x * 20), byte(y * 30), 7, byte(255 - x*10)} }
	add("tiff-hand-rgb", ".tif", buildTIFF([]tiffPage{{w: 5, h: 4, spp: 3, pix: pix3}}), "")
	add("tiff-hand-multipage-rgb-gray-rgba", ".tif", buildTIFF([]tiffPage{{w: 5, h: 4, spp: 3, pix: pix3}, {w: 6, h: 3, spp: 1, pix: pix1}, {w: 4, h: 4, spp: 4, pix: pix4}}), "p1\n\n--- Page ---\n\np2\n\n--- Page ---\n\np3")
	add("tiff-hand-palette", ".tif", buildTIFF([]tiffPage{{w: 5, h: 4, spp: 1, palette: true, pix: pix1}}), "")
	add("tiff-hand-cmyk", ".tif", buildTIFF([]tiffPage{{w: 5, h: 4, spp: 4, cmyk: true, pix: pix4}}), "")
	add("tiff-hand-packbits-rgb", ".tif", buildTIFF([]tiffPage{{w: 5, h: 4, spp: 3, packbits: true, pix: pix3}}), "")
	add("tiff-hand-packbits-gray", ".tif", buildTIFF([]tiffPage{{w: 300, h: 3, spp: 1, packbits: true, pix: func(x, y int) []byte { return []byte{byte(x*7 + y)} }}}), "")
	add("tiff-hand-lzw-rgb", ".tif", buildTIFF([]tiffPage{{w: 5, h: 4, spp: 3, lzw: true, pix: pix3}}), "")
	add("tiff-hand-lzw-gray-long", ".tif", buildTIFF([]tiffPage{{w: 120, h: 90, spp: 1, lzw: true, pix: func(x, y int) []byte { return []byte{byte((x*x + y*3) % 251)} }}}), "")
	tb := buildTIFF([]tiffPage{{w: 5, h: 4, spp: 3, pix: pix3}})
	add("tiff-truncated", ".tif", tb[:len(tb)-30], "")
	add("tiff-bad-ifd", ".tif", append(append([]byte{}, tb[:4]...), 0xFF, 0xFF, 0xFF, 0x7F), "")
	// --- WebP (cwebp 1.6 output of a 9x7 gradient; Go has no encoder)
	for _, w := range []struct{ id, b64 string }{
		{"webp-lossless", "UklGRjQAAABXRUJQVlA4TCgAAAAvCIABALmM6H/sIqL/AcJso/mTDWYa9zoBBWkbsLAidkchJkDhZzwB"},
		{"webp-lossless-alpha", "UklGRkQAAABXRUJQVlA4TDgAAAAvCIABELmM6H/sIqL/YTCQts39K7uYu/jmoVWQtgELK2J3FAQCaYgjhA2egJXm1aMyf5EkSZIk+Q=="},
		{"webp-lossy", "UklGRkgAAABXRUJQVlA4IDwAAACwAQCdASoJAAcAAUAmJagCdAEO/gLsAP2/grrNUn50RnwY33/J/ub89f0Zj4f/Ljmv/ClxLqf33+igAAA="},
		{"webp-lossy-alpha", "UklGRpgAAABXRUJQVlA4WAoAAAAQAAAACAAABgAAQUxQSDUAAAABRyAQSDgy2N9xh4jIDKoBMKp61+cUUjiFU0hhCikMfBIR/X+CAuL9c5MqppoUmrFTiOE8AQBWUDggPAAAALABAJ0BKgkABwABQCYlsAJ0AQ7+AuwA/b+Cus1SfnRGfBjff8n+5vz1/d6j7L/YS/+WPnsR347+zAAAAA=="},
	} {
		data, _ := base64.StdEncoding.DecodeString(w.b64)
		add(w.id, ".webp", data, "")
	}
	add("webp-truncated", ".webp", []byte("RIFF\x10\x00\x00\x00WEBPVP8 "), "")
	// --- dispatch errors
	add("unsupported-gif", ".gif", []byte("GIF89a"), "")
	add("unsupported-no-extension", "", []byte("x"), "")
	add("unsupported-txt", ".txt", []byte("hello"), "")
	return cs
}

type archivePDFStampInput struct {
	id  string
	pdf []byte
	ocr string
}

func archivePDFImportPDF(t *testing.T, imgs [][]byte, spec string) []byte {
	t.Helper()
	dir := t.TempDir()
	var files []string
	for i, b := range imgs {
		p := filepath.Join(dir, fmt.Sprintf("p%d.png", i))
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		files = append(files, p)
	}
	var imp *pdfcpu.Import
	if spec != "" {
		var err error
		if imp, err = pdfcpu.ParseImportDetails(spec, types.POINTS); err != nil {
			t.Fatalf("import spec %q: %v", spec, err)
		}
	}
	out := filepath.Join(dir, "in.pdf")
	if err := api.ImportImagesFile(context.Background(), files, out, imp, nil); err != nil {
		t.Fatalf("import: %v", err)
	}
	b, err := os.ReadFile(out) //nolint:gosec // disposable path
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func archivePDFTransform(t *testing.T, in []byte, f func(inFile, outFile string) error) []byte {
	t.Helper()
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.pdf"), filepath.Join(dir, "b.pdf")
	if err := os.WriteFile(a, in, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f(a, b); err != nil {
		t.Fatalf("transform: %v", err)
	}
	out, err := os.ReadFile(b) //nolint:gosec // disposable path
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func archivePDFStampCorpus(t *testing.T) []archivePDFStampInput {
	t.Helper()
	var cs []archivePDFStampInput
	add := func(id string, pdf []byte, ocr string) {
		cs = append(cs, archivePDFStampInput{id: id, pdf: pdf, ocr: ocr})
	}
	solid := func(w, h int, c color.RGBA) []byte {
		m := image.NewRGBA(image.Rect(0, 0, w, h))
		for i := 0; i < len(m.Pix); i += 4 {
			m.Pix[i], m.Pix[i+1], m.Pix[i+2], m.Pix[i+3] = c.R, c.G, c.B, c.A
		}
		return mustEncode(t, func(wr io.Writer) error { return png.Encode(wr, m) })
	}
	sq := solid(8, 8, color.RGBA{})
	one := archivePDFImportPDF(t, [][]byte{sq}, "")
	two := archivePDFImportPDF(t, [][]byte{sq, sq}, "")
	three := archivePDFImportPDF(t, [][]byte{sq, sq, sq}, "")
	a4 := archivePDFImportPDF(t, [][]byte{sq}, "form:A4P, pos:c, sc:1")
	a4l := archivePDFImportPDF(t, [][]byte{sq}, "form:A4L, pos:c, sc:1")
	letter := archivePDFImportPDF(t, [][]byte{sq}, "form:Letter, pos:tl, sc:0.5")
	wide := archivePDFImportPDF(t, [][]byte{solid(40, 8, color.RGBA{9, 9, 9, 255})}, "")
	tall := archivePDFImportPDF(t, [][]byte{solid(8, 40, color.RGBA{9, 9, 9, 255})}, "")
	mixed := archivePDFImportPDF(t, [][]byte{solid(30, 10, color.RGBA{1, 1, 1, 255}), solid(10, 30, color.RGBA{2, 2, 2, 255}), solid(20, 20, color.RGBA{3, 3, 3, 255})}, "")
	big := archivePDFImportPDF(t, [][]byte{solid(600, 800, color.RGBA{200, 200, 200, 255})}, "")
	texts := []struct{ id, ocr string }{
		{"hello", "hello"},
		{"two-lines", "hello\nworld foo"},
		{"long-line", "The quick brown fox jumps over the lazy dog 0123456789"},
		{"uneven-lines", "a\nbbbbbbbbbbbbbbbb\ncc"},
		{"blank-line-inside", "top\n\nbottom"},
		{"latin1", "Gr\u00fc\u00dfe aus M\u00fcnchen"},
		{"winansi", "5\u20ac \u201cq\u201d \u2013 \u2022 \u2122"},
		{"unmappable", "\u65e5\u672c\u8a9e x \U0001F600"},
		{"percent-page-tokens", "page %p of %P (%v)"},
		{"percent-offset", "p%p10 and %%100%% x%y%"},
		{"delims", "a(b)c\\d"},
		{"tab-cr", "a\tb\rc"},
		{"literal-backslash-n", "first\\nsecond"},
		{"above-7e-widths", "\u00c0\u00c9\u00ce\u00d5\u00dc \u00e0\u00e9\u00ee\u00f5\u00fc \u00a9\u00ae\u00b1\u00bd"},
	}
	for _, tx := range texts {
		add("a4-"+tx.id, a4, tx.ocr)
	}
	for _, tx := range texts[:3] {
		add("square-"+tx.id, one, tx.ocr)
		add("wide-"+tx.id, wide, tx.ocr)
		add("tall-"+tx.id, tall, tx.ocr)
		add("a4l-"+tx.id, a4l, tx.ocr)
		add("letter-"+tx.id, letter, tx.ocr)
		add("big-"+tx.id, big, tx.ocr)
	}
	add("two-pages-page-tokens", two, "page %p of %P\n\n--- Page ---\n\nsecond %p/%P")
	add("three-pages-same-text-cached-form", three, "same\n\n--- Page ---\n\nsame\n\n--- Page ---\n\nsame\n\n--- Page ---\n\nsame")
	add("two-pages-marker", two, "alpha\n\n--- Page ---\n\nbeta")
	add("two-pages-first-only", two, "only")
	add("two-pages-second-only", two, "\f"+"second")
	add("two-pages-blank", two, " \n\t ")
	add("two-pages-formfeed", two, "fa\fbb")
	add("two-pages-three-ocr-pages", two, "a\n\n--- Page ---\n\nb\n\n--- Page ---\n\nc")
	add("three-pages-blank-middle", three, "a\n\n--- Page ---\n\n \n\n--- Page ---\n\nc")
	add("three-pages-all", three, "a\n\n--- Page ---\n\nb\n\n--- Page ---\n\nc")
	add("mixed-sizes", mixed, "a\n\n--- Page ---\n\nsecond page text\n\n--- Page ---\n\nthird")
	add("mixed-sizes-overflow-all-pages", mixed, "1\n\n--- Page ---\n\n2\n\n--- Page ---\n\n3\n\n--- Page ---\n\n4")
	// rotated and cropped pages
	for _, deg := range []int{90, 180, 270} {
		rot := archivePDFTransform(t, mixed, func(in, out string) error { return api.RotateFile(context.Background(), in, out, deg, nil, nil) })
		add(fmt.Sprintf("rotated-%d", deg), rot, "a\n\n--- Page ---\n\nsecond page text\n\n--- Page ---\n\nthird")
	}
	crop := archivePDFTransform(t, a4, func(in, out string) error {
		pb, err := api.PageBoundaries("crop:[50 60 300 400]", types.POINTS)
		if err != nil {
			return err
		}
		return api.AddBoxesFile(context.Background(), in, out, nil, pb, nil)
	})
	add("cropbox", crop, "cropped page text")
	userUnit := archivePDFTransform(t, a4, func(in, out string) error {
		qdf := out + ".qdf"
		if b, err := exec.Command("qpdf", "--qdf", "--object-streams=disable", in, qdf).CombinedOutput(); err != nil { //nolint:gosec // fixed tool
			return fmt.Errorf("qpdf --qdf: %v: %s", err, b)
		}
		raw, err := os.ReadFile(qdf) //nolint:gosec // disposable temp path
		if err != nil {
			return err
		}
		edited := bytes.Replace(raw, []byte("/MediaBox"), []byte("/UserUnit 2\n  /MediaBox"), 1)
		if bytes.Equal(edited, raw) {
			return fmt.Errorf("no MediaBox to edit")
		}
		if err := os.WriteFile(qdf, edited, 0o600); err != nil {
			return err
		}
		// The edit shifts offsets; qpdf rebuilds the cross-reference table and exits 3 for that warning.
		b, err := exec.Command("qpdf", qdf, out).CombinedOutput() //nolint:gosec // fixed tool
		if ee, ok := err.(*exec.ExitError); err != nil && !(ok && ee.ExitCode() == 3) {
			return fmt.Errorf("qpdf rebuild: %v: %s", err, b)
		}
		return nil
	})
	add("userunit-2", userUnit, "scaled page text")
	// stamping a stamped file again (existing OCProperties, resource ids)
	stamped := archivePDFTransform(t, two, func(in, out string) error { return Generate(in, out, "first\n\n--- Page ---\n\nsecond") })
	add("restamp-two-pages", stamped, "again one\n\n--- Page ---\n\nagain two")
	stamped1 := archivePDFTransform(t, a4, func(in, out string) error { return Generate(in, out, "once") })
	add("restamp-a4", stamped1, "twice")
	return cs
}
