// Package ir defines the input-independent diagram model, JSON schema,
// and validation logic for SymDraw diagrams.
package ir

// DiagramKind identifies the high-level diagram kind.
type DiagramKind string

const (
	KindGraph    DiagramKind = "graph"
	KindSequence DiagramKind = "sequence"
	KindTimeline DiagramKind = "timeline"
	KindTree     DiagramKind = "tree"
	KindChart    DiagramKind = "chart"
	KindCustom   DiagramKind = "custom"
)

// Direction indicates the primary flow direction of the diagram.
type Direction string

const (
	DirTD Direction = "TD" // Top-Down (synonym: TB)
	DirTB Direction = "TB" // Top-Bottom
	DirBT Direction = "BT" // Bottom-Top
	DirLR Direction = "LR" // Left-Right
	DirRL Direction = "RL" // Right-Left
)

// NodeShape defines the visual geometry of a node.
type NodeShape string

const (
	ShapeRect       NodeShape = "rect"
	ShapeRound      NodeShape = "round"
	ShapeCircle     NodeShape = "circle"
	ShapeCylinder   NodeShape = "cylinder"
	ShapeDiamond    NodeShape = "diamond"
	ShapePill       NodeShape = "pill"
	ShapeStadium    NodeShape = "stadium"
	ShapeSubroutine NodeShape = "subroutine"
	ShapeHexagon    NodeShape = "hexagon"
	ShapeAsymmetric NodeShape = "asymmetric"
)

// EdgeStyle defines line dashing/stroke style.
type EdgeStyle string

const (
	EdgeSolid  EdgeStyle = "solid"
	EdgeDashed EdgeStyle = "dashed"
	EdgeDotted EdgeStyle = "dotted"
	EdgeThick  EdgeStyle = "thick"
)

// ArrowType specifies arrowhead geometry at edge endpoints.
type ArrowType string

const (
	ArrowNone   ArrowType = "none"
	ArrowSingle ArrowType = "single"
	ArrowDouble ArrowType = "double"
	ArrowCross  ArrowType = "cross"
	ArrowCircle ArrowType = "circle"
)

// NodeStyle specifies optional style overrides for a node.
type NodeStyle struct {
	Fill        string   `json:"fill,omitempty"`
	Stroke      string   `json:"stroke,omitempty"`
	StrokeWidth *float64 `json:"stroke_width,omitempty"`
	TextColor   string   `json:"text_color,omitempty"`
	Opacity     *float64 `json:"opacity,omitempty"`
	DashArray   string   `json:"dash_array,omitempty"`
}

// Node represents an entity in the diagram.
type Node struct {
	ID     string    `json:"id"`
	Label  string    `json:"label,omitempty"`
	Shape  NodeShape `json:"shape,omitempty"`
	Note   string    `json:"note,omitempty"` // Vault wikilink/path
	Icon   string    `json:"icon,omitempty"`
	Style  NodeStyle `json:"style,omitempty"`
	Width  float64   `json:"width,omitempty"`
	Height float64   `json:"height,omitempty"`
	X      float64   `json:"x,omitempty"`
	Y      float64   `json:"y,omitempty"`
}

// Edge represents a directed or undirected connection between two nodes.
type Edge struct {
	From  string    `json:"from"`
	To    string    `json:"to"`
	Label string    `json:"label,omitempty"`
	Style EdgeStyle `json:"style,omitempty"`
	Arrow ArrowType `json:"arrow,omitempty"`
	Color string    `json:"color,omitempty"`
}

// GroupStyle specifies styling for a container group.
type GroupStyle struct {
	Fill        string   `json:"fill,omitempty"`
	Stroke      string   `json:"stroke,omitempty"`
	StrokeWidth *float64 `json:"stroke_width,omitempty"`
}

// Group represents a bounding box container grouping multiple nodes.
type Group struct {
	ID      string     `json:"id,omitempty"`
	Label   string     `json:"label,omitempty"`
	Members []string   `json:"members"`
	Style   GroupStyle `json:"style,omitempty"`
}

// ChartType specifies the rendering style of a chart.
type ChartType string

const (
	ChartBar     ChartType = "bar"
	ChartLine    ChartType = "line"
	ChartPie     ChartType = "pie"
	ChartScatter ChartType = "scatter"
	ChartArea    ChartType = "area"
	ChartDonut   ChartType = "donut"
)

// DataPoint represents a single data point in a series.
type DataPoint struct {
	X     float64 `json:"x,omitempty"`
	Y     float64 `json:"y"`
	Label string  `json:"label,omitempty"`
	Color string  `json:"color,omitempty"`
}

// Series represents one dataset in a chart.
type Series struct {
	Name  string      `json:"name"`
	Data  []DataPoint `json:"data"`
	Color string      `json:"color,omitempty"`
}

// AxisSpec specifies chart axis configuration.
type AxisSpec struct {
	Title  string   `json:"title,omitempty"`
	Labels []string `json:"labels,omitempty"`
	Min    *float64 `json:"min,omitempty"`
	Max    *float64 `json:"max,omitempty"`
}

// ChartSpec defines configuration for chart diagrams.
type ChartSpec struct {
	Type   ChartType `json:"type"`
	Title  string    `json:"title,omitempty"`
	Series []Series  `json:"series"`
	XAxis  AxisSpec  `json:"x_axis,omitempty"`
	YAxis  AxisSpec  `json:"y_axis,omitempty"`
	Legend bool      `json:"legend,omitempty"`
}

// Diagram represents a fully parsed, input-independent diagram model.
type Diagram struct {
	Version   string         `json:"version,omitempty"`
	Kind      DiagramKind    `json:"kind"`
	Title     string         `json:"title,omitempty"`
	Direction Direction      `json:"direction,omitempty"`
	Theme     string         `json:"theme,omitempty"`
	Width     float64        `json:"width,omitempty"`
	Height    float64        `json:"height,omitempty"`
	Nodes     []Node         `json:"nodes,omitempty"`
	Edges     []Edge         `json:"edges,omitempty"`
	Groups    []Group        `json:"groups,omitempty"`
	Chart     *ChartSpec     `json:"chart,omitempty"`
	Custom    map[string]any `json:"custom,omitempty"`
}
