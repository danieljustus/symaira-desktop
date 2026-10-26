// Command renderlayoutgen freezes the production Go SymDraw layout engines
// (internal/draw/layout: the layered Sugiyama-style graph layout and the
// closed-form sequence layout) as a Rust replay fixture. Every case records a
// diagram input (Mermaid/JSON source parsed by the production parser, or a raw
// IR JSON document decoded without validation so the layout engine's own
// rejections are reachable) and the exact json.Marshal bytes of the Go result,
// or the exact error text.
//
// Host independence: the corpus contains no paths, no randomness beyond a
// fixed math/rand seed and no platform-dependent values. Layout arithmetic is
// additions, halvings and multiples of 1/64, which are exact; the only
// host-dependent expression reachable from layout is math.Hypot for circle
// nodes (assembly on amd64, fusable pure Go on arm64), so every graph holding
// a circle node whose size is not pinned by an explicit width and height is
// withheld and counted in the header.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/danieljustus/symaira-desktop/internal/draw/ir"
	"github.com/danieljustus/symaira-desktop/internal/draw/layout"
	parsepkg "github.com/danieljustus/symaira-desktop/internal/draw/parse"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

const (
	defaultOutput = "testdata/port/render/layout.json"
	minedMinRunes = 4
	minedMaxRunes = 20000
	randomSeed    = 20261025
)

// minedGlobs select the Go draw tests whose string literals are replayed as
// diagram sources; literals that do not parse are ignored.
var minedGlobs = []string{
	"internal/draw/*_test.go",
	"internal/draw/*/*_test.go",
}

type fixture struct {
	SchemaVersion int      `json:"schema_version"`
	Oracle        oracle   `json:"oracle"`
	Entries       []string `json:"entries"`
	Withheld      withheld `json:"withheld"`
	Cases         []testCase
}

type oracle struct {
	Commit  string `json:"commit"`
	Release string `json:"release"`
}

// withheld counts corpus members that were deliberately not frozen.
type withheld struct {
	CircleHypotAmbiguous int `json:"circle_hypot_ambiguous_skipped"`
	UnparsableSources    int `json:"unparsable_sources_skipped"`
}

type testCase struct {
	ID       string `json:"id"`
	Entry    string `json:"entry"`
	Mode     string `json:"mode"`
	Input    string `json:"input"`
	Expected string `json:"expected,omitempty"`
	Error    string `json:"error,omitempty"`
	// Crossings repeats Result.Crossings of a successful layered case as a
	// plain number, so the replay compares one Go observation structurally
	// and a fixture mutation of it is detectable.
	Crossings *int `json:"crossings,omitempty"`
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

	b := &builder{seen: map[string]bool{}, ids: map[string]bool{}}
	b.handCorpus()
	if err := b.minedCorpus(root); err != nil {
		return "", err
	}
	b.randomCorpus()
	if b.err != nil {
		return "", b.err
	}

	content, err := render(fixture{
		SchemaVersion: 1,
		Oracle:        oracle{Commit: opts.commit, Release: opts.release},
		Entries:       []string{"layered", "sequence"},
		Withheld:      b.withheld,
		Cases:         b.cases,
	})
	if err != nil {
		return "", err
	}
	if opts.check {
		current, err := os.ReadFile(opts.output) // #nosec G304 -- caller-selected fixture path
		if err != nil || !bytes.Equal(current, content) {
			return "", errors.New("render layout fixture is stale; regenerate deliberately")
		}
		return fmt.Sprintf("PASS render layout fixture (%d cases)", len(b.cases)), nil
	}
	if err := os.MkdirAll(filepath.Dir(opts.output), 0o750); err != nil {
		return "", fmt.Errorf("create directory: %w", err)
	}
	if err := os.WriteFile(opts.output, content, 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", opts.output, err)
	}
	return fmt.Sprintf("PASS render layout fixture generated (%d cases)", len(b.cases)), nil
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
	// The Cases member has no tag, so Go names it "Cases"; replace the nil
	// slice member that closes the header.
	idx := bytes.LastIndex(head, []byte(",\n  \"Cases\": null"))
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

// builder accumulates deduplicated cases. The first error sticks.
type builder struct {
	cases    []testCase
	seen     map[string]bool
	ids      map[string]bool
	withheld withheld
	err      error
}

func (b *builder) fail(format string, args ...any) {
	if b.err == nil {
		b.err = fmt.Errorf(format, args...)
	}
}

// decodeIR is the production ParseJSON decoder without ir.Validate.
func decodeIR(text string) (*ir.Diagram, error) {
	var diag ir.Diagram
	dec := json.NewDecoder(strings.NewReader(text))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&diag); err != nil {
		return nil, err
	}
	return &diag, nil
}

// load resolves the case input to a diagram: Mermaid/JSON through the
// production parse.Source, or a raw unvalidated IR document.
func load(mode, input string) (*ir.Diagram, error) {
	switch mode {
	case "source":
		return parsepkg.Source(input)
	case "ir":
		return decodeIR(input)
	}
	return nil, fmt.Errorf("unknown mode %q", mode)
}

// circleAmbiguous reports whether a circle node's size depends on math.Hypot.
func circleAmbiguous(d *ir.Diagram) bool {
	for _, n := range d.Nodes {
		if n.Shape == ir.ShapeCircle && !(n.Width > 0 && n.Height > 0) {
			return true
		}
	}
	return false
}

// run calls the production entry and records its exact bytes or error text.
func run(entry string, d *ir.Diagram) (expected, errText string, crossings *int, panicked any) {
	defer func() {
		if r := recover(); r != nil {
			panicked = r
		}
	}()
	switch entry {
	case "layered":
		res, err := layout.Layout(d)
		if err != nil {
			return "", err.Error(), nil, nil
		}
		encoded, err := json.Marshal(res)
		if err != nil {
			return "", err.Error(), nil, nil
		}
		return string(encoded), "", &res.Crossings, nil
	case "sequence":
		res, err := layout.SequenceLayoutFromIR(d)
		if err != nil {
			return "", err.Error(), nil, nil
		}
		encoded, err := json.Marshal(res)
		if err != nil {
			return "", err.Error(), nil, nil
		}
		return string(encoded), "", nil, nil
	}
	return "", "", nil, fmt.Sprintf("unknown entry %q", entry)
}

// add records one case. Sources that do not parse are counted, not frozen
// (parsing is frozen by renderdslgen); raw IR documents must decode.
func (b *builder) add(id, entry, mode, input string) {
	if !utf8.ValidString(input) {
		b.fail("%s: input is not valid UTF-8", id)
		return
	}
	key := entry + "\x00" + mode + "\x00" + input
	if b.seen[key] {
		return
	}
	d, err := load(mode, input)
	if err != nil {
		if mode == "source" {
			b.withheld.UnparsableSources++
			return
		}
		b.fail("%s: IR input does not decode: %v", id, err)
		return
	}
	if entry == "layered" && circleAmbiguous(d) {
		b.seen[key] = true
		b.withheld.CircleHypotAmbiguous++
		return
	}
	expected, errText, crossings, panicked := run(entry, d)
	if panicked != nil {
		b.fail("%s: Go oracle panicked: %v", id, panicked)
		return
	}
	b.seen[key] = true
	if b.ids[id] {
		b.fail("duplicate case id %s", id)
		return
	}
	b.ids[id] = true
	b.cases = append(b.cases, testCase{ID: id, Entry: entry, Mode: mode, Input: input, Expected: expected, Error: errText, Crossings: crossings})
}

// addBoth records the same document under both layout entries.
func (b *builder) addBoth(id, mode, input string) {
	b.add(id+"-layered", "layered", mode, input)
	b.add(id+"-sequence", "sequence", mode, input)
}

func irJSON(b *builder, id string, d *ir.Diagram) string {
	encoded, err := json.Marshal(d)
	if err != nil {
		b.fail("%s: marshal IR: %v", id, err)
		return ""
	}
	return string(encoded)
}

func (b *builder) graph(id string, d *ir.Diagram) {
	if d.Kind == "" {
		d.Kind = ir.KindGraph
	}
	b.add(id, "layered", "ir", irJSON(b, id, d))
}

func nodes(spec ...string) []ir.Node {
	out := make([]ir.Node, 0, len(spec))
	for _, s := range spec {
		out = append(out, ir.Node{ID: s, Label: strings.ToUpper(s[:1]) + s[1:]})
	}
	return out
}

func edges(pairs ...string) []ir.Edge {
	out := make([]ir.Edge, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, ir.Edge{From: pairs[i], To: pairs[i+1]})
	}
	return out
}

var directions = []ir.Direction{"", ir.DirTD, ir.DirTB, ir.DirBT, ir.DirLR, ir.DirRL}

// handCorpus covers every branch of the layered layout and the sequence
// layout, including the engine's own rejections.
func (b *builder) handCorpus() {
	// Layout invariants: cycles, long edges, every direction.
	for i, dir := range directions {
		d := ir.Diagram{Direction: dir, Nodes: nodes("a", "b", "c", "d"), Edges: edges("a", "d", "a", "b", "b", "c", "c", "a", "c", "d")}
		b.graph(fmt.Sprintf("hand-fixture-dir%d", i), &d)
	}
	for i, dir := range directions {
		d := ir.Diagram{Direction: dir, Nodes: nodes("x", "y", "z"), Edges: edges("x", "x", "x", "y", "y", "y", "z", "z", "y", "z")}
		b.graph(fmt.Sprintf("hand-selfloops-dir%d", i), &d)
	}
	// Empty and single-node graphs.
	for i, dir := range directions {
		b.graph(fmt.Sprintf("hand-empty-dir%d", i), &ir.Diagram{Direction: dir})
		b.graph(fmt.Sprintf("hand-single-dir%d", i), &ir.Diagram{Direction: dir, Nodes: nodes("only")})
	}
	b.graph("hand-no-edges", &ir.Diagram{Direction: ir.DirLR, Nodes: nodes("a", "b", "c", "d", "e")})
	b.graph("hand-multi-edge", &ir.Diagram{Nodes: nodes("a", "b"), Edges: edges("a", "b", "a", "b", "b", "a", "a", "b")})
	b.graph("hand-diamond-skip", &ir.Diagram{Nodes: nodes("a", "b", "c", "d", "e"), Edges: edges("a", "b", "a", "c", "b", "d", "c", "d", "a", "e", "e", "d", "a", "d")})
	b.graph("hand-chain-long", &ir.Diagram{Direction: ir.DirBT, Nodes: nodes("n0", "n1", "n2", "n3", "n4", "n5", "n6", "n7"), Edges: edges("n0", "n1", "n1", "n2", "n2", "n3", "n3", "n4", "n4", "n5", "n5", "n6", "n6", "n7", "n0", "n7", "n7", "n0")})
	b.graph("hand-reverse-skip", &ir.Diagram{Direction: ir.DirRL, Nodes: nodes("a", "b", "c", "d"), Edges: edges("d", "a", "a", "b", "b", "c", "c", "d")})
	// Explicit sizes, fractional sizes and shapes that avoid math.Hypot.
	sized := []ir.Node{
		{ID: "a", Label: "Wide", Width: 400, Height: 90},
		{ID: "b", Label: "Frac", Width: 100.25, Height: 33.5},
		{ID: "c", Label: "OnlyW", Width: 120},
		{ID: "d", Label: "OnlyH", Height: 60},
		{ID: "e", Label: "Tenth", Width: 0.1, Height: 0.7},
		{ID: "f", Label: "Circle", Shape: ir.ShapeCircle, Width: 90, Height: 90},
	}
	for i, dir := range directions {
		b.graph(fmt.Sprintf("hand-sized-dir%d", i), &ir.Diagram{Direction: dir, Nodes: sized, Edges: edges("a", "b", "a", "c", "b", "d", "c", "e", "d", "f", "e", "f", "a", "f")})
	}
	shapes := []ir.NodeShape{"", ir.ShapeRect, ir.ShapeRound, ir.ShapeDiamond, ir.ShapeCylinder, ir.ShapePill, ir.ShapeStadium, ir.ShapeSubroutine, ir.ShapeHexagon, ir.ShapeAsymmetric, "unknown-shape"}
	var shapeNodes []ir.Node
	for i, s := range shapes {
		shapeNodes = append(shapeNodes, ir.Node{ID: fmt.Sprintf("s%02d", i), Label: "Label " + string(s), Shape: s})
	}
	b.graph("hand-shapes", &ir.Diagram{Direction: ir.DirLR, Nodes: shapeNodes, Edges: edges("s00", "s01", "s01", "s02", "s02", "s03", "s03", "s04", "s04", "s05", "s05", "s06", "s06", "s07", "s07", "s08", "s08", "s09", "s09", "s10")})
	// Labels: multi-line, long, unicode, empty.
	labels := []ir.Node{
		{ID: "l1", Label: ""},
		{ID: "l2", Label: "A very long label that has to wrap over several lines inside the node box"},
		{ID: "l3", Label: "Ünïcödé 日本語 العربية 😀"},
		{ID: "l4", Label: "line1\nline2\nline3"},
		{ID: "l5", Label: "   spaced   "},
	}
	b.graph("hand-labels", &ir.Diagram{Nodes: labels, Edges: edges("l1", "l2", "l1", "l3", "l2", "l4", "l3", "l5")})
	// Node ids that sort unusually (byte order, case, unicode, punctuation).
	b.graph("hand-id-order", &ir.Diagram{Nodes: nodes("B", "a", "Z", "ä", "10", "9", "_x", "-y", "日本"), Edges: edges("B", "a", "a", "Z", "Z", "ä", "10", "9", "9", "_x", "_x", "-y", "-y", "日本", "日本", "B")})
	// Real node ids that collide with the generated dummy ids.
	b.graph("hand-dummy-collision", &ir.Diagram{Nodes: nodes("a", "b", "c", "__dummy_1_1", "__dummy_2_1"), Edges: edges("a", "b", "b", "c", "a", "c", "a", "__dummy_1_1")})
	// Extreme explicit sizes overflow json.Marshal.
	b.graph("hand-overflow-width", &ir.Diagram{Nodes: []ir.Node{{ID: "a", Label: "A", Width: 1.7e308, Height: 10}, {ID: "b", Label: "B", Width: 1.7e308, Height: 10}}})
	b.graph("hand-overflow-height", &ir.Diagram{Direction: ir.DirLR, Nodes: []ir.Node{{ID: "a", Label: "A", Width: 10, Height: 1.7e308}, {ID: "b", Label: "B", Width: 10, Height: 1.7e308}}})
	b.graph("hand-huge-but-finite", &ir.Diagram{Nodes: []ir.Node{{ID: "a", Label: "A", Width: 1e300, Height: 1e300}}, Edges: nil})

	// The layout engine's own rejections (unreachable through the validator).
	rejections := map[string]ir.Diagram{
		"reject-chart":       {Kind: ir.KindChart},
		"reject-sequence":    {Kind: ir.KindSequence},
		"reject-timeline":    {Kind: ir.KindTimeline},
		"reject-custom":      {Kind: ir.KindCustom},
		"reject-empty-kind":  {Kind: "", Nodes: nodes("a")},
		"reject-unknown":     {Kind: "mystery"},
		"reject-empty-id":    {Kind: ir.KindGraph, Nodes: []ir.Node{{ID: "a"}, {ID: ""}}},
		"reject-duplicate":   {Kind: ir.KindGraph, Nodes: nodes("a", "b", "a")},
		"reject-edge-source": {Kind: ir.KindGraph, Nodes: nodes("a"), Edges: edges("ghost", "a")},
		"reject-edge-target": {Kind: ir.KindGraph, Nodes: nodes("a", "b"), Edges: edges("a", "b", "a", "ghost")},
		"reject-edge-second": {Kind: ir.KindGraph, Nodes: nodes("a"), Edges: edges("a", "a", "a", "x", "y", "a")},
		"reject-id-first":    {Kind: ir.KindGraph, Nodes: []ir.Node{{ID: ""}, {ID: "a"}, {ID: "a"}}, Edges: edges("q", "r")},
	}
	names := make([]string, 0, len(rejections))
	for name := range rejections {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		d := rejections[name]
		b.addBoth("hand-"+name, "ir", irJSON(b, name, &d))
	}

	b.sequenceHand()
	b.sourceHand()
}

func (b *builder) sequenceHand() {
	doc := func(custom string) string {
		return `{"kind":"sequence","custom":` + custom + `}`
	}
	cases := []struct{ id, custom string }{
		{"seq-basic", `{"actors":[{"ID":"A","Label":"Alice"},{"ID":"B","Label":"Bob"}],"messages":[{"From":"A","To":"B","Text":"hi","Order":0}]}`},
		{"seq-lowercase-keys", `{"actors":[{"id":"A","label":"Alice"},{"id":"B"}],"messages":[{"from":"A","to":"B","text":"hi","order":0},{"from":"B","to":"A","text":"yo","order":1}]}`},
		{"seq-mixed-case-keys", `{"actors":[{"iD":"A"},{"Id":"B"}],"messages":[{"FROM":"A","tO":"B","TEXT":"x","ORDER":7}]}`},
		{"seq-unknown-keys", `{"actors":[{"ID":"A","extra":1,"nested":{"a":[1,2]}}],"messages":[{"ID":"x","Note":null}]}`},
		{"seq-no-actors", `{"actors":[],"messages":[]}`},
		{"seq-actors-null", `{"actors":null,"messages":null}`},
		{"seq-one-actor", `{"actors":[{"ID":"A"}],"messages":[{},{},{}]}`},
		{"seq-many", `{"actors":[{"ID":"a"},{"ID":"b"},{"ID":"c"},{"ID":"d"},{"ID":"e"},{"ID":"f"},{"ID":"g"}],"messages":[{},{},{},{},{},{},{},{},{},{},{},{}]}`},
		{"seq-duplicate-ids", `{"actors":[{"ID":"A"},{"ID":"B"},{"ID":"A"},{"ID":""},{}],"messages":[]}`},
		{"seq-null-elements", `{"actors":[null,{"ID":"A"},null],"messages":[null,null]}`},
		{"seq-missing-actors", `{"messages":[]}`},
		{"seq-missing-messages", `{"actors":[]}`},
		{"seq-missing-both", `{"other":1}`},
		{"seq-empty-custom", `{}`},
		{"seq-actors-string", `{"actors":"nope","messages":[]}`},
		{"seq-actors-number", `{"actors":3,"messages":[]}`},
		{"seq-actors-float", `{"actors":2.5,"messages":[]}`},
		{"seq-actors-bool", `{"actors":true,"messages":[]}`},
		{"seq-actors-object", `{"actors":{"ID":"A"},"messages":[]}`},
		{"seq-messages-string", `{"actors":[],"messages":"nope"}`},
		{"seq-messages-object", `{"actors":[],"messages":{"a":1}}`},
		{"seq-messages-bool", `{"actors":[],"messages":false}`},
		{"seq-element-string", `{"actors":["A"],"messages":[]}`},
		{"seq-element-number", `{"actors":[1],"messages":[]}`},
		{"seq-element-array", `{"actors":[["A"]],"messages":[]}`},
		{"seq-element-bool", `{"actors":[false],"messages":[]}`},
		{"seq-message-element-string", `{"actors":[],"messages":["m"]}`},
		{"seq-id-number", `{"actors":[{"ID":5}],"messages":[]}`},
		{"seq-id-bool", `{"actors":[{"ID":true}],"messages":[]}`},
		{"seq-id-array", `{"actors":[{"ID":["a"]}],"messages":[]}`},
		{"seq-id-object", `{"actors":[{"ID":{"a":1}}],"messages":[]}`},
		{"seq-label-number", `{"actors":[{"ID":"A","Label":1.5}],"messages":[]}`},
		{"seq-first-error-wins", `{"actors":[{"ID":1},{"ID":true}],"messages":[]}`},
		{"seq-actor-error-before-message", `{"actors":[{"ID":1}],"messages":[{"Order":"x"}]}`},
		{"seq-order-string", `{"actors":[],"messages":[{"Order":"3"}]}`},
		{"seq-order-float", `{"actors":[],"messages":[{"Order":1.5}]}`},
		{"seq-order-integral-float", `{"actors":[],"messages":[{"Order":3.0}]}`},
		{"seq-order-exp", `{"actors":[],"messages":[{"Order":1e3}]}`},
		{"seq-order-exp-large", `{"actors":[],"messages":[{"Order":1e21}]}`},
		{"seq-order-negative", `{"actors":[],"messages":[{"Order":-4}]}`},
		{"seq-order-overflow", `{"actors":[],"messages":[{"Order":12345678901234567890}]}`},
		{"seq-order-max-int", `{"actors":[],"messages":[{"Order":9223372036854775807}]}`},
		{"seq-order-bool", `{"actors":[],"messages":[{"Order":false}]}`},
		{"seq-order-null", `{"actors":[],"messages":[{"Order":null,"From":null}]}`},
		{"seq-from-number", `{"actors":[],"messages":[{"From":9}]}`},
		{"seq-to-object", `{"actors":[],"messages":[{"To":{}}]}`},
		{"seq-text-array", `{"actors":[],"messages":[{"Text":[]}]}`},
		{"seq-case-duplicate-key", `{"actors":[{"ID":"A","id":"B"}],"messages":[]}`},
		{"seq-case-duplicate-key-error", `{"actors":[{"ID":"A","id":7}],"messages":[]}`},
		{"seq-unicode-ids", `{"actors":[{"ID":"日本"},{"ID":"ä"},{"ID":"\u0000"},{"ID":"<&>"}],"messages":[{"Text":"</script>"}]}`},
		{"seq-extra-custom-keys", `{"actors":[{"ID":"A"}],"messages":[],"title":"ignored","n":1}`},
		{"seq-deep-ignored", `{"actors":[{"ID":"A","x":[[[[[[[[1]]]]]]]]}],"messages":[]}`},
	}
	for _, c := range cases {
		b.addBoth("hand-"+c.id, "ir", doc(c.custom))
	}
	// A graph document through the sequence entry and a sequence document
	// through the layered entry are covered by addBoth above and below.
	b.addBoth("hand-seq-width-direction", "ir", `{"kind":"sequence","direction":"LR","width":10,"height":20,"custom":{"actors":[{"ID":"A"}],"messages":[{}]}}`)
}

// sourceHand holds hand-written Mermaid and JSON sources that go through the
// production parser.
func (b *builder) sourceHand() {
	graphs := []string{
		"graph TD\nA-->B\nB-->C\nA-->C\n",
		"graph LR\nA[Start] --> B(Round)\nB --> C{Decide}\nC --> D((Circle))\nC --> E[(Database)]\n",
		"flowchart BT\nA --> B\nB --> A\nA --> A\n",
		"graph RL\nsubgraph one\nA\nB\nend\nA --> B\nB --> C\n",
		"graph TD\nA-->B\nA-->C\nA-->D\nA-->E\nA-->F\nB-->G\nC-->G\nD-->G\nE-->G\nF-->G\nG-->H\nH-->A\n",
		"graph TD\nA[\"Quoted label\"] -->|edge| B[Another]\nB -.-> C\nC ==> D\nD --- E\n",
		"graph TD\n  A --> B\n  %% comment\n  B --> C\r\n  C --> D\n",
		"graph\nA\nB\nC\n",
		"graph TD\nÄ --> Ö\nÖ --> Ü\n日本 --> Ä\n",
		`{"kind":"graph","direction":"LR","nodes":[{"id":"a","label":"A"},{"id":"b","label":"B","width":150,"height":40}],"edges":[{"from":"a","to":"b"}]}`,
		`{"kind":"graph","nodes":[{"id":"a"},{"id":"b"},{"id":"c"}],"edges":[{"from":"a","to":"b"},{"from":"b","to":"c"},{"from":"c","to":"a"}],"groups":[{"members":["a","b"]}]}`,
		`{"kind":"graph","nodes":[],"edges":[]}`,
		`{"kind":"graph"}`,
		`{"kind":"chart","chart":{"type":"bar","series":[{"name":"s","data":[{"y":1}]}]}}`,
	}
	for i, g := range graphs {
		b.addBoth(fmt.Sprintf("hand-source-graph-%02d", i), "source", g)
	}
	sequences := []string{
		"sequenceDiagram\nparticipant A\nparticipant B\nA->>B: hello\nB-->>A: back\n",
		"sequenceDiagram\nA->>B: one\nB->>C: two\nC->>A: three\n",
		"sequenceDiagram\nactor U as User\nparticipant S as Server\nU->>S: request\nS-->>U: response\n",
		"sequenceDiagram\nparticipant A\n",
		"sequenceDiagram\nparticipant A\nA->>A: self\nA->>A: again\n",
		"sequenceDiagram\nparticipant \"Spaced Name\" as X\nX->>Y: ünï\n",
	}
	for i, s := range sequences {
		b.addBoth(fmt.Sprintf("hand-source-seq-%02d", i), "source", s)
	}
}

// minedCorpus replays every string literal of the Go draw tests that parses as
// a diagram, through both layout entries.
func (b *builder) minedCorpus(root string) error {
	var paths []string
	for _, glob := range minedGlobs {
		matches, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(glob)))
		if err != nil {
			return fmt.Errorf("glob %s: %w", glob, err)
		}
		paths = append(paths, matches...)
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return errors.New("mined corpus found no Go draw test sources")
	}
	dedupe := map[string]bool{}
	for _, path := range paths {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return fmt.Errorf("parse %s: %w", rel, err)
		}
		base := strings.NewReplacer("/", "-", ".", "-", "_", "-").Replace(strings.TrimSuffix(filepath.ToSlash(rel), ".go"))
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
			b.addBoth(id, "source", value)
			return true
		})
	}
	return nil
}

var labelPool = []string{
	"", "A", "Start", "End", "Process", "Decision", "Database", "Cache layer",
	"A somewhat longer label that wraps", "Ünïcödé 日本語", "multi\nline", "Wide wide wide wide wide wide wide label",
	"x", "Worker 7", "<&>", "\"quoted\"",
}

var shapePool = []ir.NodeShape{"", ir.ShapeRect, ir.ShapeRound, ir.ShapeDiamond, ir.ShapeCylinder, ir.ShapePill, ir.ShapeStadium, ir.ShapeSubroutine, ir.ShapeHexagon, ir.ShapeAsymmetric}

var dimPool = []float64{0, 0, 0, 0, 40, 64, 100.5, 120, 33.25, 0.1}

// randomCorpus covers the crossing-reduction paths with seeded random graphs:
// small mixed graphs, and wide layered graphs whose layers exceed Go's
// stable-sort block size of 20 so merge steps run.
func (b *builder) randomCorpus() {
	rng := rand.New(rand.NewSource(randomSeed)) // #nosec G404 -- fixed seed, deterministic fixture corpus
	randomNode := func(id string) ir.Node {
		n := ir.Node{ID: id, Label: labelPool[rng.Intn(len(labelPool))], Shape: shapePool[rng.Intn(len(shapePool))]}
		if rng.Intn(5) == 0 {
			n.Width = dimPool[rng.Intn(len(dimPool))]
		}
		if rng.Intn(5) == 0 {
			n.Height = dimPool[rng.Intn(len(dimPool))]
		}
		return n
	}
	for i := 0; i < 300; i++ {
		n := 1 + rng.Intn(14)
		d := ir.Diagram{Direction: directions[rng.Intn(len(directions))]}
		ids := make([]string, n)
		for j := range ids {
			ids[j] = fmt.Sprintf("n%02d", j)
			d.Nodes = append(d.Nodes, randomNode(ids[j]))
		}
		for e := rng.Intn(2*n + 2); e > 0; e-- {
			d.Edges = append(d.Edges, ir.Edge{From: ids[rng.Intn(n)], To: ids[rng.Intn(n)]})
		}
		b.graph(fmt.Sprintf("random-small-%03d", i), &d)
	}
	for i := 0; i < 24; i++ {
		n := 15 + rng.Intn(16)
		d := ir.Diagram{Direction: directions[rng.Intn(len(directions))]}
		ids := make([]string, n)
		for j := range ids {
			ids[j] = fmt.Sprintf("m%02d", j)
			d.Nodes = append(d.Nodes, randomNode(ids[j]))
		}
		for e := n/2 + rng.Intn(2*n); e > 0; e-- {
			d.Edges = append(d.Edges, ir.Edge{From: ids[rng.Intn(n)], To: ids[rng.Intn(n)]})
		}
		b.graph(fmt.Sprintf("random-medium-%03d", i), &d)
	}
	for i := 0; i < 24; i++ {
		layers := 2 + rng.Intn(3)
		d := ir.Diagram{Direction: directions[rng.Intn(len(directions))]}
		var prev, all []string
		for l := 0; l < layers; l++ {
			width := 1 + rng.Intn(36)
			if l == 0 && rng.Intn(2) == 0 {
				width = 1 + rng.Intn(3)
			}
			var cur []string
			for j := 0; j < width; j++ {
				id := fmt.Sprintf("w%d_%02d", l, j)
				cur = append(cur, id)
				d.Nodes = append(d.Nodes, randomNode(id))
			}
			for _, to := range cur {
				if len(prev) > 0 && rng.Intn(5) != 0 {
					d.Edges = append(d.Edges, ir.Edge{From: prev[rng.Intn(len(prev))], To: to})
					if rng.Intn(3) == 0 {
						d.Edges = append(d.Edges, ir.Edge{From: prev[rng.Intn(len(prev))], To: to})
					}
				}
				if len(all) > len(prev) && rng.Intn(6) == 0 {
					d.Edges = append(d.Edges, ir.Edge{From: all[rng.Intn(len(all)-len(prev))], To: to})
				}
			}
			all = append(all, cur...)
			prev = cur
		}
		if rng.Intn(3) == 0 && len(all) > 2 {
			d.Edges = append(d.Edges, ir.Edge{From: all[len(all)-1], To: all[0]})
		}
		b.graph(fmt.Sprintf("random-wide-%03d", i), &d)
	}
	// Wide fan-outs: one parent with many children, and the transpose.
	for _, width := range []int{3, 19, 20, 21, 22, 41, 42} {
		out := ir.Diagram{}
		in := ir.Diagram{Direction: ir.DirLR}
		out.Nodes = append(out.Nodes, ir.Node{ID: "root", Label: "Root"})
		in.Nodes = append(in.Nodes, ir.Node{ID: "sink", Label: "Sink"})
		for j := 0; j < width; j++ {
			id := fmt.Sprintf("c%02d", j)
			out.Nodes = append(out.Nodes, ir.Node{ID: id, Label: labelPool[rng.Intn(len(labelPool))]})
			in.Nodes = append(in.Nodes, ir.Node{ID: id, Label: labelPool[rng.Intn(len(labelPool))]})
			out.Edges = append(out.Edges, ir.Edge{From: "root", To: id})
			in.Edges = append(in.Edges, ir.Edge{From: id, To: "sink"})
		}
		b.graph(fmt.Sprintf("fan-out-%02d", width), &out)
		b.graph(fmt.Sprintf("fan-in-%02d", width), &in)
	}
}
