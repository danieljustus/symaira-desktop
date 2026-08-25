package parse

import (
	"fmt"
	"strings"

	"github.com/danieljustus/symaira-desktop/internal/draw/ir"
)

// SequenceActor is one lifeline in a sequence diagram.
type SequenceActor struct {
	ID    string
	Label string
}

// SequenceMessage is one directed message between two actors.
type SequenceMessage struct {
	From  string
	To    string
	Text  string
	Order int
}

// parseSequenceMermaid parses the supported sequenceDiagram subset:
//
//	sequenceDiagram
//	    participant Alice
//	    participant Bob
//	    Alice->>Bob: Hello Bob
//	    Bob-->>Alice: Hi Alice
//
// The parsed result is a KindSequence diagram whose Custom payload carries
// the ordered actors and messages; the sequence layout engine reads that
// payload (issue #547).
func parseSequenceMermaid(source string, headerLineNum int, rawLines []string) (*ir.Diagram, error) {
	diag := &ir.Diagram{
		Kind:    ir.KindSequence,
		Version: DialectVersion,
		Custom:  map[string]any{},
	}

	var actors []SequenceActor
	actorIndex := map[string]int{}
	messages := []SequenceMessage{}
	order := 0

	// Start after the header line; reuse the raw lines list so line numbers
	// stay accurate for error messages.
	start := headerLineNum
	for i := start; i < len(rawLines); i++ {
		trimmed := strings.TrimSpace(stripComment(rawLines[i]))
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		switch {
		case strings.HasPrefix(lower, "participant "):
			rest := strings.TrimSpace(trimmed[len("participant "):])
			id := rest
			label := rest
			if parts := strings.Fields(rest); len(parts) > 1 {
				id = parts[0]
				label = strings.Trim(parts[1], `"'`)
			}
			if _, exists := actorIndex[id]; !exists {
				actorIndex[id] = len(actors)
				actors = append(actors, SequenceActor{ID: id, Label: label})
			}
		case strings.HasPrefix(lower, "actor "):
			rest := strings.TrimSpace(trimmed[len("actor "):])
			id := rest
			label := rest
			if parts := strings.Fields(rest); len(parts) > 1 {
				id = parts[0]
				label = strings.Trim(parts[1], `"'`)
			}
			if _, exists := actorIndex[id]; !exists {
				actorIndex[id] = len(actors)
				actors = append(actors, SequenceActor{ID: id, Label: label})
			}
		default:
			// Message line: From Arrow To: text
			from, to, text, ok := parseSequenceMessageLine(trimmed)
			if !ok {
				return nil, NewUnsupportedConstructError(
					fmt.Sprintf("sequence line %q", trimmed),
					"Supported sequence syntax: participant <id>, actor <id>, and message lines '<from>->><to>: <text>'.",
					i+1,
					rawLines[i],
				)
			}
			for _, id := range []string{from, to} {
				if _, exists := actorIndex[id]; !exists {
					actorIndex[id] = len(actors)
					actors = append(actors, SequenceActor{ID: id, Label: id})
				}
			}
			messages = append(messages, SequenceMessage{From: from, To: to, Text: text, Order: order})
			order++
		}
	}

	if len(actors) == 0 {
		return nil, &ParseError{
			Stage:   "parse",
			Message: "sequence diagram has no participants",
			Hint:    "Declare at least one participant with 'participant <id>'.",
		}
	}

	diag.Custom["actors"] = actors
	diag.Custom["messages"] = messages
	return diag, nil
}

// parseSequenceMessageLine splits "Alice->>Bob: Hello" into from, to, text.
// The arrow must be one of the supported message arrows; the text after the
// colon is optional.
func parseSequenceMessageLine(line string) (from, to, text string, ok bool) {
	// Check the longer arrow first so "-->>" is not matched by the "->>"
	// substring inside it.
	arrowIdx := strings.Index(line, "-->>")
	arrowLen := 4
	if arrowIdx < 0 {
		arrowIdx = strings.Index(line, "->>")
		arrowLen = 3
	}
	if arrowIdx < 0 {
		return "", "", "", false
	}
	from = strings.TrimSpace(line[:arrowIdx])
	rest := line[arrowIdx+arrowLen:]
	to = strings.TrimSpace(rest)
	if colon := strings.Index(to, ":"); colon >= 0 {
		text = strings.TrimSpace(to[colon+1:])
		to = strings.TrimSpace(to[:colon])
	}
	if from == "" || to == "" {
		return "", "", "", false
	}
	return from, to, text, true
}
