package ingest

// Go oracle for the barcode/QR separator path (gozxing): DetectBarcode over an
// image corpus (gozxing writer output, real gozxing test photographs, degraded
// and negative images) and ScanPDFForBarcodes/SplitPDFByBarcode driven against
// fake pdftoppm/qpdf scripts. Scripts and images are stored in the fixture so
// the Rust replay uses byte-identical inputs.

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/datamatrix"
	"github.com/makiuchi-d/gozxing/oned"
	"github.com/makiuchi-d/gozxing/qrcode"
)

const portPdftoppmScript = `#!/bin/sh
PATH=/usr/bin:/bin
d=${0%/*}
US=$(printf '\037'); RS=$(printf '\036'); l=P; for a in "$@"; do l="$l$US$a"; done
printf '%s\n' "$l" >> "$d/log"
if [ -f "$d/pdftoppm.exit" ]; then
  [ -f "$d/pdftoppm.err" ] && cat "$d/pdftoppm.err" >&2
  exit "$(cat "$d/pdftoppm.exit")"
fi
prefix=$5
for f in "$d"/img/*; do
  [ -f "$f" ] || continue
  cp "$f" "$prefix-$(basename "$f")"
done
for f in "$d"/img/*; do
  [ -d "$f" ] && mkdir -p "$prefix-$(basename "$f")"
done
exit 0
`

const portQpdfScript = `#!/bin/sh
PATH=/usr/bin:/bin
d=${0%/*}
US=$(printf '\037'); l=Q; for a in "$@"; do l="$l$US$a"; done
printf '%s\n' "$l" >> "$d/log"
if [ "$1" = --show-npages ]; then cat "$d/npages"; exit 0; fi
if [ "$1" = --check ]; then
  if [ -f "$d/check.exit" ]; then [ -f "$d/check.err" ] && cat "$d/check.err" >&2; exit "$(cat "$d/check.exit")"; fi
  exit 0
fi
printf 'PART %s' "$4" > "$6"
exit 0
`

type barcodeDetectCase struct {
	ID     string `json:"id"`
	Origin string `json:"origin"`
	Mime   string `json:"mime"`
	Data   string `json:"data_b64"`
	Text   string `json:"text"`
	Format string `json:"format"`
	Error  string `json:"error"`
}

type barcodeSplitScenario struct {
	ID       string            `json:"id"`
	Pattern  string            `json:"pattern"`
	Discard  bool              `json:"discard"`
	ParseASN bool              `json:"parse_asn"`
	Pages    map[string]string `json:"pages"` // file name -> image data base64 ("" = garbage bytes)
	Dirs     []string          `json:"dirs,omitempty"`
	NPages   string            `json:"npages"`
	Tools    string            `json:"tools"` // "all", "no-pdftoppm", "no-qpdf", "pdftoppm-fails", "check-fails"
	Scan     bool              `json:"scan_only"`
}

type barcodeSplitCase struct {
	Scenario   barcodeSplitScenario `json:"scenario"`
	SepPages   []int                `json:"separator_pages"`
	Parts      []string             `json:"parts"`
	PartBodies []string             `json:"part_bodies"`
	ASNs       map[int]int          `json:"asns"`
	Errors     map[int]string       `json:"errors"`
	Error      string               `json:"error"`
	Transcript []string             `json:"transcript"`
	Leftovers  []string             `json:"leftovers"`
}

func matrixImage(m *gozxing.BitMatrix) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, m.GetWidth(), m.GetHeight()))
	for y := 0; y < m.GetHeight(); y++ {
		for x := 0; x < m.GetWidth(); x++ {
			if m.Get(x, y) {
				img.SetGray(x, y, color.Gray{Y: 0})
			} else {
				img.SetGray(x, y, color.Gray{Y: 255})
			}
		}
	}
	return img
}

func pngBytes(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func jpegBytes(t *testing.T, img image.Image, q int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: q}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func rotate90(src *image.Gray) *image.Gray {
	b := src.Bounds()
	dst := image.NewGray(image.Rect(0, 0, b.Dy(), b.Dx()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			dst.SetGray(b.Dy()-1-y, x, src.GrayAt(x, y))
		}
	}
	return dst
}

func saltNoise(src *image.Gray, seed int64, per int) *image.Gray {
	dst := image.NewGray(src.Bounds())
	copy(dst.Pix, src.Pix)
	r := rand.New(rand.NewSource(seed)) //nolint:gosec // deterministic corpus
	for i := range dst.Pix {
		if r.Intn(1000) < per {
			dst.Pix[i] = byte(r.Intn(2) * 255)
		}
	}
	return dst
}

func writerImages(t *testing.T) []struct {
	id  string
	img *image.Gray
} {
	t.Helper()
	type enc interface {
		Encode(string, gozxing.BarcodeFormat, int, int, map[gozxing.EncodeHintType]interface{}) (*gozxing.BitMatrix, error)
	}
	specs := []struct {
		id      string
		w       enc
		format  gozxing.BarcodeFormat
		text    string
		wd, hgt int
	}{
		{"code39-patch", oned.NewCode39Writer(), gozxing.BarcodeFormat_CODE_39, "PATCH-T1", 400, 100},
		{"code39-mixed", oned.NewCode39Writer(), gozxing.BarcodeFormat_CODE_39, "ABC-123 $/+%", 500, 100},
		{"code128-patch", oned.NewCode128Writer(), gozxing.BarcodeFormat_CODE_128, "PATCH-T separator", 400, 100},
		{"code128-asn", oned.NewCode128Writer(), gozxing.BarcodeFormat_CODE_128, "ASN:42", 300, 100},
		{"code128-lower", oned.NewCode128Writer(), gozxing.BarcodeFormat_CODE_128, "patch-t lower", 400, 100},
		{"code128-digits", oned.NewCode128Writer(), gozxing.BarcodeFormat_CODE_128, "0123456789012345", 400, 100},
		{"ean13", oned.NewEAN13Writer(), gozxing.BarcodeFormat_EAN_13, "5901234123457", 300, 100},
		{"ean13-leading-zero", oned.NewEAN13Writer(), gozxing.BarcodeFormat_EAN_13, "0036000291452", 300, 100},
		{"ean8", oned.NewEAN8Writer(), gozxing.BarcodeFormat_EAN_8, "96385074", 300, 100},
		{"upca", oned.NewUPCAWriter(), gozxing.BarcodeFormat_UPC_A, "036000291452", 300, 100},
		{"upce", oned.NewUPCEWriter(), gozxing.BarcodeFormat_UPC_E, "01234565", 300, 100},
		{"itf", oned.NewITFWriter(), gozxing.BarcodeFormat_ITF, "0123456789", 400, 100},
		{"codabar-unsupported", oned.NewCodaBarWriter(), gozxing.BarcodeFormat_CODABAR, "A123456B", 400, 100},
		{"code93-unsupported", oned.NewCode93Writer(), gozxing.BarcodeFormat_CODE_93, "CODE93", 400, 100},
		{"qr-patch", qrcode.NewQRCodeWriter(), gozxing.BarcodeFormat_QR_CODE, "PATCH-T separator", 240, 240},
		{"qr-asn", qrcode.NewQRCodeWriter(), gozxing.BarcodeFormat_QR_CODE, "ASN:1234", 240, 240},
		{"qr-unicode", qrcode.NewQRCodeWriter(), gozxing.BarcodeFormat_QR_CODE, "Größe ÄÖÜ € 日本語", 300, 300},
		{"qr-long", qrcode.NewQRCodeWriter(), gozxing.BarcodeFormat_QR_CODE, strings.Repeat("0123456789abcdef", 12), 400, 400},
		{"qr-url", qrcode.NewQRCodeWriter(), gozxing.BarcodeFormat_QR_CODE, "https://example.invalid/a?b=c&d=e#f", 260, 260},
		{"datamatrix-patch", datamatrix.NewDataMatrixWriter(), gozxing.BarcodeFormat_DATA_MATRIX, "PATCH-T-DM", 200, 200},
		{"datamatrix-long", datamatrix.NewDataMatrixWriter(), gozxing.BarcodeFormat_DATA_MATRIX, "Datamatrix payload 0123456789", 260, 260},
	}
	var out []struct {
		id  string
		img *image.Gray
	}
	for _, s := range specs {
		m, err := s.w.Encode(s.text, s.format, s.wd, s.hgt, nil)
		if err != nil {
			t.Fatalf("encode %s: %v", s.id, err)
		}
		out = append(out, struct {
			id  string
			img *image.Gray
		}{s.id, matrixImage(m)})
	}
	return out
}

func goDetect(img image.Image) (string, string, string) {
	text, format, err := DetectBarcode(img)
	if err != nil {
		return "", "", err.Error()
	}
	return text, format, ""
}

func detectCase(t *testing.T, id, origin, mime string, data []byte) barcodeDetectCase {
	t.Helper()
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode %s: %v", id, err)
	}
	text, format, e := goDetect(img)
	return barcodeDetectCase{ID: id, Origin: origin, Mime: mime, Data: base64.StdEncoding.EncodeToString(data), Text: text, Format: format, Error: e}
}

func gozxingTestFiles(t *testing.T) []string {
	t.Helper()
	root := gozxingTestdataDir(t)
	var files []string
	for _, rel := range []string{
		"oned/testdata/code39", "oned/testdata/code128", "oned/testdata/ean13", "oned/testdata/ean8",
		"oned/testdata/upca", "oned/testdata/upce", "oned/testdata/itf", "oned/testdata/codabar",
		"qrcode/testdata/qrcode-1", "datamatrix/testdata",
	} {
		dir := filepath.Join(root, filepath.FromSlash(rel))
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("gozxing testdata %s: %v", dir, err)
		}
		n := 0
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !(strings.HasSuffix(name, ".png") || strings.HasSuffix(name, ".jpg")) {
				continue
			}
			info, err := e.Info()
			if err != nil || info.Size() > 60*1024 {
				continue
			}
			files = append(files, filepath.Join(dir, name))
			if n++; n == 5 {
				break
			}
		}
	}
	for _, f := range []string{"qrcode/testdata/qrcode-2.jpg", "qrcode/testdata/qrcode-3.jpg", "qrcode/testdata/structured_append_1.png"} {
		p := filepath.Join(root, filepath.FromSlash(f))
		if _, err := os.Stat(p); err == nil {
			files = append(files, p)
		}
	}
	return files
}

func buildDetectCorpus(t *testing.T) []barcodeDetectCase {
	t.Helper()
	var cases []barcodeDetectCase
	for _, w := range writerImages(t) {
		cases = append(cases, detectCase(t, w.id+"/png", "gozxing writer", "image/png", pngBytes(t, w.img)))
		cases = append(cases, detectCase(t, w.id+"/jpeg90", "gozxing writer", "image/jpeg", jpegBytes(t, w.img, 90)))
		cases = append(cases, detectCase(t, w.id+"/noise", "gozxing writer + 2% salt noise", "image/png", pngBytes(t, saltNoise(w.img, 7, 20))))
		cases = append(cases, detectCase(t, w.id+"/rot90", "gozxing writer rotated 90 degrees", "image/png", pngBytes(t, rotate90(w.img))))
	}
	for _, f := range gozxingTestFiles(t) {
		data, err := os.ReadFile(f) //nolint:gosec // module cache
		if err != nil {
			t.Fatal(err)
		}
		mime := "image/png"
		if strings.HasSuffix(f, ".jpg") {
			mime = "image/jpeg"
		}
		rel := f[strings.Index(f, "gozxing@v0.1.1")+len("gozxing@v0.1.1/"):]
		cases = append(cases, detectCase(t, "testdata/"+rel, "gozxing@v0.1.1 testdata", mime, data))
	}
	blank := image.NewGray(image.Rect(0, 0, 120, 120))
	for i := range blank.Pix {
		blank.Pix[i] = 255
	}
	cases = append(cases, detectCase(t, "blank-white", "synthetic", "image/png", pngBytes(t, blank)))
	black := image.NewGray(image.Rect(0, 0, 64, 64))
	cases = append(cases, detectCase(t, "blank-black", "synthetic", "image/png", pngBytes(t, black)))
	r := rand.New(rand.NewSource(99)) //nolint:gosec // deterministic corpus
	noise := image.NewGray(image.Rect(0, 0, 200, 200))
	for i := range noise.Pix {
		noise.Pix[i] = byte(r.Intn(256))
	}
	cases = append(cases, detectCase(t, "random-noise", "synthetic seed 99", "image/png", pngBytes(t, noise)))
	rgb := image.NewRGBA(image.Rect(0, 0, 150, 150))
	for y := 0; y < 150; y++ {
		for x := 0; x < 150; x++ {
			rgb.Set(x, y, color.RGBA{R: byte(x), G: byte(y), B: 128, A: 255})
		}
	}
	cases = append(cases, detectCase(t, "color-gradient", "synthetic", "image/png", pngBytes(t, rgb)))
	// A colour (RGBA) rendering of a QR proves the luminance conversion path.
	qr, err := qrcode.NewQRCodeWriter().Encode("PATCH-T rgb", gozxing.BarcodeFormat_QR_CODE, 220, 220, nil)
	if err != nil {
		t.Fatal(err)
	}
	q := matrixImage(qr)
	col := image.NewRGBA(q.Bounds())
	for y := 0; y < q.Bounds().Dy(); y++ {
		for x := 0; x < q.Bounds().Dx(); x++ {
			if q.GrayAt(x, y).Y == 0 {
				col.Set(x, y, color.RGBA{R: 20, G: 40, B: 90, A: 255})
			} else {
				col.Set(x, y, color.RGBA{R: 250, G: 245, B: 200, A: 255})
			}
		}
	}
	cases = append(cases, detectCase(t, "qr-colour-rgba", "synthetic", "image/png", pngBytes(t, col)))
	cases = append(cases, detectCase(t, "qr-colour-rgba/jpeg75", "synthetic", "image/jpeg", jpegBytes(t, col, 75)))
	return cases
}

func qrPage(t *testing.T, text string) []byte {
	t.Helper()
	m, err := qrcode.NewQRCodeWriter().Encode(text, gozxing.BarcodeFormat_QR_CODE, 220, 220, nil)
	if err != nil {
		t.Fatal(err)
	}
	return pngBytes(t, matrixImage(m))
}

func code128Page(t *testing.T, text string) []byte {
	t.Helper()
	m, err := oned.NewCode128Writer().Encode(text, gozxing.BarcodeFormat_CODE_128, 360, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	return pngBytes(t, matrixImage(m))
}

func blankPage(t *testing.T) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 100, 140))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	return pngBytes(t, img)
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func numbered(pages [][]byte, width int) map[string]string {
	m := map[string]string{}
	for i, p := range pages {
		m[fmt.Sprintf("%0*d.png", width, i+1)] = b64(p)
	}
	return m
}

func barcodeScenarios(t *testing.T) []barcodeSplitScenario {
	t.Helper()
	blank, sepQR, sepCode := blankPage(t), qrPage(t, "PATCH-T"), code128Page(t, "PATCH-T separator")
	lower := qrPage(t, "patch-t42")
	other := qrPage(t, "OTHER-1")
	asn := qrPage(t, "ASN:77")
	spaced := qrPage(t, " PATCH-T ")
	jpg := jpegBytes(t, func() *image.Gray {
		m, _ := qrcode.NewQRCodeWriter().Encode("PATCH-T jpeg", gozxing.BarcodeFormat_QR_CODE, 220, 220, nil)
		return matrixImage(m)
	}(), 90)
	sc := func(id string, pages map[string]string, npages string, discard bool, tools string) barcodeSplitScenario {
		return barcodeSplitScenario{ID: id, Pattern: "PATCH-T", Discard: discard, Pages: pages, NPages: npages, Tools: tools}
	}
	list := []barcodeSplitScenario{
		sc("discard-sep-page2-of-4", numbered([][]byte{blank, sepQR, blank, blank}, 2), "4", true, "all"),
		sc("retain-sep-page2-of-4", numbered([][]byte{blank, sepQR, blank, blank}, 2), "4", false, "all"),
		sc("two-separators-discard", numbered([][]byte{blank, sepQR, blank, sepCode, blank, blank}, 2), "6", true, "all"),
		sc("two-separators-retain", numbered([][]byte{blank, sepQR, blank, sepCode, blank, blank}, 2), "6", false, "all"),
		sc("lower-case-text-matches", numbered([][]byte{blank, lower, blank}, 2), "3", true, "all"),
		sc("surrounding-space-trimmed", numbered([][]byte{blank, spaced, blank}, 2), "3", true, "all"),
		sc("jpeg-page-decodes", numbered([][]byte{blank, jpg, blank}, 2), "3", true, "all"),
		sc("separator-on-page1-discard-copies", numbered([][]byte{sepQR, blank, blank}, 2), "3", true, "all"),
		sc("separator-on-page1-retain", numbered([][]byte{sepQR, blank, blank}, 2), "3", false, "all"),
		sc("separator-last-page-retain-rejected", numbered([][]byte{blank, blank, sepQR}, 2), "3", false, "all"),
		sc("separator-last-page-discard", numbered([][]byte{blank, blank, sepQR}, 2), "3", true, "all"),
		sc("no-separator-copies-original", numbered([][]byte{blank, other, blank}, 2), "3", true, "all"),
		sc("non-matching-pattern-only", numbered([][]byte{other, other}, 2), "2", false, "all"),
		sc("asn-barcode-ignored", numbered([][]byte{blank, asn, blank}, 2), "3", true, "all"),
		sc("unpadded-names-lexical-order", numbered([][]byte{blank, sepQR, blank, blank, blank, blank, blank, blank, blank, sepQR, blank}, 0), "11", true, "all"),
		sc("padded-names-numeric-order", numbered([][]byte{blank, sepQR, blank, blank, blank, blank, blank, blank, blank, sepQR, blank}, 2), "11", true, "all"),
		sc("corrupt-png-page-skipped", func() map[string]string {
			m := numbered([][]byte{blank, sepQR, blank}, 2)
			m["04.png"] = b64([]byte("not a png"))
			return m
		}(), "4", true, "all"),
		sc("extra-files-and-dirs-ignored", func() map[string]string {
			m := numbered([][]byte{blank, sepQR, blank}, 2)
			m["note.txt"] = b64([]byte("x"))
			m["09.PNG"] = b64(sepQR)
			return m
		}(), "3", true, "all"),
		sc("pdftoppm-missing", numbered([][]byte{blank, sepQR}, 2), "2", true, "no-pdftoppm"),
		sc("pdftoppm-fails", numbered([][]byte{blank, sepQR}, 2), "2", true, "pdftoppm-fails"),
		sc("qpdf-missing-with-separator", numbered([][]byte{blank, sepQR, blank}, 2), "3", true, "no-qpdf"),
		sc("qpdf-missing-without-separator-copies", numbered([][]byte{blank, blank}, 2), "2", true, "no-qpdf"),
		sc("qpdf-check-fails", numbered([][]byte{blank, sepQR, blank}, 2), "3", true, "check-fails"),
		sc("qpdf-bad-page-count", numbered([][]byte{blank, sepQR, blank}, 2), "zero", true, "all"),
		sc("separator-beyond-page-count", numbered([][]byte{blank, blank, sepQR, blank}, 2), "2", true, "all"),
		sc("no-pages-rendered", map[string]string{}, "1", true, "all"),
	}
	scan := sc("scan-only-separators", numbered([][]byte{sepQR, blank, sepCode, other}, 2), "4", true, "all")
	scan.Scan = true
	list = append(list, scan)
	empty := sc("empty-pattern-never-matches", numbered([][]byte{blank, sepQR}, 2), "2", true, "all")
	empty.Pattern = ""
	list = append(list, empty)
	return list
}

var barcodeTmp = regexp.MustCompile(`symdesk-barcode-[0-9]+`)
var pdfWorkspace = regexp.MustCompile(`\.symdesk-pdf-[0-9A-Za-z-]+`)

func runBarcodeScenario(t *testing.T, sc barcodeSplitScenario) barcodeSplitCase {
	t.Helper()
	dir, out := t.TempDir(), t.TempDir()
	tmpRoot := t.TempDir()
	t.Setenv("TMPDIR", tmpRoot)
	imgDir := filepath.Join(dir, "img")
	if err := os.MkdirAll(imgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, data := range sc.Pages {
		raw, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(imgDir, name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write := func(name, content string, mode os.FileMode) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), mode); err != nil { //nolint:gosec // fixture scripts
			t.Fatal(err)
		}
	}
	if sc.Tools != "no-pdftoppm" {
		write("pdftoppm", portPdftoppmScript, 0o755)
	}
	if sc.Tools != "no-qpdf" {
		write("qpdf", portQpdfScript, 0o755)
	}
	switch sc.Tools {
	case "pdftoppm-fails":
		write("pdftoppm.exit", "3", 0o644)
		write("pdftoppm.err", "Syntax Error: boom\n", 0o644)
	case "check-fails":
		write("check.exit", "2", 0o644)
		write("check.err", "qpdf: damaged\n", 0o644)
	}
	n := sc.NPages
	if n == "zero" {
		n = "0"
	}
	write("npages", n+"\n", 0o644)
	write("log", "", 0o644)
	t.Setenv("PATH", dir)
	input := filepath.Join(dir, "in.pdf")
	if err := os.WriteFile(input, []byte("%PDF-fake"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := BarcodeConfig{SeparatorPattern: sc.Pattern, DiscardSeparators: sc.Discard, ParseASN: sc.ParseASN, ASNPrefix: "ASN:"}
	cs := barcodeSplitCase{Scenario: sc, SepPages: []int{}, Parts: []string{}, PartBodies: []string{}, ASNs: map[int]int{}, Errors: map[int]string{}}
	norm := func(s string) string {
		s = strings.ReplaceAll(s, out, "$OUT")
		s = strings.ReplaceAll(s, dir, "$DIR")
		s = strings.ReplaceAll(s, tmpRoot, "$TMP")
		s = barcodeTmp.ReplaceAllString(s, "symdesk-barcode-N")
		return pdfWorkspace.ReplaceAllString(s, ".symdesk-pdf-N")
	}
	if sc.Scan {
		pages, err := ScanPDFForBarcodes(input, cfg)
		if err != nil {
			cs.Error = norm(err.Error())
		}
		cs.SepPages = append(cs.SepPages, pages...)
	} else {
		res, err := SplitPDFByBarcode(input, cfg, out)
		if err != nil {
			cs.Error = norm(err.Error())
		}
		if res != nil {
			cs.SepPages = append(cs.SepPages, res.SeparatorPages...)
			for _, p := range res.Parts {
				cs.Parts = append(cs.Parts, norm(p))
				body, _ := os.ReadFile(p) //nolint:gosec // test-owned output
				cs.PartBodies = append(cs.PartBodies, string(body))
			}
			for k, v := range res.ASNs {
				cs.ASNs[k] = v
			}
			for k, v := range res.Errors {
				cs.Errors[k] = v
			}
		}
	}
	logBytes, _ := os.ReadFile(filepath.Join(dir, "log")) //nolint:gosec // test-owned
	cs.Transcript = []string{}
	for _, line := range strings.Split(strings.TrimRight(string(logBytes), "\n"), "\n") {
		if line != "" {
			cs.Transcript = append(cs.Transcript, norm(line))
		}
	}
	cs.Leftovers = []string{}
	entries, _ := os.ReadDir(out)
	for _, e := range entries {
		cs.Leftovers = append(cs.Leftovers, e.Name())
	}
	sort.Strings(cs.Leftovers)
	if left, _ := os.ReadDir(tmpRoot); len(left) != 0 {
		var names []string
		for _, e := range left {
			names = append(names, e.Name())
		}
		t.Errorf("%s: temp workspace leaked: %v", sc.ID, names)
	}
	return cs
}

func TestPortIngestBarcodeContract(t *testing.T) {
	detect := buildDetectCorpus(t)
	found := 0
	for _, c := range detect {
		if c.Text != "" {
			found++
		}
	}
	if found < 40 {
		t.Fatalf("detection corpus decodes too little (%d/%d): oracle is not exercising barcodes", found, len(detect))
	}
	var splits []barcodeSplitCase
	for _, sc := range barcodeScenarios(t) {
		sc := sc
		splits = append(splits, runBarcodeScenario(t, sc))
	}
	hashes := map[string]string{}
	_, file, _, _ := runtime.Caller(0)
	src := filepath.Dir(file)
	for _, f := range []string{"barcode.go", "split.go", "barcode_port_test.go"} {
		b, e := os.ReadFile(filepath.Join(src, f)) //nolint:gosec // repository source
		if e != nil {
			t.Fatal(e)
		}
		hashes[f] = fmt.Sprintf("%x", sha256.Sum256(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))))
	}
	content, e := json.MarshalIndent(map[string]any{
		"schema_version": 1,
		"oracle":         fixtureoracle.Current(),
		"source_hashes":  hashes,
		"scripts":        map[string]string{"pdftoppm": portPdftoppmScript, "qpdf": portQpdfScript},
		"detect":         detect,
		"split":          splits,
		"normalizations": []string{
			"temp roots replaced by $DIR (tool dir), $OUT (split output dir) and $TMP (TMPDIR); symdesk-barcode-<random> by symdesk-barcode-N; .symdesk-pdf-<random> by .symdesk-pdf-N",
		},
	}, "", " ")
	if e != nil {
		t.Fatal(e)
	}
	content = append(content, '\n')
	out := filepath.Join(src, "../../testdata/port/ingest/barcode.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if e = os.WriteFile(out, content, 0o600); e != nil {
			t.Fatal(e)
		}
		return
	}
	old, e := os.ReadFile(out) //nolint:gosec // repository fixture
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(old, content) {
		t.Fatal("ingest barcode fixture drift; regenerate deliberately")
	}
}
