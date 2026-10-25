// Command renderscenegen freezes the production Go SymDraw scene and theme
// contract (internal/draw/scene and internal/draw/theme) as a Rust replay
// fixture: theme resolution and registry, Rect algebra, every primitive's
// Bounds and json.Marshal bytes, Scene.ComputeBounds/FitToBounds/Validate
// (including the exact error text) and DefaultMarkers.
//
// Every case carries its input as plain JSON (numbers, plus the strings
// "NaN", "+Inf", "-Inf" and "-0" for values JSON cannot spell) and one
// expected text recorded from the production Go package. The oracle consumes
// the input bytes exactly as the Rust replay does: each spec is marshalled,
// decoded again and only then turned into Go structs.
//
// Host independence: the corpus contains no paths, no clock and no
// randomness beyond a fixed-seed PRNG. The only host-dependent arithmetic in
// the packages is the alphabetic-baseline text offset `t.Y - fontSize*0.95`,
// which Go may fuse into one FMA on arm64 but not on amd64. Cases whose result
// differs between the fused and the unfused evaluation are not recorded
// (counted in the header), so the fixture is identical on every host.
//
// The theme registry is process-global in Go and Register mutates it, so the
// Register/Resolve/Names scripts run in a helper process of this binary; the
// parent process only ever sees the pristine registry.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/danieljustus/symaira-desktop/internal/draw/measure"
	"github.com/danieljustus/symaira-desktop/internal/draw/scene"
	"github.com/danieljustus/symaira-desktop/internal/draw/theme"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

const (
	defaultOutput = "testdata/port/render/scene-bounds.json"
	helperEnv     = "RENDERSCENEGEN_REGISTRY_HELPER"
	prngSeed      = 20261025
)

type fixture struct {
	SchemaVersion       int        `json:"schema_version"`
	Oracle              oracle     `json:"oracle"`
	Entries             []string   `json:"entries"`
	FMAAmbiguousSkipped int        `json:"fma_ambiguous_skipped"`
	Cases               []testCase `json:"cases"`
}

type oracle struct {
	Commit  string `json:"commit"`
	Release string `json:"release"`
}

type testCase struct {
	ID       string          `json:"id"`
	Entry    string          `json:"entry"`
	Input    json.RawMessage `json:"input"`
	Expected string          `json:"expected"`
}

type spec = map[string]any

func main() {
	if os.Getenv(helperEnv) != "" {
		if err := runRegistryHelper(os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
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

	b := &builder{rng: rand.New(rand.NewSource(prngSeed))} // #nosec G404 -- fixed-seed corpus generation, not security
	b.themeResolveCorpus()
	b.rectCorpus()
	b.primitiveCorpus()
	b.sceneCorpus()
	b.markerCorpus()
	if b.err != nil {
		return "", b.err
	}
	helper, err := registryHelperCases()
	if err != nil {
		return "", err
	}
	b.cases = append(b.cases, helper...)
	if err := verifyOrdering(b.cases); err != nil {
		return "", err
	}

	content, err := render(fixture{
		SchemaVersion:       1,
		Oracle:              oracle{Commit: opts.commit, Release: opts.release},
		Entries:             []string{"theme_resolve", "rect_op", "primitive", "scene", "default_markers", "theme_registry"},
		FMAAmbiguousSkipped: b.skipped,
		Cases:               b.cases,
	})
	if err != nil {
		return "", err
	}
	if opts.check {
		current, err := os.ReadFile(opts.output) // #nosec G304 -- caller-selected fixture path
		if err != nil || !bytes.Equal(current, content) {
			return "", errors.New("render scene fixture is stale; regenerate deliberately")
		}
		return fmt.Sprintf("PASS render scene fixture (%d cases)", len(b.cases)), nil
	}
	if err := os.MkdirAll(filepath.Dir(opts.output), 0o750); err != nil {
		return "", fmt.Errorf("create directory: %w", err)
	}
	if err := os.WriteFile(opts.output, content, 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", opts.output, err)
	}
	return fmt.Sprintf("PASS render scene fixture generated (%d cases)", len(b.cases)), nil
}

// verifyOrdering enforces the replay contract: the mutating registry scripts
// come last, so every other case saw the pristine built-in registry.
func verifyOrdering(cases []testCase) error {
	seenRegistry := false
	ids := map[string]bool{}
	for _, c := range cases {
		if ids[c.ID] {
			return fmt.Errorf("duplicate case id %s", c.ID)
		}
		ids[c.ID] = true
		if c.Entry == "theme_registry" {
			seenRegistry = true
		} else if seenRegistry {
			return fmt.Errorf("case %s follows a registry-mutating case", c.ID)
		}
	}
	return nil
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
	idx := bytes.LastIndex(head, []byte(",\n  \"cases\": null"))
	if idx < 0 || !bytes.HasSuffix(head, []byte("\n}")) {
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

// builder accumulates cases. The first error sticks.
type builder struct {
	cases   []testCase
	err     error
	rng     *rand.Rand
	skipped int
	n       int
}

func (b *builder) fail(format string, args ...any) {
	if b.err == nil {
		b.err = fmt.Errorf(format, args...)
	}
}

// add records one case. The oracle runs on the decoded copy of the input.
func (b *builder) add(prefix, entry string, input spec, run func(any) (string, error)) {
	if b.err != nil {
		return
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		b.fail("%s: marshal input: %v", prefix, err)
		return
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		b.fail("%s: decode input: %v", prefix, err)
		return
	}
	expected, err := run(decoded)
	if errors.Is(err, errSkip) {
		return
	}
	if err != nil {
		b.fail("%s: %v", prefix, err)
		return
	}
	b.n++
	b.cases = append(b.cases, testCase{ID: fmt.Sprintf("%s-%04d", prefix, b.n), Entry: entry, Input: encoded, Expected: expected})
}

// goJSON is json.Marshal with the error text kept: "!<error>" on failure.
func goJSON(v any) string {
	encoded, err := json.Marshal(v)
	if err != nil {
		return "!" + err.Error()
	}
	return string(encoded)
}

// ---------------------------------------------------------------- spec decode

// num decodes a spec number: a JSON number or one of the spelled specials.
// Finite numbers must survive a 15-digit round trip so any conforming JSON
// parser reads the same float64.
func num(v any) (float64, error) {
	switch x := v.(type) {
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return 0, fmt.Errorf("non-finite number %v in a JSON number", x)
		}
		back, err := strconv.ParseFloat(strconv.FormatFloat(x, 'g', 15, 64), 64)
		if err != nil || back != x {
			return 0, fmt.Errorf("spec number %v needs more than 15 significant digits", x)
		}
		return x, nil
	case string:
		switch x {
		case "NaN":
			return math.NaN(), nil
		case "+Inf":
			return math.Inf(1), nil
		case "-Inf":
			return math.Inf(-1), nil
		case "-0":
			return math.Copysign(0, -1), nil
		}
	}
	return 0, fmt.Errorf("unsupported spec number %v (%T)", v, v)
}

func asMap(v any) (map[string]any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected object, got %T", v)
	}
	return m, nil
}

func asList(v any) ([]any, error) {
	l, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("expected array, got %T", v)
	}
	return l, nil
}

func decodeRect(v any) (scene.Rect, error) {
	m, err := asMap(v)
	if err != nil {
		return scene.Rect{}, err
	}
	var r scene.Rect
	if err := fill(&r, m); err != nil {
		return scene.Rect{}, err
	}
	return r, nil
}

func decodePoints(v any) ([]measure.Point, error) {
	if v == nil {
		return nil, nil
	}
	list, err := asList(v)
	if err != nil {
		return nil, err
	}
	points := make([]measure.Point, 0, len(list))
	for _, item := range list {
		m, err := asMap(item)
		if err != nil {
			return nil, err
		}
		var p measure.Point
		if err := fill(&p, m); err != nil {
			return nil, err
		}
		points = append(points, p)
	}
	return points, nil
}

func decodeSegments(v any) ([]measure.PathSegment, error) {
	if v == nil {
		return nil, nil
	}
	list, err := asList(v)
	if err != nil {
		return nil, err
	}
	segments := make([]measure.PathSegment, 0, len(list))
	for _, item := range list {
		m, err := asMap(item)
		if err != nil {
			return nil, err
		}
		var s measure.PathSegment
		rest := copyMap(m)
		if args, ok := rest["args"]; ok {
			delete(rest, "args")
			if s.Args, err = decodePoints(args); err != nil {
				return nil, err
			}
		}
		if err := fill(&s, rest); err != nil {
			return nil, err
		}
		segments = append(segments, s)
	}
	return segments, nil
}

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// fill sets the exported fields of the struct behind ptr from m, matching the
// json tag names. Only string, float64 and bool fields are handled here;
// callers decode slices first and remove their keys. An unknown key is an
// error so a typo in the corpus cannot silently test nothing.
func fill(ptr any, m map[string]any) error {
	v := reflect.ValueOf(ptr).Elem()
	t := v.Type()
	used := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		raw, ok := m[name]
		if !ok {
			continue
		}
		used[name] = true
		switch f.Type.Kind() {
		case reflect.String:
			s, ok := raw.(string)
			if !ok {
				return fmt.Errorf("field %s: expected string, got %T", name, raw)
			}
			v.Field(i).SetString(s)
		case reflect.Float64:
			n, err := num(raw)
			if err != nil {
				return fmt.Errorf("field %s: %w", name, err)
			}
			v.Field(i).SetFloat(n)
		case reflect.Bool:
			bv, ok := raw.(bool)
			if !ok {
				return fmt.Errorf("field %s: expected bool, got %T", name, raw)
			}
			v.Field(i).SetBool(bv)
		default:
			return fmt.Errorf("field %s: unsupported kind %s", name, f.Type.Kind())
		}
	}
	for k := range m {
		if !used[k] {
			return fmt.Errorf("unknown spec key %q for %s", k, t.Name())
		}
	}
	return nil
}

func decodeStringMap(v any) (map[string]string, error) {
	m, err := asMap(v)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(m))
	for k, raw := range m {
		s, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("metadata %q: expected string", k)
		}
		out[k] = s
	}
	return out, nil
}

func decodeTheme(v any) (theme.Theme, error) {
	m, err := asMap(v)
	if err != nil {
		return theme.Theme{}, err
	}
	if name, ok := m["resolve"]; ok {
		s, ok := name.(string)
		if !ok || len(m) != 1 {
			return theme.Theme{}, errors.New("theme resolve spec must be {\"resolve\": name}")
		}
		return theme.Resolve(s), nil
	}
	rest := copyMap(m)
	var th theme.Theme
	if p, ok := rest["palette"]; ok {
		delete(rest, "palette")
		if p != nil {
			list, err := asList(p)
			if err != nil {
				return th, err
			}
			th.Palette = make([]string, 0, len(list))
			for _, item := range list {
				s, ok := item.(string)
				if !ok {
					return th, errors.New("palette entry is not a string")
				}
				th.Palette = append(th.Palette, s)
			}
		}
	}
	if err := fill(&th, rest); err != nil {
		return th, err
	}
	return th, nil
}

// decodePrimitive builds the Go primitive for a spec; nil for JSON null.
func decodePrimitive(v any) (scene.Primitive, error) {
	if v == nil {
		return nil, nil
	}
	m, err := asMap(v)
	if err != nil {
		return nil, err
	}
	typ, _ := m["type"].(string)
	rest := copyMap(m)
	delete(rest, "type")
	switch typ {
	case "rect":
		p := &scene.RectElement{}
		return p, fill(p, rest)
	case "circle":
		p := &scene.CircleElement{}
		return p, fill(p, rest)
	case "ellipse":
		p := &scene.EllipseElement{}
		return p, fill(p, rest)
	case "line":
		p := &scene.LineElement{}
		return p, fill(p, rest)
	case "polyline":
		p := &scene.PolylineElement{}
		if pts, ok := rest["points"]; ok {
			delete(rest, "points")
			if p.Points, err = decodePoints(pts); err != nil {
				return nil, err
			}
		}
		return p, fill(p, rest)
	case "polygon":
		p := &scene.PolygonElement{}
		if pts, ok := rest["points"]; ok {
			delete(rest, "points")
			if p.Points, err = decodePoints(pts); err != nil {
				return nil, err
			}
		}
		return p, fill(p, rest)
	case "path":
		p := &scene.PathElement{}
		if segs, ok := rest["segments"]; ok {
			delete(rest, "segments")
			if p.Segments, err = decodeSegments(segs); err != nil {
				return nil, err
			}
		}
		if hint, ok := rest["bounds_hint"]; ok {
			delete(rest, "bounds_hint")
			r, err := decodeRect(hint)
			if err != nil {
				return nil, err
			}
			p.SetBoundsHint(r)
		}
		return p, fill(p, rest)
	case "text":
		p := &scene.TextElement{}
		return p, fill(p, rest)
	case "group":
		p := &scene.GroupElement{}
		if kids, ok := rest["children"]; ok {
			delete(rest, "children")
			if kids != nil {
				list, err := asList(kids)
				if err != nil {
					return nil, err
				}
				p.Children = make([]scene.Primitive, 0, len(list))
				for _, item := range list {
					child, err := decodePrimitive(item)
					if err != nil {
						return nil, err
					}
					if child == nil {
						return nil, errors.New("a nil group child would panic Group.Bounds")
					}
					p.Children = append(p.Children, child)
				}
			}
		}
		return p, fill(p, rest)
	}
	return nil, fmt.Errorf("unknown primitive type %q", typ)
}

func decodeMarker(v any) (scene.MarkerDef, error) {
	m, err := asMap(v)
	if err != nil {
		return scene.MarkerDef{}, err
	}
	rest := copyMap(m)
	var def scene.MarkerDef
	if vb, ok := rest["view_box"]; ok {
		delete(rest, "view_box")
		if def.ViewBox, err = decodeRect(vb); err != nil {
			return def, err
		}
	}
	if path, ok := rest["path"]; ok {
		delete(rest, "path")
		if def.Path, err = decodePrimitive(path); err != nil {
			return def, err
		}
	}
	return def, fill(&def, rest)
}

// ------------------------------------------------------- fused-multiply guard

// fmaAmbiguous reports whether the alphabetic-baseline offset of a text
// primitive (directly or inside a group) evaluates differently when Go fuses
// `t.Y - fontSize*0.95` into one FMA (arm64) than when it rounds the product
// first (amd64, Rust). Such cases would make the fixture host dependent.
func fmaAmbiguous(p scene.Primitive) bool {
	switch t := p.(type) {
	case *scene.TextElement:
		switch t.Baseline {
		case scene.BaselineMiddle, scene.BaselineCentral, scene.BaselineHanging, scene.BaselineTop:
			return false
		}
		fontSize := t.FontSize
		if fontSize <= 0 {
			fontSize = 14.0
		}
		product := fontSize * 0.95
		unfused := t.Y - float64(product)
		fused := math.FMA(-fontSize, 0.95, t.Y)
		if math.IsNaN(unfused) && math.IsNaN(fused) {
			return false
		}
		return math.Float64bits(unfused) != math.Float64bits(fused)
	case *scene.GroupElement:
		for _, c := range t.Children {
			if fmaAmbiguous(c) {
				return true
			}
		}
	}
	return false
}

// ------------------------------------------------------------------- corpora

var sceneThemes = []string{"symaira-dark", "symaira-light", "report", "meeting", "behoerde", "brief", "dark", "light", "unknown"}

func (b *builder) themeResolveCorpus() {
	names := []string{
		"", " ", "symaira-dark", "symaira-light", "report", "meeting", "behoerde", "brief",
		"dark", "light", "DARK", "Light", "  Report  ", "MEETING", "BehOerde", "BRIEF", "Symaira-Dark",
		"\tbrief\n", "\u00a0report", "\u2003meeting\u2003", "report\u00a0", "\u0085brief", "\u200breport",
		"symaira_dark", "symaira-dark ", "symaira dark", "darkmode", "nope", "unknown", "BEHÖRDE", "behörde",
		"İstanbul", "ǅ", "ΣΑΣ", "日本", "report\x00", "a", "z", strings.Repeat("x", 300),
	}
	for _, n := range names {
		name := n
		b.add("theme", "theme_resolve", spec{"name": name}, func(any) (string, error) {
			return goJSON(theme.Resolve(name)), nil
		})
	}
}

func (b *builder) rectCorpus() {
	rects := []spec{
		{"x": 0, "y": 0, "width": 0, "height": 0},
		{"x": 0, "y": 0, "width": 10, "height": 10},
		{"x": 5, "y": 5, "width": 10, "height": 10},
		{"x": 2, "y": 2, "width": 4, "height": 4},
		{"x": 20, "y": 20, "width": 5, "height": 5},
		{"x": -10, "y": -10, "width": 5, "height": 5},
		{"x": 0, "y": 0, "width": -5, "height": 10},
		{"x": 0, "y": 0, "width": 10, "height": 0},
		{"x": 0, "y": 0, "width": 0, "height": 10},
		{"x": -0.01, "y": -0.01, "width": 10.02, "height": 10.02},
		{"x": -0.02, "y": 0, "width": 10, "height": 10},
		{"x": 0, "y": 0.0100001, "width": 10, "height": 9.99},
		{"x": 0, "y": 0, "width": 10.01, "height": 10},
		{"x": 0, "y": 0, "width": 10.02, "height": 10},
		{"x": "-0", "y": "-0", "width": 1, "height": 1},
		{"x": 1.5, "y": 2.25, "width": 3.125, "height": 0.5},
		{"x": 100, "y": 100, "width": 0.001, "height": 0.001},
		{"x": "NaN", "y": 0, "width": 10, "height": 10},
		{"x": 0, "y": 0, "width": "NaN", "height": 10},
		{"x": 0, "y": 0, "width": 10, "height": "NaN"},
		{"x": 0, "y": "+Inf", "width": 10, "height": 10},
		{"x": 0, "y": 0, "width": "+Inf", "height": 10},
		{"x": "-Inf", "y": 0, "width": "+Inf", "height": 10},
	}
	paddings := []any{0, 1, -1, 2.5, 0.1, -3, "-0", "NaN", "+Inf"}
	for i, a := range rects {
		for j, c := range rects {
			pad := paddings[(i+j)%len(paddings)]
			b.add("rect", "rect_op", spec{"a": a, "b": c, "padding": pad}, func(in any) (string, error) {
				m, _ := asMap(in)
				ra, err := decodeRect(m["a"])
				if err != nil {
					return "", err
				}
				rb, err := decodeRect(m["b"])
				if err != nil {
					return "", err
				}
				padding, err := num(m["padding"])
				if err != nil {
					return "", err
				}
				lines := []string{
					"max_x=" + goJSON(ra.MaxX()),
					"max_y=" + goJSON(ra.MaxY()),
					"is_empty=" + goJSON(ra.IsEmpty()),
					"union=" + goJSON(ra.Union(rb)),
					"contains=" + goJSON(ra.Contains(rb)),
					"pad=" + goJSON(ra.Pad(padding)),
				}
				return strings.Join(lines, "\n"), nil
			})
		}
	}
}

func p(typ string, kv ...any) spec {
	m := spec{"type": typ}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

// recordPrimitive is the shared primitive oracle: type, id, bounds, bytes.
func (b *builder) recordPrimitive(prefix string, primitive spec) {
	b.add(prefix, "primitive", spec{"primitive": primitive}, func(in any) (string, error) {
		m, _ := asMap(in)
		prim, err := decodePrimitive(m["primitive"])
		if err != nil {
			return "", err
		}
		if fmaAmbiguous(prim) {
			b.skipped++
			return "", errSkip
		}
		return strings.Join([]string{
			"type=" + prim.Type(),
			"id=" + prim.ElementID(),
			"bounds=" + goJSON(prim.Bounds()),
			"json=" + goJSON(prim),
		}, "\n"), nil
	})
}

// errSkip drops a host-dependent case without failing the run.
var errSkip = errors.New("skip host-dependent case")

func (b *builder) primitiveCorpus() {
	coords := []any{0, 10.5, -3, "-0"}
	extents := []any{0, 40, 120.5, -10}
	strokes := []any{0, 1, 3.5, -2}

	for _, x := range coords {
		for _, y := range coords[:3] {
			for _, w := range extents {
				for _, h := range extents[:3] {
					for _, sw := range strokes {
						b.recordPrimitive("prim-rect", p("rect", "x", x, "y", y, "width", w, "height", h, "stroke_width", sw))
					}
				}
			}
		}
	}
	b.recordPrimitive("prim-rect-attrs", p("rect", "id", "r1", "class", "node box", "x", 1, "y", 2, "width", 30, "height", 20,
		"rx", 4, "ry", 2.5, "fill", "#E5C397", "stroke", "#000", "stroke_width", 2, "dash_array", "4 2", "opacity", 0.5, "link", "https://example.com/?a=1&b=<2>"))
	b.recordPrimitive("prim-rect-attrs", p("rect", "id", "\u00e4<&>\"'\u2028\u2029\u0001", "width", 5, "height", 5))
	b.recordPrimitive("prim-rect-attrs", p("rect", "x", 1, "y", 1, "width", 1, "height", 1, "opacity", "-0", "rx", "-0"))
	for _, special := range []any{"NaN", "+Inf", "-Inf"} {
		b.recordPrimitive("prim-rect-special", p("rect", "x", special, "width", 4, "height", 4))
		b.recordPrimitive("prim-rect-special", p("rect", "width", special, "height", 4))
		b.recordPrimitive("prim-rect-special", p("rect", "width", 4, "height", 4, "stroke_width", special))
		b.recordPrimitive("prim-rect-special", p("rect", "width", 4, "height", 4, "rx", special))
		b.recordPrimitive("prim-rect-special", p("rect", "width", 4, "height", 4, "opacity", special))
	}

	for _, cx := range coords {
		for _, cy := range coords[:3] {
			for _, r := range []any{0, 1, 7.5, 25, -4} {
				for _, sw := range strokes {
					b.recordPrimitive("prim-circle", p("circle", "cx", cx, "cy", cy, "r", r, "stroke_width", sw))
				}
			}
		}
	}
	b.recordPrimitive("prim-circle-attrs", p("circle", "id", "c1", "class", "dot", "cx", 5, "cy", 5, "r", 4, "fill", "#fff", "stroke", "#000", "stroke_width", 1, "opacity", 0.25, "link", "#x"))
	for _, special := range []any{"NaN", "+Inf", "-Inf"} {
		b.recordPrimitive("prim-circle-special", p("circle", "cx", special, "cy", 1, "r", 2))
		b.recordPrimitive("prim-circle-special", p("circle", "cx", 1, "cy", 1, "r", special))
	}

	for _, cx := range coords[:3] {
		for _, rx := range []any{0, 5, 12.5, -3} {
			for _, ry := range []any{0, 2, 30.25, -1} {
				for _, sw := range strokes[:3] {
					b.recordPrimitive("prim-ellipse", p("ellipse", "cx", cx, "cy", 4, "rx", rx, "ry", ry, "stroke_width", sw))
				}
			}
		}
	}
	b.recordPrimitive("prim-ellipse-attrs", p("ellipse", "id", "e1", "class", "oval", "cx", 3, "cy", 4, "rx", 5, "ry", 6, "fill", "none", "stroke", "red", "stroke_width", 2, "opacity", 1, "link", "x"))
	b.recordPrimitive("prim-ellipse-special", p("ellipse", "cx", "NaN", "cy", 0, "rx", 1, "ry", 1))
	b.recordPrimitive("prim-ellipse-special", p("ellipse", "cx", 0, "cy", 0, "rx", "+Inf", "ry", 1))

	for _, x1 := range coords {
		for _, y1 := range coords[:3] {
			for _, x2 := range []any{0, 25, -12.5} {
				for _, y2 := range []any{0, 8, -30} {
					for _, sw := range strokes[:3] {
						b.recordPrimitive("prim-line", p("line", "x1", x1, "y1", y1, "x2", x2, "y2", y2, "stroke_width", sw))
					}
				}
			}
		}
	}
	b.recordPrimitive("prim-line-attrs", p("line", "id", "l1", "class", "edge", "x1", 1, "y1", 2, "x2", 3, "y2", 4, "stroke", "#111", "stroke_width", 1.5, "dash_array", "3,3", "marker_start", "arrow-start", "marker_end", "arrow-end", "opacity", 0.75))
	b.recordPrimitive("prim-line-special", p("line", "x1", "NaN", "y1", 0, "x2", 5, "y2", 5))
	b.recordPrimitive("prim-line-special", p("line", "x1", 0, "y1", 0, "x2", "NaN", "y2", 5))
	b.recordPrimitive("prim-line-special", p("line", "x1", "-0", "y1", "-0", "x2", 0, "y2", 0))
	b.recordPrimitive("prim-line-special", p("line", "x1", "+Inf", "y1", 0, "x2", 5, "y2", 5))

	pointSets := map[string][]any{
		"nil":    nil,
		"empty":  {},
		"one":    {spec{"x": 3, "y": 4}},
		"two":    {spec{"x": 0, "y": 0}, spec{"x": 10, "y": 5}},
		"tri":    {spec{"x": 0, "y": 1.5}, spec{"x": 9, "y": 5}, spec{"x": 0, "y": 8.5}},
		"neg":    {spec{"x": -5, "y": -6}, spec{"x": -1, "y": -2}, spec{"x": -9, "y": 3}},
		"zero":   {spec{"x": "-0", "y": "-0"}, spec{"x": 0, "y": 0}},
		"nan0":   {spec{"x": "NaN", "y": 1}, spec{"x": 4, "y": 5}},
		"nan1":   {spec{"x": 2, "y": 1}, spec{"x": "NaN", "y": "NaN"}, spec{"x": 4, "y": 5}},
		"inf":    {spec{"x": 0, "y": 0}, spec{"x": "+Inf", "y": 1}},
		"neginf": {spec{"x": "-Inf", "y": 0}, spec{"x": 1, "y": 1}},
	}
	for _, name := range []string{"nil", "empty", "one", "two", "tri", "neg", "zero", "nan0", "nan1", "inf", "neginf"} {
		for _, sw := range strokes {
			points := pointSets[name]
			var pts any = points
			if name == "nil" {
				b.recordPrimitive("prim-polyline", p("polyline", "stroke_width", sw))
				b.recordPrimitive("prim-polygon", p("polygon", "stroke_width", sw))
				continue
			}
			b.recordPrimitive("prim-polyline", p("polyline", "points", pts, "stroke_width", sw))
			b.recordPrimitive("prim-polygon", p("polygon", "points", pts, "stroke_width", sw))
		}
	}
	b.recordPrimitive("prim-polyline-attrs", p("polyline", "id", "pl", "class", "route", "points", pointSets["tri"], "fill", "none", "stroke", "#222", "stroke_width", 2, "dash_array", "1 1", "marker_end", "arrow-end", "opacity", 0.9))
	b.recordPrimitive("prim-polygon-attrs", p("polygon", "id", "pg", "class", "hex", "points", pointSets["tri"], "fill", "#f0f", "stroke", "#222", "stroke_width", 2, "dash_array", "1 1", "opacity", 0.9))

	segSets := map[string][]any{
		"none":  nil,
		"move":  {spec{"op": "M", "args": []any{spec{"x": 1, "y": 2}}}},
		"line":  {spec{"op": "M", "args": []any{spec{"x": 0, "y": 0}}}, spec{"op": "L", "args": []any{spec{"x": 10, "y": 8}}}},
		"quad":  {spec{"op": "M", "args": []any{spec{"x": 0, "y": 0}}}, spec{"op": "Q", "args": []any{spec{"x": 5, "y": -9}, spec{"x": 10, "y": 0}}}},
		"cube":  {spec{"op": "M", "args": []any{spec{"x": 0, "y": 0}}}, spec{"op": "C", "args": []any{spec{"x": 1, "y": 7}, spec{"x": 8, "y": -3}, spec{"x": 10, "y": 4}}}},
		"close": {spec{"op": "M", "args": []any{spec{"x": 3, "y": 3}}}, spec{"op": "Z"}},
		"zonly": {spec{"op": "Z"}},
		"empty": {spec{"op": "M", "args": []any{}}},
		"nan":   {spec{"op": "M", "args": []any{spec{"x": "NaN", "y": 1}}}, spec{"op": "L", "args": []any{spec{"x": 4, "y": 5}}}},
		"neg":   {spec{"op": "M", "args": []any{spec{"x": -4, "y": -4}}}, spec{"op": "L", "args": []any{spec{"x": -9, "y": 2}}}},
	}
	for _, name := range []string{"none", "move", "line", "quad", "cube", "close", "zonly", "empty", "nan", "neg"} {
		for _, sw := range strokes {
			path := p("path", "stroke_width", sw)
			if segs := segSets[name]; segs != nil {
				path["segments"] = segs
			}
			b.recordPrimitive("prim-path", path)
		}
	}
	hints := []spec{
		{"x": 0, "y": 0, "width": 50, "height": 20},
		{"x": -5, "y": -5, "width": 0, "height": 0},
		{"x": 1, "y": 2, "width": "NaN", "height": 3},
	}
	for _, hint := range hints {
		for _, name := range []string{"none", "line", "cube"} {
			path := p("path", "stroke_width", 2, "bounds_hint", hint)
			if segs := segSets[name]; segs != nil {
				path["segments"] = segs
			}
			b.recordPrimitive("prim-path-hint", path)
		}
	}
	b.recordPrimitive("prim-path-attrs", p("path", "id", "pa", "class", "curve", "d", "M0 0 L10 10", "segments", segSets["line"], "fill", "none", "stroke", "#333", "stroke_width", 1, "dash_array", "2 2", "marker_end", "arrow-end", "opacity", 0.3, "link", "n"))

	b.textCorpus()

	groupKids := map[string][]any{
		"nil":    nil,
		"empty":  {},
		"rect":   {p("rect", "x", 0, "y", 0, "width", 10, "height", 10)},
		"two":    {p("rect", "x", 0, "y", 0, "width", 10, "height", 10), p("circle", "cx", 30, "cy", 5, "r", 5)},
		"zero":   {p("rect", "width", 0, "height", 0), p("rect", "x", 4, "y", 4, "width", 6, "height", 6)},
		"zero0":  {p("rect", "x", 4, "y", 4, "width", 6, "height", 6), p("rect", "width", 0, "height", 0)},
		"nested": {p("group", "children", []any{p("rect", "x", 1, "y", 1, "width", 2, "height", 2)}), p("line", "x1", 0, "y1", 0, "x2", 20, "y2", 9, "stroke_width", 2)},
		"deep":   {p("group", "children", []any{p("group", "children", []any{p("circle", "cx", 2, "cy", 2, "r", 1)})})},
		"nan":    {p("rect", "x", "NaN", "width", 3, "height", 3), p("rect", "x", 0, "width", 3, "height", 3)},
		"mixed":  {p("polyline", "points", pointSets["tri"], "stroke_width", 1), p("ellipse", "cx", 9, "cy", 9, "rx", 3, "ry", 1), p("polygon", "points", pointSets["neg"])},
	}
	for _, name := range []string{"nil", "empty", "rect", "two", "zero", "zero0", "nested", "deep", "nan", "mixed"} {
		g := p("group")
		if kids := groupKids[name]; name != "nil" {
			g["children"] = kids
		}
		b.recordPrimitive("prim-group", g)
		withAttrs := p("group", "id", "g-"+name, "class", "cluster", "transform", "translate(10, 20)", "link", "#"+name)
		if kids := groupKids[name]; name != "nil" {
			withAttrs["children"] = kids
		}
		b.recordPrimitive("prim-group-attrs", withAttrs)
	}
}

func (b *builder) textCorpus() {
	texts := []string{"", "A", "Hello, World!", "Ünïcödé", "日本語", "a<b>&c", "WWW iii", "line\nbreak", "\u00a0x", "😀 emoji", "tab\there"}
	sizes := []any{0, -5, 8, 14, 16.5, 24, 72, 100.125}
	weights := []string{"", "regular", "bold", "Bold", "700"}
	anchors := []string{"", "start", "middle", "end", "bogus"}
	baselines := []string{"", "alphabetic", "middle", "central", "hanging", "top", "bogus"}
	lengths := []any{0, 50, -3, 0.5}

	for _, text := range texts {
		for _, size := range sizes {
			for _, weight := range weights {
				b.recordPrimitive("prim-text", p("text", "x", 10, "y", 20, "text", text, "font_size", size, "font_weight", weight))
			}
		}
	}
	for _, anchor := range anchors {
		for _, baseline := range baselines {
			for _, size := range []any{14, 20} {
				for _, length := range lengths {
					b.recordPrimitive("prim-text-align", p("text", "x", 100, "y", 50, "text", "Anchor test", "font_size", size,
						"anchor", anchor, "baseline", baseline, "text_length", length))
				}
			}
		}
	}
	coords := []any{0, 7.5, -12, 100, "-0", 0.25}
	for i := 0; i < 400; i++ {
		prim := p("text",
			"x", coords[b.rng.Intn(len(coords))], "y", coords[b.rng.Intn(len(coords))],
			"text", texts[b.rng.Intn(len(texts))],
			"font_size", sizes[b.rng.Intn(len(sizes))],
			"font_weight", weights[b.rng.Intn(len(weights))],
			"anchor", anchors[b.rng.Intn(len(anchors))],
			"baseline", baselines[b.rng.Intn(len(baselines))],
			"text_length", lengths[b.rng.Intn(len(lengths))])
		b.recordPrimitive("prim-text-rand", prim)
	}
	b.recordPrimitive("prim-text-attrs", p("text", "id", "t1", "class", "label", "x", 1, "y", 2, "text", "Label", "font_size", 12,
		"font_family", "Inter, sans-serif", "font_weight", "bold", "fill", "#fff", "anchor", "middle", "baseline", "central", "text_length", 40, "opacity", 0.8, "link", "u"))
	b.recordPrimitive("prim-text-special", p("text", "x", "NaN", "y", 0, "text", "x", "font_size", 14, "baseline", "top"))
	b.recordPrimitive("prim-text-special", p("text", "x", 0, "y", "+Inf", "text", "x", "font_size", 14, "baseline", "top"))
	b.recordPrimitive("prim-text-special", p("text", "x", 0, "y", 0, "text", "x", "font_size", 14, "text_length", "NaN", "baseline", "top"))
	b.recordPrimitive("prim-text-special", p("text", "x", 0, "y", 0, "text", "x", "font_size", 14, "text_length", "+Inf", "baseline", "top"))
	b.recordPrimitive("prim-text-special", p("text", "x", "-0", "y", "-0", "text", "", "font_size", 14, "baseline", "top"))
}

func (b *builder) randomPrimitive(depth int) spec {
	coords := []any{0, 5, 12.5, -8, 40, 100.25, "-0"}
	c := func() any { return coords[b.rng.Intn(len(coords))] }
	size := func() any { return []any{0, 1, 4, 10.5, 30, 64}[b.rng.Intn(6)] }
	stroke := func() any { return []any{0, 1, 2, 3.5}[b.rng.Intn(4)] }
	kinds := 9
	if depth >= 2 {
		kinds = 8
	}
	switch b.rng.Intn(kinds) {
	case 0:
		return p("rect", "x", c(), "y", c(), "width", size(), "height", size(), "stroke_width", stroke(), "fill", "#abc")
	case 1:
		return p("circle", "cx", c(), "cy", c(), "r", size(), "stroke_width", stroke())
	case 2:
		return p("ellipse", "cx", c(), "cy", c(), "rx", size(), "ry", size(), "stroke_width", stroke())
	case 3:
		return p("line", "x1", c(), "y1", c(), "x2", c(), "y2", c(), "stroke_width", stroke(), "marker_end", "arrow-end")
	case 4:
		return p("polyline", "points", []any{spec{"x": c(), "y": c()}, spec{"x": c(), "y": c()}, spec{"x": c(), "y": c()}}, "stroke_width", stroke())
	case 5:
		return p("polygon", "points", []any{spec{"x": c(), "y": c()}, spec{"x": c(), "y": c()}, spec{"x": c(), "y": c()}}, "stroke_width", stroke())
	case 6:
		return p("path", "segments", []any{spec{"op": "M", "args": []any{spec{"x": c(), "y": c()}}}, spec{"op": "L", "args": []any{spec{"x": c(), "y": c()}}}}, "stroke_width", stroke())
	case 7:
		return p("text", "x", c(), "y", c(), "text", []any{"Node", "Label A", "ü", "", "Wide wide label"}[b.rng.Intn(5)],
			"font_size", []any{0, 10, 14, 18}[b.rng.Intn(4)],
			"anchor", []string{"", "start", "middle", "end"}[b.rng.Intn(4)],
			"baseline", []string{"", "middle", "top", "central"}[b.rng.Intn(4)])
	default:
		n := 1 + b.rng.Intn(3)
		kids := make([]any, 0, n)
		for i := 0; i < n; i++ {
			kids = append(kids, b.randomPrimitive(depth+1))
		}
		return p("group", "id", "grp", "children", kids)
	}
}

// recordScene runs the production scene pipeline for one spec.
func (b *builder) recordScene(prefix string, in spec) {
	b.add(prefix, "scene", in, func(raw any) (string, error) {
		m, _ := asMap(raw)
		for key := range m {
			switch key {
			case "width", "height", "theme", "view_box", "background", "metadata", "no_markers", "extra_markers", "primitives", "fit_padding":
			default:
				return "", fmt.Errorf("unknown scene key %q", key)
			}
		}
		width, err := num(m["width"])
		if err != nil {
			return "", err
		}
		height, err := num(m["height"])
		if err != nil {
			return "", err
		}
		var th theme.Theme
		if t, ok := m["theme"]; ok {
			if th, err = decodeTheme(t); err != nil {
				return "", err
			}
		}
		s := scene.NewScene(width, height, th)
		if v, ok := m["view_box"]; ok {
			if s.ViewBox, err = decodeRect(v); err != nil {
				return "", err
			}
		}
		if v, ok := m["background"]; ok {
			bg, ok := v.(string)
			if !ok {
				return "", errors.New("background must be a string")
			}
			s.Background = bg
		}
		if v, ok := m["metadata"]; ok {
			if s.Metadata, err = decodeStringMap(v); err != nil {
				return "", err
			}
		}
		if v, ok := m["no_markers"]; ok && v == true {
			s.Markers = nil
		}
		if v, ok := m["extra_markers"]; ok {
			list, err := asList(v)
			if err != nil {
				return "", err
			}
			for _, item := range list {
				def, err := decodeMarker(item)
				if err != nil {
					return "", err
				}
				s.AddMarker(def)
			}
		}
		if v, ok := m["primitives"]; ok {
			list, err := asList(v)
			if err != nil {
				return "", err
			}
			for _, item := range list {
				prim, err := decodePrimitive(item)
				if err != nil {
					return "", err
				}
				if fmaAmbiguous(prim) {
					b.skipped++
					return "", errSkip
				}
				s.Add(prim)
			}
		}
		validate := func() string {
			if err := s.Validate(); err != nil {
				return err.Error()
			}
			return "ok"
		}
		lines := []string{
			"bounds=" + goJSON(s.ComputeBounds()),
			"validate=" + validate(),
			"json=" + goJSON(s),
		}
		if v, ok := m["fit_padding"]; ok {
			padding, err := num(v)
			if err != nil {
				return "", err
			}
			s.FitToBounds(padding)
			lines = append(lines,
				"fit_view_box="+goJSON(s.ViewBox),
				"fit_width="+goJSON(s.Width),
				"fit_height="+goJSON(s.Height),
				"bounds_after_fit="+goJSON(s.ComputeBounds()),
				"validate_after_fit="+validate())
		}
		return strings.Join(lines, "\n"), nil
	})
}

func (b *builder) sceneCorpus() {
	rect := p("rect", "x", 10, "y", 20, "width", 100, "height", 40, "stroke_width", 2)
	circle := p("circle", "cx", 200, "cy", 50, "r", 25)
	line := p("line", "x1", 110, "y1", 40, "x2", 175, "y2", 50, "stroke_width", 1)
	text := p("text", "x", 60, "y", 40, "text", "Node A", "font_size", 14, "anchor", "middle", "baseline", "middle")
	hand := []spec{
		{"width": 0, "height": 0},
		{"width": 300, "height": 200},
		{"width": 300, "height": 200, "primitives": []any{}},
		{"width": 300, "height": 200, "primitives": []any{nil}},
		{"width": 300, "height": 200, "primitives": []any{nil, rect, nil}},
		{"width": 300, "height": 200, "primitives": []any{rect}},
		{"width": 300, "height": 200, "primitives": []any{rect, circle, line, text}},
		{"width": 300, "height": 200, "primitives": []any{p("rect", "width", 0, "height", 0)}},
		{"width": 300, "height": 200, "primitives": []any{p("rect", "width", 0, "height", 0), p("circle", "cx", 3, "cy", 3, "r", 0)}},
		{"width": 300, "height": 200, "primitives": []any{p("group")}},
		{"width": 300, "height": 200, "primitives": []any{p("group", "children", []any{rect, circle})}},
		{"width": "NaN", "height": 200},
		{"width": "+Inf", "height": 200},
		{"width": "-Inf", "height": 200},
		{"width": -5, "height": 200},
		{"width": 300, "height": "NaN"},
		{"width": 300, "height": "+Inf"},
		{"width": 300, "height": -0.5},
		{"width": "-0", "height": "-0"},
		{"width": 1e6, "height": 200},
		{"width": 123456789, "height": 0.0001},
		{"width": 1e21, "height": 200},
		{"width": 0.00001, "height": 1e-7},
		{"width": 300, "height": 200, "view_box": spec{"x": 0, "y": 0, "width": -1, "height": 10}},
		{"width": 300, "height": 200, "view_box": spec{"x": 0, "y": 0, "width": 10, "height": -1}},
		{"width": 300, "height": 200, "view_box": spec{"x": -20, "y": -30, "width": 400, "height": 300}},
		{"width": 300, "height": 200, "background": "#123456"},
		{"width": 300, "height": 200, "background": ""},
		{"width": 300, "height": 200, "metadata": spec{"title": "Flow", "author": "ä<&>", "": "empty key", "z": "last"}},
		{"width": 300, "height": 200, "metadata": spec{}},
		{"width": 300, "height": 200, "no_markers": true},
		{"width": 300, "height": 200, "no_markers": true, "extra_markers": []any{
			spec{"id": "m1", "view_box": spec{"x": 0, "y": 0, "width": 4, "height": 4}, "ref_x": 1, "ref_y": 2, "marker_width": 3, "marker_height": 3, "orient": "auto", "path": p("circle", "cx", 2, "cy", 2, "r", 1)},
			spec{"id": "m2", "view_box": spec{"x": 0, "y": 0, "width": 4, "height": 4}, "orient": "0", "path": nil},
			spec{"id": "m3", "path": p("polygon", "points", []any{spec{"x": 0, "y": 0}, spec{"x": 4, "y": 2}, spec{"x": 0, "y": 4}}, "fill", "red")},
		}},
		{"width": 300, "height": 200, "extra_markers": []any{spec{"id": "arrow-end", "orient": "auto-start-reverse", "path": p("rect", "width", 2, "height", 2)}}},
		{"width": 300, "height": 200, "primitives": []any{p("rect", "x", "NaN", "width", 3, "height", 3)}},
		{"width": 300, "height": 200, "primitives": []any{rect, p("rect", "x", "NaN", "width", 3, "height", 3)}},
		{"width": 300, "height": 200, "primitives": []any{rect, p("circle", "cx", "+Inf", "cy", 0, "r", 1)}},
		{"width": 300, "height": 200, "primitives": []any{rect, p("line", "x1", 1, "y1", 1, "x2", "NaN", "y2", 5)}},
		{"width": 300, "height": 200, "primitives": []any{p("polyline", "points", []any{spec{"x": "NaN", "y": 1}, spec{"x": 2, "y": 3}})}},
		{"width": 300, "height": 200, "primitives": []any{rect, p("rect", "id", "bad<&>", "x", 1, "y", 1, "width", "NaN", "height", 2)}},
		{"width": 300, "height": 200, "primitives": []any{p("rect", "x", 1e6, "y", 2e-7, "width", "NaN", "height", 2)}},
		{"width": 300, "height": 200, "primitives": []any{p("rect", "x", 123456789, "y", 1e21, "width", 5, "height", "NaN")}},
		{"width": 300, "height": 200, "primitives": []any{p("path", "stroke_width", 2, "bounds_hint", spec{"x": 0, "y": 0, "width": 9, "height": 9}), rect}},
		{"width": 300, "height": 200, "primitives": []any{p("path", "segments", []any{spec{"op": "M", "args": []any{spec{"x": 1, "y": 1}}}}), rect}},
	}
	for i, h := range hand {
		for _, pad := range []any{nil, 0, 10, -5, "NaN"} {
			in := copyMap(h)
			in["theme"] = spec{"resolve": sceneThemes[(i+len(sceneThemes))%len(sceneThemes)]}
			if pad != nil {
				in["fit_padding"] = pad
			}
			b.recordScene("scene-hand", in)
		}
	}
	// A zero Theme (empty name) falls back to symaira-dark; an inline theme
	// keeps its own tokens in the scene JSON.
	b.recordScene("scene-theme", spec{"width": 10, "height": 10})
	b.recordScene("scene-theme", spec{"width": 10, "height": 10, "theme": spec{}})
	b.recordScene("scene-theme", spec{"width": 10, "height": 10, "theme": spec{"name": "inline", "is_dark": true, "background": "#101010", "edge": "", "primary": "", "palette": []any{"#1", "#2"}, "font_family": "Mono"}})
	b.recordScene("scene-theme", spec{"width": 10, "height": 10, "theme": spec{"name": "inline-nil-palette", "background": "#fff"}})
	b.recordScene("scene-theme", spec{"width": 10, "height": 10, "theme": spec{"name": "inline-empty-palette", "palette": []any{}}})
	b.recordScene("scene-theme", spec{"width": 10, "height": 10, "theme": spec{"name": "esc", "background": "<&>\u2028", "font_family": "\"quoted\""}})

	for i := 0; i < 150; i++ {
		n := b.rng.Intn(7)
		prims := make([]any, 0, n)
		for j := 0; j < n; j++ {
			if b.rng.Intn(12) == 0 {
				prims = append(prims, nil)
				continue
			}
			prims = append(prims, b.randomPrimitive(0))
		}
		in := spec{
			"width":      []any{0, 100, 320, 640.5}[b.rng.Intn(4)],
			"height":     []any{0, 80, 240, 480.25}[b.rng.Intn(4)],
			"theme":      spec{"resolve": sceneThemes[b.rng.Intn(len(sceneThemes))]},
			"primitives": prims,
		}
		if b.rng.Intn(2) == 0 {
			in["fit_padding"] = []any{0, 8, 16, 24.5, -4}[b.rng.Intn(5)]
		}
		b.recordScene("scene-rand", in)
	}
}

func (b *builder) markerCorpus() {
	themes := []spec{
		{"resolve": "symaira-dark"}, {"resolve": "symaira-light"}, {"resolve": "report"}, {"resolve": "meeting"},
		{"resolve": "behoerde"}, {"resolve": "brief"}, {"resolve": "unknown"},
		{}, {"name": "x"}, {"name": "x", "edge": "#111"}, {"name": "x", "primary": "#222"},
		{"name": "x", "edge": "#111", "primary": "#222"}, {"name": "esc", "edge": "<&>", "primary": "\"q\""},
	}
	for _, th := range themes {
		b.add("markers", "default_markers", spec{"theme": th}, func(in any) (string, error) {
			m, _ := asMap(in)
			decoded, err := decodeTheme(m["theme"])
			if err != nil {
				return "", err
			}
			return goJSON(scene.DefaultMarkers(decoded)), nil
		})
	}
}

// ------------------------------------------------------------ registry helper

// registryScripts are the Register/Resolve/Names scripts. They run in a
// helper process because Register mutates process-global state; scripts of
// one run accumulate on top of each other in fixture order.
func registryScripts() []spec {
	reg := func(t spec) spec { return spec{"op": "register", "theme": t} }
	res := func(n string) spec { return spec{"op": "resolve", "name": n} }
	names := spec{"op": "names"}
	return []spec{
		{"ops": []any{names}},
		{"ops": []any{reg(spec{"name": ""}), names, res("")}},
		{"ops": []any{reg(spec{"name": "Custom", "is_dark": true, "background": "#000", "palette": []any{"#a", "#b"}}), names, res("custom"), res("CUSTOM"), res(" Custom "), res("Custom")}},
		{"ops": []any{reg(spec{"name": "Custom", "background": "#fff"}), res("custom"), names}},
		{"ops": []any{reg(spec{"name": "Keeps-Font", "font_family": "Mono"}), res("keeps-font")}},
		{"ops": []any{reg(spec{"name": "Default-Font", "font_family": ""}), res("default-font")}},
		{"ops": []any{reg(spec{"name": "REPORT", "background": "#eee"}), res("report"), names}},
		{"ops": []any{reg(spec{"name": "dark", "background": "#123"}), res("dark"), res("DARK"), res("symaira-dark"), names}},
		{"ops": []any{reg(spec{"name": "light", "background": "#456"}), res("light"), res("Light")}},
		{"ops": []any{reg(spec{"name": "İstanbul"}), res("İstanbul"), res("istanbul"), res("i̇stanbul"), names}},
		{"ops": []any{reg(spec{"name": "ÄÖÜ"}), res("äöü"), names}},
		{"ops": []any{reg(spec{"name": " padded "}), res("padded"), res(" padded "), names}},
		{"ops": []any{reg(spec{"name": "a"}), reg(spec{"name": "B"}), reg(spec{"name": "b2"}), names}},
		{"ops": []any{res("unknown-after-register"), res("symaira-light"), res("")}},
	}
}

// runRegistryHelper executes every registry script against the process-global
// registry and prints the resulting cases as JSON lines of testCase.
func runRegistryHelper(out *os.File) error {
	b := &builder{}
	for _, script := range registryScripts() {
		b.add("registry", "theme_registry", script, func(in any) (string, error) {
			m, _ := asMap(in)
			ops, err := asList(m["ops"])
			if err != nil {
				return "", err
			}
			var lines []string
			for _, raw := range ops {
				op, err := asMap(raw)
				if err != nil {
					return "", err
				}
				switch op["op"] {
				case "register":
					th, err := decodeTheme(op["theme"])
					if err != nil {
						return "", err
					}
					theme.Register(th)
					lines = append(lines, "register")
				case "resolve":
					name, _ := op["name"].(string)
					lines = append(lines, "resolve="+goJSON(theme.Resolve(name)))
				case "names":
					lines = append(lines, "names="+goJSON(theme.Names()))
				default:
					return "", fmt.Errorf("unknown registry op %v", op["op"])
				}
			}
			return strings.Join(lines, "\n"), nil
		})
	}
	if b.err != nil {
		return b.err
	}
	return json.NewEncoder(out).Encode(b.cases)
}

func registryHelperCases() ([]testCase, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate generator executable: %w", err)
	}
	cmd := exec.Command(exe) // #nosec G204 G702 -- re-executes this very binary in helper mode
	cmd.Env = append(os.Environ(), helperEnv+"=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("registry helper: %w\n%s", err, stderr.String())
	}
	var cases []testCase
	if err := json.Unmarshal(stdout.Bytes(), &cases); err != nil {
		return nil, fmt.Errorf("decode registry helper output: %w", err)
	}
	if len(cases) != len(registryScripts()) {
		return nil, fmt.Errorf("registry helper returned %d cases, want %d", len(cases), len(registryScripts()))
	}
	return cases, nil
}
