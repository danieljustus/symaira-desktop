package layout

import (
	"encoding/json"
	"fmt"

	"github.com/danieljustus/symaira-desktop/internal/draw/ir"
	"github.com/danieljustus/symaira-desktop/internal/draw/measure"
)

// SequenceLayout is the closed-form layout for sequence diagrams (issue
// #547): lifelines are columns, messages are ordered rows. No graph
// algorithm is needed — the positioning is deterministic.
type SequenceLayout struct {
	// ActorX maps actor id to the center x of its lifeline column.
	ActorX map[string]float64
	// MessageY maps the message order index to its row y.
	MessageY []float64
	// Width and Height bound the diagram.
	Width  float64
	Height float64
}

const (
	seqColGap  = 180.0
	seqRowGap  = 48.0
	seqMarginX = 120.0
	seqMarginY = 80.0
	seqHeaderH = 60.0
)

// SequenceActor mirrors the parser's actor payload. The Custom payload
// round-trips through JSON, so the struct fields must match the parser's.
type SequenceActor struct {
	ID    string
	Label string
}

// SequenceMessage mirrors the parser's message payload.
type SequenceMessage struct {
	From  string
	To    string
	Text  string
	Order int
}

// SequenceLayoutFromIR computes the closed-form layout from a parsed
// sequence diagram's Custom payload (actors + messages).
func SequenceLayoutFromIR(d *ir.Diagram) (*SequenceLayout, error) {
	if d == nil || d.Kind != ir.KindSequence {
		return nil, fmt.Errorf("sequence layout requires sequence kind, got %T", d)
	}
	rawActors, ok := d.Custom["actors"]
	if !ok {
		return nil, fmt.Errorf("sequence diagram missing actors")
	}
	rawMessages, ok := d.Custom["messages"]
	if !ok {
		return nil, fmt.Errorf("sequence diagram missing messages")
	}

	actorsJSON, err := json.Marshal(rawActors)
	if err != nil {
		return nil, fmt.Errorf("encode actors: %w", err)
	}
	messagesJSON, err := json.Marshal(rawMessages)
	if err != nil {
		return nil, fmt.Errorf("encode messages: %w", err)
	}
	var actors []SequenceActor
	var messages []SequenceMessage
	if err := json.Unmarshal(actorsJSON, &actors); err != nil {
		return nil, fmt.Errorf("decode actors: %w", err)
	}
	if err := json.Unmarshal(messagesJSON, &messages); err != nil {
		return nil, fmt.Errorf("decode messages: %w", err)
	}

	actorX := make(map[string]float64, len(actors))
	for i, a := range actors {
		actorX[a.ID] = seqMarginX + float64(i)*seqColGap
	}
	messageY := make([]float64, len(messages))
	for i := range messages {
		messageY[i] = seqMarginY + seqHeaderH + float64(i)*seqRowGap
	}

	width := seqMarginX*2 + float64(maxInt(1, len(actors)-1))*seqColGap
	height := seqMarginY + seqHeaderH + float64(len(messages))*seqRowGap + seqMarginY

	return &SequenceLayout{
		ActorX:   actorX,
		MessageY: messageY,
		Width:    width,
		Height:   height,
	}, nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

var _ = measure.Point{} // keep measure import for future edge routing
