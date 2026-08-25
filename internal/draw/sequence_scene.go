package draw

import (
	"encoding/json"
	"fmt"

	"github.com/danieljustus/symaira-desktop/internal/draw/ir"
	"github.com/danieljustus/symaira-desktop/internal/draw/layout"
	"github.com/danieljustus/symaira-desktop/internal/draw/scene"
	"github.com/danieljustus/symaira-desktop/internal/draw/theme"
)

// seqActor is the JSON shape of a parsed sequence actor (mirrors
// parse.SequenceActor).
type seqActor struct {
	ID    string `json:"ID"`
	Label string `json:"Label"`
}

// seqMessage is the JSON shape of a parsed sequence message.
type seqMessage struct {
	From  string `json:"From"`
	To    string `json:"To"`
	Text  string `json:"Text"`
	Order int    `json:"Order"`
}

// buildSequenceScene renders a KindSequence diagram using the closed-form
// lifeline layout (issue #547): actor headers as columns, messages as
// ordered rows with solid/dashed arrows between lifelines.
func buildSequenceScene(d *ir.Diagram) (*scene.Scene, error) {
	lay, err := layout.SequenceLayoutFromIR(d)
	if err != nil {
		return nil, fmt.Errorf("sequence layout: %w", err)
	}

	actorsJSON, _ := json.Marshal(d.Custom["actors"])
	messagesJSON, _ := json.Marshal(d.Custom["messages"])
	var actors []seqActor
	var messages []seqMessage
	if err := json.Unmarshal(actorsJSON, &actors); err != nil {
		return nil, fmt.Errorf("decode actors: %w", err)
	}
	if err := json.Unmarshal(messagesJSON, &messages); err != nil {
		return nil, fmt.Errorf("decode messages: %w", err)
	}

	th := theme.Resolve(d.Theme)
	sc := scene.NewScene(lay.Width, lay.Height, th)

	// Actor headers + lifelines.
	for _, a := range actors {
		x := lay.ActorX[a.ID]
		// Header box.
		sc.Add(&scene.RectElement{
			X: x - 70, Y: 20, Width: 140, Height: 40, Rx: 8, Ry: 8,
			Fill: th.Surface, Stroke: th.Border,
		})
		sc.Add(&scene.TextElement{
			X: x, Y: 45, Text: a.Label, FontSize: 14,
			Anchor: scene.AnchorMiddle, Baseline: scene.BaselineMiddle,
			Fill: th.Text,
		})
		// Lifeline down the diagram.
		sc.Add(&scene.LineElement{
			X1: x, Y1: 70, X2: x, Y2: lay.Height - 40,
			Stroke: th.BorderSubtle, StrokeWidth: 1, DashArray: "4 4",
		})
	}

	// Messages as horizontal arrows between lifelines.
	for i, m := range messages {
		y := lay.MessageY[i]
		fromX := lay.ActorX[m.From]
		toX := lay.ActorX[m.To]
		if toX == 0 {
			toX = fromX
		}
		dashed := false
		sc.Add(&scene.LineElement{
			X1: fromX, Y1: y, X2: toX, Y2: y,
			Stroke: th.Edge, StrokeWidth: 1.5,
			DashArray: map[bool]string{true: "6 4"}[dashed],
		})
		if m.Text != "" {
			sc.Add(&scene.TextElement{
				X: (fromX + toX) / 2, Y: y - 8, Text: m.Text, FontSize: 12,
				Anchor: scene.AnchorMiddle, Baseline: scene.BaselineMiddle,
				Fill: th.TextMuted,
			})
		}
	}

	return sc, nil
}
