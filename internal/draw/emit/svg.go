// Package emit provides deterministic SVG, pure-Go raster PNG, GIF, and Typst emitters
// over the single scene graph choke point.
package emit

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/danieljustus/symaira-desktop/internal/draw/measure"
	"github.com/danieljustus/symaira-desktop/internal/draw/scene"
)

// SVGEmitOptions configures the SVG serialization.
type SVGEmitOptions struct {
	TextAsPaths bool // Convert text to vector path outlines instead of <text> elements
	FontHash    string
	Generator   string
}

func formatFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', 2, 64)
}

func escapeXML(s string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}

func escapeAttr(s string) string {
	s = escapeXML(s)
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}

// EmitSVG serializes a scene.Scene into deterministic, byte-identical SVG XML bytes.
func EmitSVG(sc *scene.Scene, opts SVGEmitOptions) ([]byte, error) {
	if sc == nil {
		return nil, fmt.Errorf("scene is nil")
	}

	var sb strings.Builder

	// XML Header & SVG root tag
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf(
		`<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" viewBox="%s %s %s %s" width="%s" height="%s">`,
		formatFloat(sc.ViewBox.X),
		formatFloat(sc.ViewBox.Y),
		formatFloat(sc.ViewBox.Width),
		formatFloat(sc.ViewBox.Height),
		formatFloat(sc.Width),
		formatFloat(sc.Height),
	))
	sb.WriteString("\n")

	// Metadata
	fontHash := opts.FontHash
	if fontHash == "" && sc.Metadata != nil {
		fontHash = sc.Metadata["font-hash"]
	}
	if fontHash == "" {
		fontHash = measure.Default().FontHash()
	}

	generator := opts.Generator
	if generator == "" {
		generator = "symdraw"
	}

	sb.WriteString("  <metadata>\n")
	sb.WriteString(fmt.Sprintf("    <symdraw:font-hash>%s</symdraw:font-hash>\n", escapeXML(fontHash)))
	sb.WriteString(fmt.Sprintf("    <symdraw:generator>%s</symdraw:generator>\n", escapeXML(generator)))

	if len(sc.Metadata) > 0 {
		var metaKeys []string
		for k := range sc.Metadata {
			if k != "font-hash" && k != "generator" {
				metaKeys = append(metaKeys, k)
			}
		}
		sort.Strings(metaKeys)
		for _, k := range metaKeys {
			sb.WriteString(fmt.Sprintf("    <symdraw:meta name=\"%s\">%s</symdraw:meta>\n", escapeAttr(k), escapeXML(sc.Metadata[k])))
		}
	}
	sb.WriteString("  </metadata>\n")

	// Defs & Markers
	if len(sc.Markers) > 0 {
		sb.WriteString("  <defs>\n")
		for _, m := range sc.Markers {
			sb.WriteString(fmt.Sprintf(
				"    <marker id=\"%s\" viewBox=\"%s %s %s %s\" refX=\"%s\" refY=\"%s\" markerWidth=\"%s\" markerHeight=\"%s\" orient=\"%s\">\n",
				escapeAttr(m.ID),
				formatFloat(m.ViewBox.X),
				formatFloat(m.ViewBox.Y),
				formatFloat(m.ViewBox.Width),
				formatFloat(m.ViewBox.Height),
				formatFloat(m.RefX),
				formatFloat(m.RefY),
				formatFloat(m.MarkerWidth),
				formatFloat(m.MarkerHeight),
				escapeAttr(m.Orient),
			))
			if m.Path != nil {
				sb.WriteString("      ")
				writePrimitive(&sb, m.Path, opts, "      ")
				sb.WriteString("\n")
			}
			sb.WriteString("    </marker>\n")
		}
		sb.WriteString("  </defs>\n")
	}

	// Background
	if sc.Background != "" && sc.Background != "none" && sc.Background != "transparent" {
		sb.WriteString(fmt.Sprintf(
			"  <rect x=\"%s\" y=\"%s\" width=\"%s\" height=\"%s\" fill=\"%s\" />\n",
			formatFloat(sc.ViewBox.X),
			formatFloat(sc.ViewBox.Y),
			formatFloat(sc.ViewBox.Width),
			formatFloat(sc.ViewBox.Height),
			escapeAttr(sc.Background),
		))
	}

	// Primitives
	for _, p := range sc.Primitives {
		writePrimitiveWithLink(&sb, p, opts, "  ")
		sb.WriteString("\n")
	}

	sb.WriteString("</svg>\n")

	return []byte(sb.String()), nil
}

func writePrimitiveWithLink(sb *strings.Builder, p scene.Primitive, opts SVGEmitOptions, indent string) {
	link := getLink(p)
	if link != "" {
		sb.WriteString(fmt.Sprintf("%s<a href=\"%s\">\n", indent, escapeAttr(link)))
		writePrimitive(sb, p, opts, indent+"  ")
		sb.WriteString("\n")
		sb.WriteString(fmt.Sprintf("%s</a>", indent))
	} else {
		sb.WriteString(indent)
		writePrimitive(sb, p, opts, indent)
	}
}

func getLink(p scene.Primitive) string {
	switch el := p.(type) {
	case *scene.RectElement:
		return el.Link
	case *scene.CircleElement:
		return el.Link
	case *scene.EllipseElement:
		return el.Link
	case *scene.PathElement:
		return el.Link
	case *scene.TextElement:
		return el.Link
	case *scene.GroupElement:
		return el.Link
	default:
		return ""
	}
}

func writePrimitive(sb *strings.Builder, p scene.Primitive, opts SVGEmitOptions, indent string) {
	switch el := p.(type) {
	case *scene.RectElement:
		sb.WriteString("<rect")
		writeCommonAttrs(sb, el.ID, el.Class, el.Fill, el.Stroke, el.StrokeWidth, el.DashArray, el.Opacity)
		sb.WriteString(fmt.Sprintf(" x=\"%s\" y=\"%s\" width=\"%s\" height=\"%s\"",
			formatFloat(el.X), formatFloat(el.Y), formatFloat(el.Width), formatFloat(el.Height)))
		if el.Rx > 0 {
			sb.WriteString(fmt.Sprintf(" rx=\"%s\"", formatFloat(el.Rx)))
		}
		if el.Ry > 0 {
			sb.WriteString(fmt.Sprintf(" ry=\"%s\"", formatFloat(el.Ry)))
		}
		sb.WriteString(" />")

	case *scene.CircleElement:
		sb.WriteString("<circle")
		writeCommonAttrs(sb, el.ID, el.Class, el.Fill, el.Stroke, el.StrokeWidth, "", el.Opacity)
		sb.WriteString(fmt.Sprintf(" cx=\"%s\" cy=\"%s\" r=\"%s\"",
			formatFloat(el.CX), formatFloat(el.CY), formatFloat(el.R)))
		sb.WriteString(" />")

	case *scene.EllipseElement:
		sb.WriteString("<ellipse")
		writeCommonAttrs(sb, el.ID, el.Class, el.Fill, el.Stroke, el.StrokeWidth, "", el.Opacity)
		sb.WriteString(fmt.Sprintf(" cx=\"%s\" cy=\"%s\" rx=\"%s\" ry=\"%s\"",
			formatFloat(el.CX), formatFloat(el.CY), formatFloat(el.RX), formatFloat(el.RY)))
		sb.WriteString(" />")

	case *scene.LineElement:
		sb.WriteString("<line")
		writeCommonAttrs(sb, el.ID, el.Class, "", el.Stroke, el.StrokeWidth, el.DashArray, el.Opacity)
		sb.WriteString(fmt.Sprintf(" x1=\"%s\" y1=\"%s\" x2=\"%s\" y2=\"%s\"",
			formatFloat(el.X1), formatFloat(el.Y1), formatFloat(el.X2), formatFloat(el.Y2)))
		if el.MarkerStart != "" {
			sb.WriteString(fmt.Sprintf(" marker-start=\"url(#%s)\"", escapeAttr(el.MarkerStart)))
		}
		if el.MarkerEnd != "" {
			sb.WriteString(fmt.Sprintf(" marker-end=\"url(#%s)\"", escapeAttr(el.MarkerEnd)))
		}
		sb.WriteString(" />")

	case *scene.PolylineElement:
		sb.WriteString("<polyline")
		writeCommonAttrs(sb, el.ID, el.Class, el.Fill, el.Stroke, el.StrokeWidth, el.DashArray, el.Opacity)
		var pts []string
		for _, pt := range el.Points {
			pts = append(pts, fmt.Sprintf("%s,%s", formatFloat(pt.X), formatFloat(pt.Y)))
		}
		sb.WriteString(fmt.Sprintf(" points=\"%s\"", strings.Join(pts, " ")))
		if el.MarkerEnd != "" {
			sb.WriteString(fmt.Sprintf(" marker-end=\"url(#%s)\"", escapeAttr(el.MarkerEnd)))
		}
		sb.WriteString(" />")

	case *scene.PolygonElement:
		sb.WriteString("<polygon")
		writeCommonAttrs(sb, el.ID, el.Class, el.Fill, el.Stroke, el.StrokeWidth, el.DashArray, el.Opacity)
		var pts []string
		for _, pt := range el.Points {
			pts = append(pts, fmt.Sprintf("%s,%s", formatFloat(pt.X), formatFloat(pt.Y)))
		}
		sb.WriteString(fmt.Sprintf(" points=\"%s\"", strings.Join(pts, " ")))
		sb.WriteString(" />")

	case *scene.PathElement:
		sb.WriteString("<path")
		writeCommonAttrs(sb, el.ID, el.Class, el.Fill, el.Stroke, el.StrokeWidth, el.DashArray, el.Opacity)
		d := el.D
		if d == "" && len(el.Segments) > 0 {
			d = measure.SegmentsToSVGPath(el.Segments)
		}
		sb.WriteString(fmt.Sprintf(" d=\"%s\"", escapeAttr(d)))
		if el.MarkerEnd != "" {
			sb.WriteString(fmt.Sprintf(" marker-end=\"url(#%s)\"", escapeAttr(el.MarkerEnd)))
		}
		sb.WriteString(" />")

	case *scene.TextElement:
		if opts.TextAsPaths {
			writeTextAsPath(sb, el)
		} else {
			writeTextElement(sb, el)
		}

	case *scene.GroupElement:
		sb.WriteString("<g")
		if el.ID != "" {
			sb.WriteString(fmt.Sprintf(" id=\"%s\"", escapeAttr(el.ID)))
		}
		if el.Class != "" {
			sb.WriteString(fmt.Sprintf(" class=\"%s\"", escapeAttr(el.Class)))
		}
		if el.Transform != "" {
			sb.WriteString(fmt.Sprintf(" transform=\"%s\"", escapeAttr(el.Transform)))
		}
		sb.WriteString(">\n")
		for _, child := range el.Children {
			writePrimitiveWithLink(sb, child, opts, indent+"  ")
			sb.WriteString("\n")
		}
		sb.WriteString(indent)
		sb.WriteString("</g>")
	}
}

func writeCommonAttrs(sb *strings.Builder, id, class, fill, stroke string, strokeWidth float64, dashArray string, opacity float64) {
	if id != "" {
		sb.WriteString(fmt.Sprintf(" id=\"%s\"", escapeAttr(id)))
	}
	if class != "" {
		sb.WriteString(fmt.Sprintf(" class=\"%s\"", escapeAttr(class)))
	}
	if fill != "" {
		sb.WriteString(fmt.Sprintf(" fill=\"%s\"", escapeAttr(fill)))
	} else if stroke != "" {
		sb.WriteString(" fill=\"none\"")
	}
	if stroke != "" {
		sb.WriteString(fmt.Sprintf(" stroke=\"%s\"", escapeAttr(stroke)))
	}
	if strokeWidth > 0 {
		sb.WriteString(fmt.Sprintf(" stroke-width=\"%s\"", formatFloat(strokeWidth)))
	}
	if dashArray != "" {
		sb.WriteString(fmt.Sprintf(" stroke-dasharray=\"%s\"", escapeAttr(dashArray)))
	}
	if opacity > 0 && opacity < 1 {
		sb.WriteString(fmt.Sprintf(" opacity=\"%s\"", formatFloat(opacity)))
	}
}

func writeTextElement(sb *strings.Builder, el *scene.TextElement) {
	sb.WriteString("<text")
	if el.ID != "" {
		sb.WriteString(fmt.Sprintf(" id=\"%s\"", escapeAttr(el.ID)))
	}
	if el.Class != "" {
		sb.WriteString(fmt.Sprintf(" class=\"%s\"", escapeAttr(el.Class)))
	}

	fontFamily := el.FontFamily
	if fontFamily == "" {
		fontFamily = "Inter, -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif"
	}
	fontSize := el.FontSize
	if fontSize <= 0 {
		fontSize = 14.0
	}
	fill := el.Fill
	if fill == "" {
		fill = "#F5F4F0"
	}

	sb.WriteString(fmt.Sprintf(" x=\"%s\" y=\"%s\"", formatFloat(el.X), formatFloat(el.Y)))
	sb.WriteString(fmt.Sprintf(" font-family=\"%s\" font-size=\"%s\"", escapeAttr(fontFamily), formatFloat(fontSize)))

	if el.FontWeight == measure.WeightBold {
		sb.WriteString(" font-weight=\"bold\"")
	}
	sb.WriteString(fmt.Sprintf(" fill=\"%s\"", escapeAttr(fill)))

	if el.Anchor != "" && el.Anchor != scene.AnchorStart {
		sb.WriteString(fmt.Sprintf(" text-anchor=\"%s\"", escapeAttr(string(el.Anchor))))
	}
	if el.Baseline != "" && el.Baseline != scene.BaselineAlphabetic {
		sb.WriteString(fmt.Sprintf(" dominant-baseline=\"%s\"", escapeAttr(string(el.Baseline))))
	}

	textLen := el.TextLength
	if textLen <= 0 {
		textLen, _ = measure.Default().MeasureText(el.Text, fontSize, el.FontWeight)
	}
	if textLen > 0 {
		sb.WriteString(fmt.Sprintf(" textLength=\"%s\"", formatFloat(textLen)))
	}

	if el.Opacity > 0 && el.Opacity < 1 {
		sb.WriteString(fmt.Sprintf(" opacity=\"%s\"", formatFloat(el.Opacity)))
	}

	sb.WriteString(">")
	sb.WriteString(escapeXML(el.Text))
	sb.WriteString("</text>")
}

func writeTextAsPath(sb *strings.Builder, el *scene.TextElement) {
	fontSize := el.FontSize
	if fontSize <= 0 {
		fontSize = 14.0
	}
	fill := el.Fill
	if fill == "" {
		fill = "#F5F4F0"
	}

	// Adjust X based on Anchor
	w := el.TextLength
	if w <= 0 {
		w, _ = measure.Default().MeasureText(el.Text, fontSize, el.FontWeight)
	}

	startX := el.X
	switch el.Anchor {
	case scene.AnchorMiddle:
		startX = el.X - w/2.0
	case scene.AnchorEnd:
		startX = el.X - w
	}

	// Adjust Y based on Baseline
	startY := el.Y
	metrics, _ := measure.Default().MeasureTextDetailed("Ag", fontSize, el.FontWeight)
	switch el.Baseline {
	case scene.BaselineMiddle, scene.BaselineCentral:
		startY = el.Y + metrics.LineHeight/2.0 - metrics.Descent
	case scene.BaselineHanging, scene.BaselineTop:
		startY = el.Y + metrics.Ascent
	}

	segs, err := measure.Default().Outlines(el.Text, fontSize, el.FontWeight, startX, startY)
	if err != nil || len(segs) == 0 {
		writeTextElement(sb, el)
		return
	}

	d := measure.SegmentsToSVGPath(segs)
	sb.WriteString(fmt.Sprintf("<path fill=\"%s\" d=\"%s\"", escapeAttr(fill), escapeAttr(d)))
	if el.ID != "" {
		sb.WriteString(fmt.Sprintf(" id=\"%s\"", escapeAttr(el.ID)))
	}
	if el.Class != "" {
		sb.WriteString(fmt.Sprintf(" class=\"%s\"", escapeAttr(el.Class)))
	}
	if el.Opacity > 0 && el.Opacity < 1 {
		sb.WriteString(fmt.Sprintf(" opacity=\"%s\"", formatFloat(el.Opacity)))
	}
	sb.WriteString(" />")
}
