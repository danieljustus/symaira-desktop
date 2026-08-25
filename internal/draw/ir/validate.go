package ir

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ValidationError represents an error encountered while validating an IR diagram.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("invalid ir field %q: %s", e.Field, e.Message)
}

// Validate checks the diagram against structural and semantic rules.
func Validate(d *Diagram) error {
	if d == nil {
		return errors.New("diagram is nil")
	}

	switch d.Kind {
	case KindGraph, KindSequence, KindTimeline, KindTree, KindChart, KindCustom:
		// valid
	case "":
		return &ValidationError{Field: "kind", Message: "diagram kind is required"}
	default:
		return &ValidationError{Field: "kind", Message: fmt.Sprintf("unsupported kind %q", d.Kind)}
	}

	if d.Direction != "" {
		switch d.Direction {
		case DirTD, DirTB, DirBT, DirLR, DirRL:
			// valid
		default:
			return &ValidationError{Field: "direction", Message: fmt.Sprintf("unsupported direction %q", d.Direction)}
		}
	}

	nodeIDs := make(map[string]bool, len(d.Nodes))
	for i, node := range d.Nodes {
		id := strings.TrimSpace(node.ID)
		if id == "" {
			return &ValidationError{Field: fmt.Sprintf("nodes[%d].id", i), Message: "node id cannot be empty"}
		}
		if nodeIDs[id] {
			return &ValidationError{Field: fmt.Sprintf("nodes[%d].id", i), Message: fmt.Sprintf("duplicate node id %q", id)}
		}
		nodeIDs[id] = true

		if node.Shape != "" {
			switch node.Shape {
			case ShapeRect, ShapeRound, ShapeCircle, ShapeCylinder, ShapeDiamond,
				ShapePill, ShapeStadium, ShapeSubroutine, ShapeHexagon, ShapeAsymmetric:
				// valid
			default:
				return &ValidationError{Field: fmt.Sprintf("nodes[%d].shape", i), Message: fmt.Sprintf("unsupported shape %q", node.Shape)}
			}
		}

		if node.Width < 0 || node.Height < 0 {
			return &ValidationError{Field: fmt.Sprintf("nodes[%d]", i), Message: "width and height cannot be negative"}
		}
	}

	for i, edge := range d.Edges {
		from := strings.TrimSpace(edge.From)
		to := strings.TrimSpace(edge.To)
		if from == "" {
			return &ValidationError{Field: fmt.Sprintf("edges[%d].from", i), Message: "edge from cannot be empty"}
		}
		if to == "" {
			return &ValidationError{Field: fmt.Sprintf("edges[%d].to", i), Message: "edge to cannot be empty"}
		}

		if len(nodeIDs) > 0 {
			if !nodeIDs[from] {
				return &ValidationError{Field: fmt.Sprintf("edges[%d].from", i), Message: fmt.Sprintf("referenced node %q does not exist", from)}
			}
			if !nodeIDs[to] {
				return &ValidationError{Field: fmt.Sprintf("edges[%d].to", i), Message: fmt.Sprintf("referenced node %q does not exist", to)}
			}
		}

		if edge.Style != "" {
			switch edge.Style {
			case EdgeSolid, EdgeDashed, EdgeDotted, EdgeThick:
				// valid
			default:
				return &ValidationError{Field: fmt.Sprintf("edges[%d].style", i), Message: fmt.Sprintf("unsupported edge style %q", edge.Style)}
			}
		}

		if edge.Arrow != "" {
			switch edge.Arrow {
			case ArrowNone, ArrowSingle, ArrowDouble, ArrowCross, ArrowCircle:
				// valid
			default:
				return &ValidationError{Field: fmt.Sprintf("edges[%d].arrow", i), Message: fmt.Sprintf("unsupported arrow type %q", edge.Arrow)}
			}
		}
	}

	for i, group := range d.Groups {
		if len(group.Members) == 0 {
			return &ValidationError{Field: fmt.Sprintf("groups[%d].members", i), Message: "group must have at least one member"}
		}
		if len(nodeIDs) > 0 {
			for _, m := range group.Members {
				if !nodeIDs[m] {
					return &ValidationError{Field: fmt.Sprintf("groups[%d].members", i), Message: fmt.Sprintf("grouped node %q does not exist", m)}
				}
			}
		}
	}

	if d.Kind == KindChart || d.Chart != nil {
		if d.Chart == nil {
			return &ValidationError{Field: "chart", Message: "chart specification is required for chart diagram kind"}
		}
		switch d.Chart.Type {
		case ChartBar, ChartLine, ChartPie, ChartScatter, ChartArea, ChartDonut:
			// valid
		default:
			return &ValidationError{Field: "chart.type", Message: fmt.Sprintf("unsupported chart type %q", d.Chart.Type)}
		}
		if len(d.Chart.Series) == 0 {
			return &ValidationError{Field: "chart.series", Message: "chart must have at least one series"}
		}
	}

	return nil
}

// FromJSON parses a JSON payload into a Diagram and validates it.
func FromJSON(data []byte) (*Diagram, error) {
	var d Diagram
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("unmarshal ir diagram: %w", err)
	}
	if err := Validate(&d); err != nil {
		return nil, err
	}
	return &d, nil
}

// ToJSON serializes a Diagram into pretty-formatted deterministic JSON.
func ToJSON(d *Diagram) ([]byte, error) {
	if err := Validate(d); err != nil {
		return nil, err
	}
	return json.MarshalIndent(d, "", "  ")
}
