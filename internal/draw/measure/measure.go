// Package measure provides exact embedded-font text measurement, line breaking,
// and intrinsic node sizing using Inter TrueType fonts and sfnt parsing.
package measure

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"sync"

	"github.com/danieljustus/symaira-desktop/internal/draw/fonts"
	"github.com/danieljustus/symaira-desktop/internal/draw/ir"
	"golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// FontWeight specifies font boldness for metrics.
type FontWeight string

const (
	WeightRegular FontWeight = "regular"
	WeightBold    FontWeight = "bold"
)

// NormalizeFontWeight maps common weight strings ("normal", "400", "bold", "700", etc.)
// to canonical FontWeight.
func NormalizeFontWeight(w string) FontWeight {
	switch strings.ToLower(strings.TrimSpace(w)) {
	case "bold", "700", "800", "900", "bolder":
		return WeightBold
	default:
		return WeightRegular
	}
}

// TextMetrics holds exact typography metrics for a measured text string.
type TextMetrics struct {
	Width         float64   `json:"width"`
	Height        float64   `json:"height"`
	Ascent        float64   `json:"ascent"`
	Descent       float64   `json:"descent"`
	CapHeight     float64   `json:"cap_height"`
	XHeight       float64   `json:"x_height"`
	LineHeight    float64   `json:"line_height"`
	GlyphAdvances []float64 `json:"glyph_advances,omitempty"`
}

// LineBreakResult contains the output of breaking text across multiple lines.
type LineBreakResult struct {
	Lines        []string  `json:"lines"`
	MaxLineWidth float64   `json:"max_line_width"`
	TotalHeight  float64   `json:"total_height"`
	LineHeights  []float64 `json:"line_heights"`
	LineSpacing  float64   `json:"line_spacing"`
}

// NodeMeasureOptions configures intrinsic node sizing.
type NodeMeasureOptions struct {
	FontSize    float64
	FontWeight  FontWeight
	PaddingX    float64
	PaddingY    float64
	MinWidth    float64
	MinHeight   float64
	MaxWidth    float64
	LineSpacing float64
}

// DefaultNodeMeasureOptions returns sensible defaults for node sizing.
func DefaultNodeMeasureOptions() NodeMeasureOptions {
	return NodeMeasureOptions{
		FontSize:    14.0,
		FontWeight:  WeightRegular,
		PaddingX:    20.0,
		PaddingY:    14.0,
		MinWidth:    80.0,
		MinHeight:   40.0,
		MaxWidth:    240.0,
		LineSpacing: 4.0,
	}
}

// Measurer evaluates exact text metrics and glyph layouts against sfnt font tables.
type Measurer struct {
	regular  *sfnt.Font
	bold     *sfnt.Font
	fontHash string
	mu       sync.Mutex
	buf      sfnt.Buffer
}

var (
	defaultMeasurer     *Measurer
	defaultMeasurerOnce sync.Once
	defaultMeasurerErr  error
)

// Default returns the process-wide default Measurer loaded with embedded Inter fonts.
func Default() *Measurer {
	defaultMeasurerOnce.Do(func() {
		defaultMeasurer, defaultMeasurerErr = NewMeasurer()
	})
	if defaultMeasurerErr != nil {
		panic(fmt.Sprintf("initialize default text measurer: %v", defaultMeasurerErr))
	}
	return defaultMeasurer
}

// NewMeasurer constructs a Measurer from the embedded Inter-Regular and Inter-Bold fonts.
func NewMeasurer() (*Measurer, error) {
	return NewMeasurerWithFonts(fonts.Regular(), fonts.Bold())
}

// NewMeasurerWithFonts constructs a Measurer from raw TTF byte slices.
func NewMeasurerWithFonts(regularBytes, boldBytes []byte) (*Measurer, error) {
	regFont, err := sfnt.Parse(regularBytes)
	if err != nil {
		return nil, fmt.Errorf("parse regular ttf: %w", err)
	}

	boldFont, err := sfnt.Parse(boldBytes)
	if err != nil {
		return nil, fmt.Errorf("parse bold ttf: %w", err)
	}

	h := sha256.New()
	h.Write([]byte("Inter-Regular.ttf\x00"))
	h.Write(regularBytes)
	h.Write([]byte("Inter-Bold.ttf\x00"))
	h.Write(boldBytes)
	hashKey := hex.EncodeToString(h.Sum(nil))

	return &Measurer{
		regular:  regFont,
		bold:     boldFont,
		fontHash: hashKey,
	}, nil
}

// FontHash returns the content hash of the underlying font files.
func (m *Measurer) FontHash() string {
	return m.fontHash
}

func (m *Measurer) getFont(weight FontWeight) *sfnt.Font {
	if weight == WeightBold {
		return m.bold
	}
	return m.regular
}

// MeasureText computes the bounding width and line height of a single-line string.
func (m *Measurer) MeasureText(text string, fontSize float64, weight FontWeight) (width, height float64) {
	metrics, err := m.MeasureTextDetailed(text, fontSize, weight)
	if err != nil {
		// Fallback approximation if glyph lookup fails
		return float64(len(text)) * fontSize * 0.6, fontSize * 1.2
	}
	return metrics.Width, metrics.LineHeight
}

// MeasureTextDetailed returns comprehensive vertical and horizontal typography metrics.
func (m *Measurer) MeasureTextDetailed(text string, fontSize float64, weight FontWeight) (TextMetrics, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	f := m.getFont(weight)
	scale := fixed.Int26_6(math.Round(fontSize * 64))

	fm, err := f.Metrics(&m.buf, scale, font.HintingNone)
	if err != nil {
		return TextMetrics{}, fmt.Errorf("font metrics: %w", err)
	}

	ascent := float64(fm.Ascent) / 64.0
	descent := float64(fm.Descent) / 64.0
	lineHeight := float64(fm.Height) / 64.0
	if lineHeight <= 0 {
		lineHeight = ascent + descent
	}
	capHeight := float64(fm.CapHeight) / 64.0
	xHeight := float64(fm.XHeight) / 64.0

	var totalAdv fixed.Int26_6
	advances := make([]float64, 0, len(text))

	for _, r := range text {
		idx, err := f.GlyphIndex(&m.buf, r)
		if err != nil {
			idx = 0
		}
		adv, err := f.GlyphAdvance(&m.buf, idx, scale, font.HintingNone)
		if err != nil {
			adv = fixed.Int26_6(fontSize * 0.6 * 64)
		}
		totalAdv += adv
		advances = append(advances, float64(adv)/64.0)
	}

	return TextMetrics{
		Width:         float64(totalAdv) / 64.0,
		Height:        ascent + descent,
		Ascent:        ascent,
		Descent:       descent,
		CapHeight:     capHeight,
		XHeight:       xHeight,
		LineHeight:    lineHeight,
		GlyphAdvances: advances,
	}, nil
}

// BreakLines wraps text within maxWidth, respecting existing newlines.
func (m *Measurer) BreakLines(text string, maxWidth float64, fontSize float64, weight FontWeight) LineBreakResult {
	if text == "" {
		return LineBreakResult{
			Lines:        nil,
			MaxLineWidth: 0,
			TotalHeight:  0,
			LineHeights:  nil,
			LineSpacing:  4.0,
		}
	}

	metrics, _ := m.MeasureTextDetailed("Ag", fontSize, weight)
	lineHeight := metrics.LineHeight
	if lineHeight <= 0 {
		lineHeight = fontSize * 1.2
	}
	lineSpacing := math.Round(fontSize * 0.25)

	var resultLines []string
	var maxW float64

	paragraphs := strings.Split(text, "\n")
	for _, p := range paragraphs {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			resultLines = append(resultLines, "")
			continue
		}

		if maxWidth <= 0 {
			w, _ := m.MeasureText(trimmed, fontSize, weight)
			resultLines = append(resultLines, trimmed)
			if w > maxW {
				maxW = w
			}
			continue
		}

		words := strings.Fields(trimmed)
		if len(words) == 0 {
			resultLines = append(resultLines, "")
			continue
		}

		curLine := words[0]
		curW, _ := m.MeasureText(curLine, fontSize, weight)

		for _, word := range words[1:] {
			candidate := curLine + " " + word
			candW, _ := m.MeasureText(candidate, fontSize, weight)
			if candW <= maxWidth {
				curLine = candidate
				curW = candW
			} else {
				resultLines = append(resultLines, curLine)
				if curW > maxW {
					maxW = curW
				}
				curLine = word
				curW, _ = m.MeasureText(curLine, fontSize, weight)
			}
		}

		if curLine != "" {
			resultLines = append(resultLines, curLine)
			if curW > maxW {
				maxW = curW
			}
		}
	}

	numLines := len(resultLines)
	if numLines == 0 {
		numLines = 1
	}

	totalH := float64(numLines)*lineHeight + float64(numLines-1)*lineSpacing
	lineHeights := make([]float64, numLines)
	for i := range lineHeights {
		lineHeights[i] = lineHeight
	}

	return LineBreakResult{
		Lines:        resultLines,
		MaxLineWidth: maxW,
		TotalHeight:  totalH,
		LineHeights:  lineHeights,
		LineSpacing:  lineSpacing,
	}
}

// MeasureNode calculates the optimal width and height for a diagram node based on its shape and label.
func (m *Measurer) MeasureNode(label string, shape ir.NodeShape, opts NodeMeasureOptions) (width, height float64, lines []string) {
	if opts.FontSize <= 0 {
		opts.FontSize = 14.0
	}
	if opts.PaddingX <= 0 {
		opts.PaddingX = 20.0
	}
	if opts.PaddingY <= 0 {
		opts.PaddingY = 14.0
	}

	lb := m.BreakLines(label, opts.MaxWidth, opts.FontSize, opts.FontWeight)
	lines = lb.Lines

	textW := lb.MaxLineWidth
	textH := lb.TotalHeight

	rawW := textW + opts.PaddingX*2
	rawH := textH + opts.PaddingY*2

	switch shape {
	case ir.ShapeCircle:
		// Circle must enclose the bounding rectangle: diameter >= sqrt(w^2 + h^2)
		diag := math.Hypot(rawW, rawH)
		size := math.Max(diag, opts.MinWidth)
		return size, size, lines

	case ir.ShapeDiamond:
		// Rhombus: vertices are at (w/2, 0), (w, h/2), (w/2, h), (0, h/2).
		// An inner box of (rawW, rawH) requires bounding box (rawW * 1.5, rawH * 1.5)
		w := math.Max(rawW*1.5, opts.MinWidth)
		h := math.Max(rawH*1.5, opts.MinHeight)
		return w, h, lines

	case ir.ShapeCylinder:
		// Cylinder has top and bottom ellipse caps
		w := math.Max(rawW+10, opts.MinWidth)
		h := math.Max(rawH+24, opts.MinHeight+16)
		return w, h, lines

	case ir.ShapePill, ir.ShapeStadium:
		// Stadium has semicircular ends (rx = h / 2)
		h := math.Max(rawH, opts.MinHeight)
		w := math.Max(rawW+h/2, opts.MinWidth)
		return w, h, lines

	case ir.ShapeHexagon:
		// Hexagon has triangular left and right points
		h := math.Max(rawH, opts.MinHeight)
		w := math.Max(rawW+h*0.5, opts.MinWidth)
		return w, h, lines

	default: // ShapeRect, ShapeRound, etc.
		w := math.Max(rawW, opts.MinWidth)
		h := math.Max(rawH, opts.MinHeight)
		return w, h, lines
	}
}
