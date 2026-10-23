// Command rendermeasuregen freezes the production Go SymDraw text measurement
// contract (internal/draw/measure over the embedded internal/draw/fonts Inter
// TrueType files) as a Rust replay fixture. Every case records the exact
// json.Marshal bytes Go produced for one measurement call: single-line
// metrics, detailed per-glyph advances, line breaking, intrinsic node sizing
// and font weight normalization. A sweep entry additionally covers the whole
// Unicode scalar range through SHA-256 digests of the per-glyph advances, so
// the Rust sfnt/cmap port is checked for every code point, not only samples.
//
// Host independence: the corpus contains no paths, no randomness and no
// platform-dependent values. Circle node sizes are only frozen where the
// hypotenuse is exactly representable (Pythagorean triples), because
// math.Hypot may differ in the last bit between architectures (assembly on
// amd64, fused multiply-add on arm64). Font sizes stay far from the int32
// overflow of fontSize*64, whose float-to-int conversion is
// architecture-defined in Go.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/danieljustus/symaira-desktop/internal/draw/fonts"
	"github.com/danieljustus/symaira-desktop/internal/draw/ir"
	"github.com/danieljustus/symaira-desktop/internal/draw/measure"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

const (
	defaultOutput = "testdata/port/render/text-measure.json"
	minedMinRunes = 2
	minedMaxRunes = 100
	sweepChunk    = 2048
	sweepSize     = 16.0
)

// minedSources are Go test files whose string literals (labels, sample text)
// are replayed through the measurement entry points.
var minedSources = []string{
	"internal/draw/measure/measure_test.go",
	"internal/draw/draw_test.go",
	"internal/draw/golden_test.go",
	"internal/draw/sequence_pipeline_test.go",
	"internal/draw/layout/layout_test.go",
	"internal/draw/layout/sequence_test.go",
	"internal/draw/emit/emit_test.go",
}

type fixture struct {
	SchemaVersion int        `json:"schema_version"`
	Oracle        oracle     `json:"oracle"`
	Fonts         fontInfo   `json:"fonts"`
	Entries       []string   `json:"entries"`
	Cases         []testCase `json:"cases"`
}

type oracle struct {
	Commit  string `json:"commit"`
	Release string `json:"release"`
}

type fontInfo struct {
	MeasurerHash string `json:"measurer_hash"`
	VersionKey   string `json:"version_key"`
}

type nodeOptions struct {
	FontSize    float64 `json:"font_size"`
	FontWeight  string  `json:"font_weight"`
	PaddingX    float64 `json:"padding_x"`
	PaddingY    float64 `json:"padding_y"`
	MinWidth    float64 `json:"min_width"`
	MinHeight   float64 `json:"min_height"`
	MaxWidth    float64 `json:"max_width"`
	LineSpacing float64 `json:"line_spacing"`
}

type testCase struct {
	ID       string       `json:"id"`
	Entry    string       `json:"entry"`
	Text     string       `json:"text,omitempty"`
	Weight   string       `json:"weight,omitempty"`
	Size     float64      `json:"size"`
	MaxWidth float64      `json:"max_width,omitempty"`
	Shape    string       `json:"shape,omitempty"`
	Options  *nodeOptions `json:"options,omitempty"`
	Start    int          `json:"start,omitempty"`
	Count    int          `json:"count,omitempty"`
	Expected string       `json:"expected"`
}

type nodeResult struct {
	Width  float64  `json:"width"`
	Height float64  `json:"height"`
	Lines  []string `json:"lines"`
}

type sweepResult struct {
	AdvancesSHA256 string  `json:"advances_sha256"`
	Width          float64 `json:"width"`
}

func main() {
	output := flag.String("output", defaultOutput, "fixture path")
	check := flag.Bool("check", false, "fail if the fixture differs")
	commit := flag.String("oracle-commit", fixtureoracle.Defaults().Commit, "Go oracle commit")
	release := flag.String("oracle-release", fixtureoracle.Defaults().Release, "Go oracle release")
	flag.Parse()
	message, err := generate(options{output: *output, check: *check, commit: *commit, release: *release})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(message)
}

// options is the resolved command line of one generator run.
type options struct {
	output          string
	check           bool
	commit, release string
}

// generate builds the fixture and either verifies it against the committed
// file (check) or writes it. It returns the PASS line.
func generate(opts options) (string, error) {
	root, err := fixtureoracle.FindRepositoryRoot(".")
	if err != nil {
		return "", fmt.Errorf("resolve fixture source: %w", err)
	}
	if err := fixtureoracle.ValidateSource(root, inventory.Oracle{Commit: opts.commit, Release: opts.release}); err != nil {
		return "", fmt.Errorf("verify selected fixture source: %w", err)
	}
	m, err := measure.NewMeasurer()
	if err != nil {
		return "", fmt.Errorf("load embedded measurer: %w", err)
	}

	b := &builder{m: m, seen: map[string]bool{}, ids: map[string]bool{}}
	b.handCorpus()
	if err := b.minedCorpus(root); err != nil {
		return "", err
	}
	if err := b.circleCorpus(); err != nil {
		return "", err
	}
	b.sweepCorpus()
	if b.err != nil {
		return "", b.err
	}

	content, err := render(fixture{
		SchemaVersion: 1,
		Oracle:        oracle{Commit: opts.commit, Release: opts.release},
		Fonts:         fontInfo{MeasurerHash: m.FontHash(), VersionKey: fonts.VersionKey()},
		Entries:       []string{"measure_text", "measure_text_detailed", "break_lines", "measure_node", "normalize_weight", "advance_sweep"},
		Cases:         b.cases,
	})
	if err != nil {
		return "", err
	}
	if opts.check {
		current, err := os.ReadFile(opts.output) // #nosec G304 -- caller-selected fixture path
		if err != nil || !bytes.Equal(current, content) {
			return "", errors.New("render text measure fixture is stale; regenerate deliberately")
		}
		return fmt.Sprintf("PASS render text measure fixture (%d cases)", len(b.cases)), nil
	}
	if err := os.MkdirAll(filepath.Dir(opts.output), 0o750); err != nil {
		return "", fmt.Errorf("create directory: %w", err)
	}
	if err := os.WriteFile(opts.output, content, 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", opts.output, err)
	}
	return fmt.Sprintf("PASS render text measure fixture generated (%d cases)", len(b.cases)), nil
}

// render serializes the fixture with LF line endings, one compact case per
// line, so the committed bytes do not depend on the host.
func render(f fixture) ([]byte, error) {
	var out bytes.Buffer
	cases := f.Cases
	f.Cases = nil
	head, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal fixture header: %w", err)
	}
	// Replace the trailing `"cases": null` member produced for the nil slice.
	suffix := []byte("\n}")
	idx := bytes.LastIndex(head, []byte(",\n  \"cases\": null"))
	if idx < 0 || !bytes.HasSuffix(head, suffix) {
		return nil, errors.New("unexpected fixture header shape")
	}
	out.Write(head[:idx])
	out.WriteString(",\n  \"cases\": [\n")
	for i, c := range cases {
		encoded, err := json.Marshal(c)
		if err != nil {
			return nil, fmt.Errorf("marshal case %s: %w", c.ID, err)
		}
		out.WriteString("    ")
		out.Write(encoded)
		if i < len(cases)-1 {
			out.WriteByte(',')
		}
		out.WriteByte('\n')
	}
	out.WriteString("  ]\n}\n")
	return out.Bytes(), nil
}

// builder accumulates deduplicated cases. The first error sticks.
type builder struct {
	m     *measure.Measurer
	cases []testCase
	seen  map[string]bool
	ids   map[string]bool
	err   error
}

func (b *builder) fail(format string, args ...any) {
	if b.err == nil {
		b.err = fmt.Errorf(format, args...)
	}
}

func (b *builder) add(c testCase) {
	key, err := json.Marshal(struct {
		E, T, W string
		S, M    float64
		H       string
		O       *nodeOptions
		A, N    int
	}{c.Entry, c.Text, c.Weight, c.Size, c.MaxWidth, c.Shape, c.Options, c.Start, c.Count})
	if err != nil {
		b.fail("case key %s: %v", c.ID, err)
		return
	}
	if b.seen[string(key)] {
		return
	}
	b.seen[string(key)] = true
	if b.ids[c.ID] {
		b.fail("duplicate case id %s", c.ID)
		return
	}
	b.ids[c.ID] = true
	if !utf8.ValidString(c.Text) {
		b.fail("%s: text is not valid UTF-8", c.ID)
		return
	}
	b.cases = append(b.cases, c)
}

func marshal(b *builder, id string, v any) string {
	encoded, err := json.Marshal(v)
	if err != nil {
		b.fail("%s: marshal Go oracle result: %v", id, err)
		return ""
	}
	return string(encoded)
}

func (b *builder) measureText(id, text string, size float64, weight string) {
	w, h := b.m.MeasureText(text, size, measure.FontWeight(weight))
	b.add(testCase{ID: id, Entry: "measure_text", Text: text, Weight: weight, Size: size, Expected: marshal(b, id, []float64{w, h})})
}

func (b *builder) measureDetailed(id, text string, size float64, weight string) {
	metrics, err := b.m.MeasureTextDetailed(text, size, measure.FontWeight(weight))
	if err != nil {
		b.fail("%s: MeasureTextDetailed: %v", id, err)
		return
	}
	b.add(testCase{ID: id, Entry: "measure_text_detailed", Text: text, Weight: weight, Size: size, Expected: marshal(b, id, metrics)})
}

func (b *builder) breakLines(id, text string, maxWidth, size float64, weight string) {
	res := b.m.BreakLines(text, maxWidth, size, measure.FontWeight(weight))
	b.add(testCase{ID: id, Entry: "break_lines", Text: text, Weight: weight, Size: size, MaxWidth: maxWidth, Expected: marshal(b, id, res)})
}

func (b *builder) measureNode(id, label, shape string, o nodeOptions) {
	opts := measure.NodeMeasureOptions{
		FontSize: o.FontSize, FontWeight: measure.FontWeight(o.FontWeight),
		PaddingX: o.PaddingX, PaddingY: o.PaddingY,
		MinWidth: o.MinWidth, MinHeight: o.MinHeight,
		MaxWidth: o.MaxWidth, LineSpacing: o.LineSpacing,
	}
	w, h, lines := b.m.MeasureNode(label, ir.NodeShape(shape), opts)
	oc := o
	b.add(testCase{ID: id, Entry: "measure_node", Text: label, Shape: shape, Options: &oc, Expected: marshal(b, id, nodeResult{Width: w, Height: h, Lines: lines})})
}

func (b *builder) normalize(id, input string) {
	b.add(testCase{ID: id, Entry: "normalize_weight", Text: input, Expected: marshal(b, id, string(measure.NormalizeFontWeight(input)))})
}

// measureTexts are single-line samples: ASCII, narrow/wide glyphs, accents,
// scripts the font covers and does not cover, combining marks, controls,
// private-use and supplementary code points.
func measureTexts() []string {
	return []string{
		"", "A", " ", "Ag", "Hello, World!", "iiiiii", "WWWWWW", "mmmmmmmm", "1234567890", "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~",
		"The quick brown fox jumps over the lazy dog",
		"Ünïcödé àéîõü ß ÆØÅ ñ", "Ǆ ǅ Ǉ ǈ ǋ ǌ", "ﬁ ﬂ ligatures fi fl", "fi fl",
		"Привет, мир! Это тест", "αβγδ ΑΒΓΔ Ωμέγα", "Ελληνικά", "ᚠᚢᚦᚨᚱᚲ", "ԱԲԳ",
		"日本語テキスト", "한국어 텍스트", "中文文本", "العربية", "עברית", "हिन्दी", "ไทย",
		"😀👍🏽🇩🇪", "👨‍👩‍👧‍👦", "e\u0301 o\u0308 a\u030a", "\u200b\u200c\u200d\ufeff",
		"\t", "\n", "a\tb", "line1\nline2", "\r\n", "\x00", "\x01\x1f\x7f", "\u0080\u009f",
		"\u00a0nbsp\u00a0", "\u2003\u2009\u200a", "\u2028\u2029", "\ufffd", "\ufffe\uffff",
		"\ue000\ue001", "\U0001f600", "\U0001d400\U0001d401", "\U0010ffff", "\U00010000",
		"∑∫√∞≠≤≥≈", "→←↑↓↔⇒", "─│┌┐└┘", "█▓▒░", "★☆♠♣♥♦", "€£¥¢₹₽",
		"½¼¾ ²³ ºª", "ĀāĂăĄą", "ȀȁȂȃ", "ẞ ẛ", "İıŞş", "Ǻ ǻ",
		strings.Repeat("Lorem ipsum dolor sit amet, ", 18),
		strings.Repeat("W", 300), strings.Repeat("i", 300), strings.Repeat("日", 120),
	}
}

func (b *builder) handCorpus() {
	texts := measureTexts()
	allSizes := []float64{0, 0.5, 1, 8, 10, 12, 13.5, 14, 16, 20, 24, 32, 48, 72, 100, 333.33, 1000, -14, -0.4}
	coreSizes := []float64{0, 1, 10, 14, 24, 72, -14}
	weights := []string{"regular", "bold"}

	for ti, text := range texts {
		for _, size := range coreSizes {
			for _, w := range weights {
				b.measureText(fmt.Sprintf("mt-%03d-%s-%s", ti, sizeSlug(size), w), text, size, w)
			}
		}
	}
	for si, size := range allSizes {
		for wi, w := range []string{"regular", "bold", "Bold", "BOLD", "700", "", "semibold", "bolder", " bold"} {
			b.measureText(fmt.Sprintf("mt-weights-%02d-%d", si, wi), "Hamburgefonts 0123 ÄÖÜ", size, w)
		}
	}

	detailedSizes := []float64{0, 1, 14, 100, -14}
	for ti, text := range texts {
		if len([]rune(text)) > 64 {
			continue
		}
		for _, size := range detailedSizes {
			for _, w := range weights {
				b.measureDetailed(fmt.Sprintf("md-%03d-%s-%s", ti, sizeSlug(size), w), text, size, w)
			}
		}
	}
	for si, size := range allSizes {
		b.measureDetailed(fmt.Sprintf("md-sizes-%02d", si), "Agxyz HQ", size, "regular")
		b.measureDetailed(fmt.Sprintf("md-sizes-bold-%02d", si), "Agxyz HQ", size, "bold")
	}

	breakTexts := []string{
		"", "a", " ", "\n", "\n\n", "  \n  ", "Hello World", "The quick brown fox jumps over the lazy dog",
		"Supercalifragilisticexpialidocious word",
		"First paragraph here\nSecond paragraph is a bit longer than the first\n\nFourth after empty",
		"tabs\tand\tspaces   multiple", "trailing   ", "   leading", "CRLF line one\r\nCRLF line two",
		"nbsp\u00a0joined\u00a0words and\u2003em\u2003space", "\u0085next-line\u0085char",
		"long url https://example.com/a/very/long/path/that/cannot/be/broken/anywhere/at/all",
		"日本語のテキストは空白なしで続きます", "Привет мир это длинный русский текст для переноса",
		"emoji 😀 words 👍🏽 here", "e\u0301 e\u0301 e\u0301", "<b>&amp;</b> html & chars",
		"quote \"x\" \\ back", "ctrl\x01\x02 chars \x00 nul", "\u2028 line sep \u2029 para sep",
		strings.Repeat("alpha beta gamma delta ", 12),
		"A B C D E F G H I J K L M N O P Q R S T U V W X Y Z",
		"single", "two words", "  two   spaced   words  ", "\ttab-led", "end\n", "\nstart",
		"ΑΒΓ ΔΕΖ ΗΘΙ", "ﬁne ﬂow", "x\u200by\u200bz zero width",
	}
	widths := []float64{-5, 0, 0.5, 1, 40, 80, 120, 240, 500, 1e6}
	type face struct {
		size   float64
		weight string
	}
	faces := []face{{14, "regular"}, {14, "bold"}, {20, "regular"}}
	for ti, text := range breakTexts {
		for _, width := range widths {
			for _, f := range faces {
				b.breakLines(fmt.Sprintf("bl-%03d-%s-%s-%s", ti, sizeSlug(width), sizeSlug(f.size), f.weight), text, width, f.size, f.weight)
			}
		}
	}
	// A candidate line that is exactly as wide as the limit still fits
	// (`candW <= maxWidth`): probe the boundary at width-1/64, width, width+1/64.
	exactTexts := []string{"alpha beta gamma delta epsilon", "WWW iii mmm lll", "Überlänge Wörter für Tests", "a b c d e f g h"}
	for ti, text := range exactTexts {
		words := strings.Fields(text)
		for _, f := range faces {
			for n := 2; n <= len(words); n++ {
				w, _ := b.m.MeasureText(strings.Join(words[:n], " "), f.size, measure.FontWeight(f.weight))
				for di, delta := range []float64{-1.0 / 64, 0, 1.0 / 64} {
					b.breakLines(fmt.Sprintf("bl-exact-%d-%d-%s-%d-%d", ti, n, f.weight, int(f.size), di), text, w+delta, f.size, f.weight)
				}
			}
		}
	}
	sweepTexts := []string{"The quick brown fox jumps over the lazy dog", "one two three\nfour five six seven eight", "Überlänge-Wort und noch mehr Text"}
	for ti, text := range sweepTexts {
		for si, size := range []float64{0, 1, 8, 10, 14, 20, 33.3, 72, -14, -3.5} {
			for _, w := range []string{"regular", "bold", "Bold"} {
				b.breakLines(fmt.Sprintf("bl-sizes-%d-%02d-%s", ti, si, w), text, 120, size, w)
			}
		}
	}

	labels := []string{
		"", "A", "API Gateway", "Database", "A much longer node label that has to wrap across several lines",
		"Line one\nLine two\n\nLine four", "日本語ラベル", "Wide WWWWWW and narrow iiiiii", "<&>\"'",
	}
	shapes := []string{"rect", "round", "cylinder", "diamond", "pill", "stadium", "subroutine", "hexagon", "asymmetric", "", "unknown", "CIRCLE", "circle ", "Diamond"}
	optionSets := []nodeOptions{
		{FontSize: 14, FontWeight: "regular", PaddingX: 20, PaddingY: 14, MinWidth: 80, MinHeight: 40, MaxWidth: 240, LineSpacing: 4},
		{},
		{FontSize: 18, FontWeight: "bold", PaddingX: 12, PaddingY: 6, MinWidth: 60, MinHeight: 30, MaxWidth: 150, LineSpacing: 2},
		{FontSize: 10, FontWeight: "regular", PaddingX: 5.5, PaddingY: 3.25, MinWidth: 0, MinHeight: 0, MaxWidth: 90, LineSpacing: 0},
		{FontSize: -3, FontWeight: "regular", PaddingX: -1, PaddingY: -1, MinWidth: 300, MinHeight: 200, MaxWidth: 240, LineSpacing: 4},
		{FontSize: 14.5, FontWeight: "Bold", PaddingX: 20.3, PaddingY: 14.7, MinWidth: 80, MinHeight: 40, MaxWidth: 60, LineSpacing: 4},
		{FontSize: 16, FontWeight: "bold", PaddingX: 8, PaddingY: 8, MinWidth: 10, MinHeight: 10, MaxWidth: -1, LineSpacing: 4},
	}
	for li, label := range labels {
		for si, shape := range shapes {
			for oi, o := range optionSets {
				b.measureNode(fmt.Sprintf("mn-%02d-%02d-%d", li, si, oi), label, shape, o)
			}
		}
	}

	for i, in := range []string{
		"bold", "BOLD", " Bold ", "700", "800", "900", "bolder", "Bolder", "\u00a0bold\u2003", "\t700\n", "boldest", "bold 700",
		"regular", "normal", "400", "600", "lighter", "semibold", "", " ", "İ", "ΒΟΛD", "bol\u212a", "7\u200b00", "+700", "0700", "bold\x00",
	} {
		b.normalize(fmt.Sprintf("nw-%02d", i), in)
	}
}

func sizeSlug(v float64) string {
	return strings.NewReplacer(".", "_", "-", "m", "+", "p").Replace(strconv.FormatFloat(v, 'g', -1, 64))
}

// minedCorpus feeds every short string literal from the Go draw tests through
// the measurement entry points.
func (b *builder) minedCorpus(root string) error {
	dedupe := map[string]bool{}
	for _, rel := range minedSources {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, filepath.Join(root, filepath.FromSlash(rel)), nil, 0)
		if err != nil {
			return fmt.Errorf("parse %s: %w", rel, err)
		}
		base := strings.TrimSuffix(filepath.Base(rel), "_test.go")
		n := 0
		ast.Inspect(file, func(node ast.Node) bool {
			lit, ok := node.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(lit.Value)
			runes := utf8.RuneCountInString(value)
			if err != nil || !utf8.ValidString(value) || runes < minedMinRunes || runes > minedMaxRunes || dedupe[value] {
				return true
			}
			dedupe[value] = true
			id := fmt.Sprintf("mined-%s-%03d", base, n)
			n++
			b.measureDetailed(id+"-md", value, 14, "regular")
			b.breakLines(id+"-bl", value, 120, 14, "regular")
			b.measureNode(id+"-mn", value, "rect", nodeOptions{FontSize: 14, FontWeight: "regular", PaddingX: 20, PaddingY: 14, MinWidth: 80, MinHeight: 40, MaxWidth: 240, LineSpacing: 4})
			return true
		})
	}
	return nil
}

// canonicalHypot is Go's portable math.hypot with every product rounded
// separately (explicit conversions forbid fused multiply-add).
func canonicalHypot(p, q float64) float64 {
	p, q = math.Abs(p), math.Abs(q)
	if p < q {
		p, q = q, p
	}
	if p == 0 {
		return 0
	}
	q /= p
	return p * math.Sqrt(1+float64(q*q))
}

// circleCorpus freezes circle node sizes only where padding is solved so the
// raw box is a Pythagorean triple: the hypotenuse is then exact on every
// architecture, whatever math.Hypot's last-bit behaviour is.
func (b *builder) circleCorpus() error {
	triples := [][3]float64{{3, 4, 5}, {5, 12, 13}, {8, 15, 17}, {20, 21, 29}}
	labels := []string{"Hub", "OK", "Gateway node", "Ünï 日本", "A\nB"}
	for li, label := range labels {
		for ti, t := range triples {
			for _, k := range []float64{60, 100} {
				for flip := 0; flip < 2; flip++ {
					rawW, rawH, hyp := t[0]*k, t[1]*k, t[2]*k
					if flip == 1 {
						rawW, rawH = rawH, rawW
					}
					for _, weight := range []string{"regular", "bold"} {
						const size = 14.0
						lb := b.m.BreakLines(label, 0, size, measure.FontWeight(weight))
						padX := (rawW - lb.MaxLineWidth) / 2
						padY := (rawH - lb.TotalHeight) / 2
						if padX <= 0 || padY <= 0 || lb.MaxLineWidth+padX*2 != rawW || lb.TotalHeight+padY*2 != rawH {
							return fmt.Errorf("circle %d/%d/%v: padding does not reproduce the %vx%v box", li, ti, k, rawW, rawH)
						}
						o := nodeOptions{FontSize: size, FontWeight: weight, PaddingX: padX, PaddingY: padY, MinWidth: 80, MinHeight: 40, MaxWidth: 0, LineSpacing: 4}
						if got := math.Hypot(rawW, rawH); got != hyp || canonicalHypot(rawW, rawH) != hyp {
							return fmt.Errorf("circle %d/%d/%v: hypot(%v,%v) = %v (portable %v), want exactly %v on every host", li, ti, k, rawW, rawH, got, canonicalHypot(rawW, rawH), hyp)
						}
						b.measureNode(fmt.Sprintf("mn-circle-%d-%d-%v-%d-%s", li, ti, k, flip, weight), label, "circle", o)
					}
				}
			}
		}
	}
	// MinWidth above the diagonal wins.
	b.measureNode("mn-circle-minwidth", "", "circle", nodeOptions{FontSize: 14, FontWeight: "regular", PaddingX: 150, PaddingY: 200, MinWidth: 5000, MinHeight: 40})
	return nil
}

// sweepCorpus covers every Unicode scalar value: per chunk of consecutive
// code points, the SHA-256 of the JSON per-glyph advances and the total width.
func (b *builder) sweepCorpus() {
	for start := 0; start < 0x110000; start += sweepChunk {
		if start >= 0xD800 && start < 0xE000 {
			continue
		}
		var sb strings.Builder
		for r := start; r < start+sweepChunk; r++ {
			sb.WriteRune(rune(r))
		}
		for _, weight := range []string{"regular", "bold"} {
			id := fmt.Sprintf("sweep-%06x-%s", start, weight)
			metrics, err := b.m.MeasureTextDetailed(sb.String(), sweepSize, measure.FontWeight(weight))
			if err != nil {
				b.fail("%s: %v", id, err)
				return
			}
			advances, err := json.Marshal(metrics.GlyphAdvances)
			if err != nil {
				b.fail("%s: %v", id, err)
				return
			}
			digest := sha256.Sum256(advances)
			b.add(testCase{
				ID: id, Entry: "advance_sweep", Weight: weight, Size: sweepSize, Start: start, Count: sweepChunk,
				Expected: marshal(b, id, sweepResult{AdvancesSHA256: hex.EncodeToString(digest[:]), Width: metrics.Width}),
			})
		}
	}
}
