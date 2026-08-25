// Package layout contains deterministic, offline diagram layout engines.
package layout

import (
	"fmt"
	"sort"

	"github.com/danieljustus/symaira-desktop/internal/draw/ir"
	"github.com/danieljustus/symaira-desktop/internal/draw/measure"
)

const (
	nodeGap  = 32.0
	layerGap = 72.0
	margin   = 40.0
)

// Box is the positioned rectangle for a graph node. Dummy nodes have Dummy set.
type Box struct {
	X, Y          float64
	Width, Height float64
	Layer         int
	Order         int
	Dummy         bool
}

// Route is an edge path in source-to-target order. Long edges contain points
// through their inserted dummy nodes; self-loops contain a deterministic loop.
type Route struct {
	From         string
	To           string
	Points       []measure.Point
	OriginalEdge int
	Reversed     bool
	SelfLoop     bool
}

// Result is the complete deterministic output of the layered layout engine.
type Result struct {
	Nodes     map[string]Box
	Dummies   map[string]Box
	Routes    []Route
	Layers    [][]string
	Crossings int
	Width     float64
	Height    float64
}

type workEdge struct {
	index    int
	from     string
	to       string
	reversed bool
}

type segment struct {
	from, to string
	edge     int
}

// Layout positions a directed graph using a deterministic Sugiyama-style
// pipeline: cycle breaking, longest-path layering, dummy insertion, median
// crossing reduction, balanced coordinates, and orthogonal-ish edge routes.
func Layout(d *ir.Diagram) (*Result, error) {
	if d == nil {
		return nil, fmt.Errorf("diagram is nil")
	}
	if d.Kind != ir.KindGraph {
		return nil, fmt.Errorf("layered layout requires graph kind, got %q", d.Kind)
	}

	nodeIDs := make([]string, 0, len(d.Nodes))
	nodes := make(map[string]ir.Node, len(d.Nodes))
	for _, n := range d.Nodes {
		if n.ID == "" {
			return nil, fmt.Errorf("graph node id cannot be empty")
		}
		if _, exists := nodes[n.ID]; exists {
			return nil, fmt.Errorf("duplicate graph node %q", n.ID)
		}
		nodes[n.ID] = n
		nodeIDs = append(nodeIDs, n.ID)
	}
	sort.Strings(nodeIDs)

	edges := make([]workEdge, len(d.Edges))
	for i, e := range d.Edges {
		if _, ok := nodes[e.From]; !ok {
			return nil, fmt.Errorf("edge %d references unknown source %q", i, e.From)
		}
		if _, ok := nodes[e.To]; !ok {
			return nil, fmt.Errorf("edge %d references unknown target %q", i, e.To)
		}
		edges[i] = workEdge{index: i, from: e.From, to: e.To}
	}

	breakCycles(nodeIDs, edges)
	order := topologicalOrder(nodeIDs, edges)
	layersByNode := assignLayers(order, edges)
	layerNodes := makeLayers(nodeIDs, layersByNode)

	// Split every edge that spans a layer into a chain through dummy nodes.
	segments := make([]segment, 0, len(edges))
	chains := make([][]string, len(edges))
	dummies := make(map[string]Box)
	for _, e := range edges {
		if e.from == e.to {
			chains[e.index] = []string{e.from}
			continue
		}
		layoutFrom, layoutTo := edgeDirection(e)
		start, end := layersByNode[layoutFrom], layersByNode[layoutTo]
		chain := []string{layoutFrom}
		for layer := start + 1; layer < end; layer++ {
			id := fmt.Sprintf("__dummy_%d_%d", e.index, layer)
			chain = append(chain, id)
			layerNodes[layer] = append(layerNodes[layer], id)
			dummies[id] = Box{Layer: layer, Dummy: true, Width: 1, Height: 1}
		}
		chain = append(chain, layoutTo)
		chains[e.index] = chain
		for i := 0; i+1 < len(chain); i++ {
			segments = append(segments, segment{from: chain[i], to: chain[i+1], edge: e.index})
		}
	}

	for i := range layerNodes {
		sort.Strings(layerNodes[i])
	}
	reduceCrossings(layerNodes, segments)

	result := &Result{
		Nodes:   make(map[string]Box, len(nodes)),
		Dummies: dummies,
		Routes:  make([]Route, len(edges)),
		Layers:  cloneLayers(layerNodes),
	}
	assignCoordinates(result, layerNodes, nodes, d.Direction)
	for id, box := range result.Dummies {
		if assigned, ok := result.Nodes[id]; ok {
			result.Dummies[id] = assigned
		} else {
			result.Dummies[id] = box
		}
	}
	for _, e := range edges {
		result.Routes[e.index] = makeRoute(e, chains[e.index], result.Nodes, d.Direction)
	}
	result.Crossings = countCrossings(layerNodes, segments)
	return result, nil
}

func breakCycles(nodeIDs []string, edges []workEdge) {
	out := make(map[string][]int, len(nodeIDs))
	for i, e := range edges {
		out[e.from] = append(out[e.from], i)
	}
	for id := range out {
		sort.SliceStable(out[id], func(i, j int) bool {
			a, b := edges[out[id][i]], edges[out[id][j]]
			if a.to != b.to {
				return a.to < b.to
			}
			return a.index < b.index
		})
	}
	state := make(map[string]uint8, len(nodeIDs))
	var visit func(string)
	visit = func(id string) {
		state[id] = 1
		for _, edgeIndex := range out[id] {
			e := &edges[edgeIndex]
			switch state[e.to] {
			case 0:
				visit(e.to)
			case 1:
				e.reversed = true
			}
		}
		state[id] = 2
	}
	for _, id := range nodeIDs {
		if state[id] == 0 {
			visit(id)
		}
	}
}

func edgeDirection(e workEdge) (string, string) {
	if e.reversed {
		return e.to, e.from
	}
	return e.from, e.to
}

func topologicalOrder(nodeIDs []string, edges []workEdge) []string {
	for attempts := 0; attempts <= len(edges); attempts++ {
		indegree := make(map[string]int, len(nodeIDs))
		out := make(map[string][]string, len(nodeIDs))
		for _, id := range nodeIDs {
			indegree[id] = 0
		}
		for _, e := range edges {
			from, to := edgeDirection(e)
			if from == to {
				continue
			}
			indegree[to]++
			out[from] = append(out[from], to)
		}
		for id := range out {
			sort.Strings(out[id])
		}
		ready := make([]string, 0)
		for _, id := range nodeIDs {
			if indegree[id] == 0 {
				ready = append(ready, id)
			}
		}
		order := make([]string, 0, len(nodeIDs))
		for len(ready) > 0 {
			id := ready[0]
			ready = ready[1:]
			order = append(order, id)
			for _, next := range out[id] {
				indegree[next]--
				if indegree[next] == 0 {
					ready = append(ready, next)
					sort.Strings(ready)
				}
			}
		}
		if len(order) == len(nodeIDs) {
			return order
		}
		// Defensive fallback for an unexpected cycle after DFS cycle breaking.
		// Reversing the first remaining edge makes progress and is deterministic.
		for i := range edges {
			from, to := edgeDirection(edges[i])
			if indegree[from] > 0 && indegree[to] > 0 {
				edges[i].reversed = !edges[i].reversed
				break
			}
		}
	}
	return append([]string(nil), nodeIDs...)
}

func assignLayers(order []string, edges []workEdge) map[string]int {
	layers := make(map[string]int, len(order))
	for _, id := range order {
		if _, ok := layers[id]; !ok {
			layers[id] = 0
		}
		for _, e := range edges {
			from, to := edgeDirection(e)
			if from == id && from != to && layers[to] < layers[from]+1 {
				layers[to] = layers[from] + 1
			}
		}
	}
	return layers
}

func makeLayers(nodeIDs []string, layers map[string]int) [][]string {
	maxLayer := 0
	for _, id := range nodeIDs {
		if layers[id] > maxLayer {
			maxLayer = layers[id]
		}
	}
	result := make([][]string, maxLayer+1)
	for _, id := range nodeIDs {
		result[layers[id]] = append(result[layers[id]], id)
	}
	return result
}

func cloneLayers(layers [][]string) [][]string {
	out := make([][]string, len(layers))
	for i := range layers {
		out[i] = append([]string(nil), layers[i]...)
	}
	return out
}

func reduceCrossings(layers [][]string, segments []segment) {
	for iteration := 0; iteration < 8; iteration++ {
		for layer := 1; layer < len(layers); layer++ {
			medianOrder(layers, segments, layer, false)
		}
		for layer := len(layers) - 2; layer >= 0; layer-- {
			medianOrder(layers, segments, layer, true)
		}
		transpose(layers, segments)
	}
}

func medianOrder(layers [][]string, segments []segment, layer int, lookDown bool) {
	if len(layers[layer]) < 2 {
		return
	}
	positions := make(map[string]int, len(layers[layer]))
	for i, id := range layers[layer] {
		positions[id] = i
	}
	neighborPositions := make(map[string]int)
	if lookDown {
		for i, id := range layers[layer+1] {
			neighborPositions[id] = i
		}
	} else {
		for i, id := range layers[layer-1] {
			neighborPositions[id] = i
		}
	}
	bary := func(id string) (float64, bool) {
		values := make([]int, 0)
		for _, s := range segments {
			if lookDown && s.from == id {
				if p, ok := neighborPositions[s.to]; ok {
					values = append(values, p)
				}
			} else if !lookDown && s.to == id {
				if p, ok := neighborPositions[s.from]; ok {
					values = append(values, p)
				}
			}
		}
		if len(values) == 0 {
			return 0, false
		}
		sort.Ints(values)
		return float64(values[len(values)/2]), true
	}
	sort.SliceStable(layers[layer], func(i, j int) bool {
		a, aok := bary(layers[layer][i])
		b, bok := bary(layers[layer][j])
		if !aok || !bok {
			return positions[layers[layer][i]] < positions[layers[layer][j]]
		}
		if a != b {
			return a < b
		}
		return layers[layer][i] < layers[layer][j]
	})
}

func transpose(layers [][]string, segments []segment) {
	for layer := range layers {
		changed := true
		for changed {
			changed = false
			for i := 0; i+1 < len(layers[layer]); i++ {
				before := countCrossings(layers, segments)
				layers[layer][i], layers[layer][i+1] = layers[layer][i+1], layers[layer][i]
				after := countCrossings(layers, segments)
				if after < before {
					changed = true
				} else {
					layers[layer][i], layers[layer][i+1] = layers[layer][i+1], layers[layer][i]
				}
			}
		}
	}
}

func countCrossings(layers [][]string, segments []segment) int {
	positions := make([]map[string]int, len(layers))
	for i, layer := range layers {
		positions[i] = make(map[string]int, len(layer))
		for j, id := range layer {
			positions[i][id] = j
		}
	}
	count := 0
	for layer := 0; layer+1 < len(layers); layer++ {
		pairs := make([][2]int, 0)
		for _, s := range segments {
			fromLayer, fromOK := findLayer(positions, s.from)
			toLayer, toOK := findLayer(positions, s.to)
			if !fromOK || !toOK || fromLayer != layer || toLayer != layer+1 {
				continue
			}
			pairs = append(pairs, [2]int{positions[layer][s.from], positions[layer+1][s.to]})
		}
		for i := range pairs {
			for j := i + 1; j < len(pairs); j++ {
				if (pairs[i][0] < pairs[j][0] && pairs[i][1] > pairs[j][1]) ||
					(pairs[i][0] > pairs[j][0] && pairs[i][1] < pairs[j][1]) {
					count++
				}
			}
		}
	}
	return count
}

func findLayer(positions []map[string]int, id string) (int, bool) {
	for layer, position := range positions {
		if _, ok := position[id]; ok {
			return layer, true
		}
	}
	return 0, false
}

func assignCoordinates(result *Result, layers [][]string, nodes map[string]ir.Node, direction ir.Direction) {
	measurer := measure.Default()
	options := measure.DefaultNodeMeasureOptions()
	widths := make(map[string]float64, len(nodes))
	heights := make(map[string]float64, len(nodes))
	for id, node := range nodes {
		w, h, _ := measurer.MeasureNode(node.Label, node.Shape, options)
		if node.Width > 0 {
			w = node.Width
		}
		if node.Height > 0 {
			h = node.Height
		}
		widths[id], heights[id] = w, h
	}

	horizontal := direction == ir.DirLR || direction == ir.DirRL
	layerSpans := make([]float64, len(layers))
	layerSizes := make([]float64, len(layers))
	maxSpan := 0.0
	for i, layer := range layers {
		span, size := 0.0, 0.0
		for _, id := range layer {
			w, h := widths[id], heights[id]
			if w == 0 {
				w, h = 1, 1
			}
			if horizontal {
				span += h
				size = max(size, w)
			} else {
				span += w
				size = max(size, h)
			}
		}
		if len(layer) > 1 {
			span += float64(len(layer)-1) * nodeGap
		}
		layerSpans[i], layerSizes[i] = span, size
		maxSpan = max(maxSpan, span)
	}

	layerOffsets := make([]float64, len(layers))
	cursor := margin
	for i := range layers {
		layerOffsets[i] = cursor
		cursor += layerSizes[i] + layerGap
	}
	totalPrimary := cursor - layerGap + margin
	totalSecondary := maxSpan + margin*2
	result.Width, result.Height = totalSecondary, totalPrimary
	if horizontal {
		result.Width, result.Height = totalPrimary, totalSecondary
	}

	for layerIndex, layer := range layers {
		secondary := margin + (maxSpan-layerSpans[layerIndex])/2
		for order, id := range layer {
			w, h := widths[id], heights[id]
			if w == 0 {
				w, h = 1, 1
			}
			if horizontal {
				x := layerOffsets[layerIndex] + (layerSizes[layerIndex]-w)/2
				result.Nodes[id] = Box{X: x, Y: secondary, Width: w, Height: h, Layer: layerIndex, Order: order, Dummy: result.Dummies[id].Dummy}
				secondary += h + nodeGap
			} else {
				y := layerOffsets[layerIndex] + (layerSizes[layerIndex]-h)/2
				result.Nodes[id] = Box{X: secondary, Y: y, Width: w, Height: h, Layer: layerIndex, Order: order, Dummy: result.Dummies[id].Dummy}
				secondary += w + nodeGap
			}
		}
	}

	switch direction {
	case ir.DirBT:
		for id, box := range result.Nodes {
			box.Y = result.Height - box.Y - box.Height
			result.Nodes[id] = box
		}
	case ir.DirRL:
		for id, box := range result.Nodes {
			box.X = result.Width - box.X - box.Width
			result.Nodes[id] = box
		}
	}
}

func max(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func makeRoute(edge workEdge, chain []string, nodes map[string]Box, direction ir.Direction) Route {
	route := Route{From: edge.from, To: edge.to, OriginalEdge: edge.index, Reversed: edge.reversed}
	if edge.from == edge.to {
		box := nodes[edge.from]
		if direction == ir.DirLR || direction == ir.DirRL {
			x := box.X + box.Width
			y := box.Y + box.Height/2
			route.Points = []measure.Point{{X: x, Y: y}, {X: x + 28, Y: y - 24}, {X: x + 28, Y: y + 24}, {X: x, Y: y}}
		} else {
			x := box.X + box.Width/2
			y := box.Y + box.Height
			route.Points = []measure.Point{{X: x, Y: y}, {X: x + 28, Y: y + 18}, {X: x + 28, Y: y + 42}, {X: x, Y: y}}
		}
		route.SelfLoop = true
		return route
	}
	if len(chain) < 2 {
		return route
	}
	points := make([]measure.Point, 0, len(chain))
	for i, id := range chain {
		box := nodes[id]
		center := measure.Point{X: box.X + box.Width/2, Y: box.Y + box.Height/2}
		if i == 0 {
			if direction == ir.DirLR || direction == ir.DirRL {
				center.X = box.X + box.Width
			} else {
				center.Y = box.Y + box.Height
			}
		} else if i == len(chain)-1 {
			if direction == ir.DirLR || direction == ir.DirRL {
				center.X = box.X
			} else {
				center.Y = box.Y
			}
		}
		points = append(points, center)
	}
	if edge.reversed {
		for i, j := 0, len(points)-1; i < j; i, j = i+1, j-1 {
			points[i], points[j] = points[j], points[i]
		}
	}
	route.Points = points
	return route
}
