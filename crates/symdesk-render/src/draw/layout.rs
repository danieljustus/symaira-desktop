//! Port of Go `internal/draw/layout`: the deterministic layered
//! (Sugiyama-style) graph layout and the closed-form sequence layout.
//!
//! Faithfulness notes:
//!
//! * Go orders each layer with `sort.SliceStable` and a comparator that is not
//!   a strict weak ordering (nodes without neighbours compare by their current
//!   position while the others compare by median), so the result depends on
//!   Go's exact algorithm (insertion-sorted blocks of 20 merged with
//!   `symMerge`). `go_slice_stable` is a line-for-line port of
//!   `sort.stable_func`, not a call to Rust's `sort_by`.
//! * Every real number is a sum of multiples of 1/64, halvings and
//!   `integer * 32`/`integer * 180`/`integer * 48` products, all exact in
//!   `f64`, so Go's optional FMA fusion cannot change a result. Circle nodes
//!   use `math.Hypot` inside `MeasureNode`; that caveat is documented in
//!   `measure`.
//! * The result renders as the exact `json.Marshal` bytes of the Go structs
//!   (no JSON tags, map keys in byte order, `null` for nil slices, the
//!   `json: unsupported value` error for NaN/Inf).
//! * Go's recursive cycle-breaking DFS is iterative here, so a deep graph
//!   never consumes native stack per level.

use std::collections::{BTreeMap, HashMap};

use super::gotext::quote;
use super::ir::{Diagram, Value, write_f64, write_string};
use super::measure::{Measurer, NodeMeasureOptions, go_json_number};

const NODE_GAP: f64 = 32.0;
const LAYER_GAP: f64 = 72.0;
const MARGIN: f64 = 40.0;

/// Go `layout.Box`.
#[derive(Debug, Clone, Copy, Default, PartialEq)]
pub struct LayoutBox {
    pub x: f64,
    pub y: f64,
    pub width: f64,
    pub height: f64,
    pub layer: usize,
    pub order: usize,
    pub dummy: bool,
}

/// Go `layout.Route`.
#[derive(Debug, Clone, PartialEq)]
pub struct Route {
    pub from: String,
    pub to: String,
    pub points: Vec<(f64, f64)>,
    pub original_edge: usize,
    pub reversed: bool,
    pub self_loop: bool,
}

/// Go `layout.Result`.
#[derive(Debug, Clone, PartialEq)]
pub struct LayeredLayout {
    pub nodes: BTreeMap<String, LayoutBox>,
    pub dummies: BTreeMap<String, LayoutBox>,
    pub routes: Vec<Route>,
    pub layers: Vec<Vec<String>>,
    pub crossings: usize,
    pub width: f64,
    pub height: f64,
}

#[derive(Debug, Clone)]
struct WorkEdge {
    index: usize,
    from: String,
    to: String,
    reversed: bool,
}

#[derive(Debug, Clone)]
struct Segment {
    from: String,
    to: String,
}

fn edge_direction(e: &WorkEdge) -> (&str, &str) {
    if e.reversed {
        (&e.to, &e.from)
    } else {
        (&e.from, &e.to)
    }
}

fn is_horizontal(direction: &str) -> bool {
    direction == "LR" || direction == "RL"
}

/// Go `layout.Layout`.
pub fn layout_graph(measurer: &Measurer, d: &Diagram) -> Result<LayeredLayout, String> {
    if d.kind != "graph" {
        return Err(format!(
            "layered layout requires graph kind, got {}",
            quote(&d.kind)
        ));
    }

    let mut node_ids: Vec<String> = Vec::with_capacity(d.nodes.len());
    let mut nodes: HashMap<&str, &super::ir::Node> = HashMap::with_capacity(d.nodes.len());
    for n in &d.nodes {
        if n.id.is_empty() {
            return Err("graph node id cannot be empty".to_owned());
        }
        if nodes.contains_key(n.id.as_str()) {
            return Err(format!("duplicate graph node {}", quote(&n.id)));
        }
        nodes.insert(n.id.as_str(), n);
        node_ids.push(n.id.clone());
    }
    node_ids.sort();

    let mut edges: Vec<WorkEdge> = Vec::with_capacity(d.edges.len());
    for (i, e) in d.edges.iter().enumerate() {
        if !nodes.contains_key(e.from.as_str()) {
            return Err(format!(
                "edge {i} references unknown source {}",
                quote(&e.from)
            ));
        }
        if !nodes.contains_key(e.to.as_str()) {
            return Err(format!(
                "edge {i} references unknown target {}",
                quote(&e.to)
            ));
        }
        edges.push(WorkEdge {
            index: i,
            from: e.from.clone(),
            to: e.to.clone(),
            reversed: false,
        });
    }

    break_cycles(&node_ids, &mut edges);
    let order = topological_order(&node_ids, &mut edges);
    let layers_by_node = assign_layers(&order, &edges);
    let mut layer_nodes = make_layers(&node_ids, &layers_by_node);

    // Split every edge that spans a layer into a chain through dummy nodes.
    let mut segments: Vec<Segment> = Vec::with_capacity(edges.len());
    let mut chains: Vec<Vec<String>> = vec![Vec::new(); edges.len()];
    let mut dummies: BTreeMap<String, LayoutBox> = BTreeMap::new();
    for e in &edges {
        if e.from == e.to {
            chains[e.index] = vec![e.from.clone()];
            continue;
        }
        let (layout_from, layout_to) = edge_direction(e);
        let start = layers_by_node.get(layout_from).copied().unwrap_or(0);
        let end = layers_by_node.get(layout_to).copied().unwrap_or(0);
        let mut chain = vec![layout_from.to_owned()];
        let mut layer = start + 1;
        while layer < end {
            let id = format!("__dummy_{}_{}", e.index, layer);
            chain.push(id.clone());
            layer_nodes[layer].push(id.clone());
            dummies.insert(
                id,
                LayoutBox {
                    layer,
                    dummy: true,
                    width: 1.0,
                    height: 1.0,
                    ..LayoutBox::default()
                },
            );
            layer += 1;
        }
        chain.push(layout_to.to_owned());
        for pair in chain.windows(2) {
            segments.push(Segment {
                from: pair[0].clone(),
                to: pair[1].clone(),
            });
        }
        chains[e.index] = chain;
    }

    for layer in &mut layer_nodes {
        layer.sort();
    }
    reduce_crossings(&mut layer_nodes, &segments);

    let mut result = LayeredLayout {
        nodes: BTreeMap::new(),
        dummies,
        routes: Vec::with_capacity(edges.len()),
        layers: layer_nodes.iter().map(|l| l.to_vec()).collect(),
        crossings: 0,
        width: 0.0,
        height: 0.0,
    };
    assign_coordinates(&mut result, &layer_nodes, &nodes, measurer, &d.direction);
    let assigned: Vec<(String, LayoutBox)> = result
        .dummies
        .keys()
        .filter_map(|id| result.nodes.get(id).map(|b| (id.clone(), *b)))
        .collect();
    for (id, b) in assigned {
        result.dummies.insert(id, b);
    }
    for e in &edges {
        result
            .routes
            .push(make_route(e, &chains[e.index], &result.nodes, &d.direction));
    }
    result.crossings = count_crossings(&layer_nodes, &segments);
    Ok(result)
}

/// Go `breakCycles`: depth-first search marking back edges as reversed.
fn break_cycles(node_ids: &[String], edges: &mut [WorkEdge]) {
    let mut out: HashMap<String, Vec<usize>> = HashMap::with_capacity(node_ids.len());
    for (i, e) in edges.iter().enumerate() {
        out.entry(e.from.clone()).or_default().push(i);
    }
    for list in out.values_mut() {
        list.sort_by(|&x, &y| {
            let (a, b) = (&edges[x], &edges[y]);
            if a.to != b.to {
                a.to.cmp(&b.to)
            } else {
                a.index.cmp(&b.index)
            }
        });
    }
    let mut state: HashMap<String, u8> = HashMap::with_capacity(node_ids.len());
    for start in node_ids {
        if state.get(start).copied().unwrap_or(0) != 0 {
            continue;
        }
        state.insert(start.clone(), 1);
        let mut stack: Vec<(String, usize)> = vec![(start.clone(), 0)];
        while let Some((id, next)) = stack.last_mut() {
            let list = out.get(id.as_str());
            if let Some(&edge_index) = list.and_then(|l| l.get(*next)) {
                *next += 1;
                let to = edges[edge_index].to.clone();
                match state.get(&to).copied().unwrap_or(0) {
                    0 => {
                        state.insert(to.clone(), 1);
                        stack.push((to, 0));
                    }
                    1 => edges[edge_index].reversed = true,
                    _ => {}
                }
            } else {
                let finished = id.clone();
                state.insert(finished, 2);
                stack.pop();
            }
        }
    }
}

/// Go `topologicalOrder`, including its defensive cycle fallback.
fn topological_order(node_ids: &[String], edges: &mut [WorkEdge]) -> Vec<String> {
    for _attempt in 0..=edges.len() {
        let mut indegree: HashMap<String, i64> = HashMap::with_capacity(node_ids.len());
        let mut out: HashMap<String, Vec<String>> = HashMap::with_capacity(node_ids.len());
        for id in node_ids {
            indegree.insert(id.clone(), 0);
        }
        for e in edges.iter() {
            let (from, to) = edge_direction(e);
            if from == to {
                continue;
            }
            *indegree.entry(to.to_owned()).or_insert(0) += 1;
            out.entry(from.to_owned()).or_default().push(to.to_owned());
        }
        for list in out.values_mut() {
            list.sort();
        }
        let mut ready: Vec<String> = node_ids
            .iter()
            .filter(|id| indegree.get(id.as_str()).copied().unwrap_or(0) == 0)
            .cloned()
            .collect();
        let mut order: Vec<String> = Vec::with_capacity(node_ids.len());
        while !ready.is_empty() {
            let id = ready.remove(0);
            order.push(id.clone());
            if let Some(next_list) = out.get(&id) {
                for next in next_list {
                    let degree = indegree.entry(next.clone()).or_insert(0);
                    *degree -= 1;
                    if *degree == 0 {
                        ready.push(next.clone());
                        ready.sort();
                    }
                }
            }
        }
        if order.len() == node_ids.len() {
            return order;
        }
        // Defensive fallback for an unexpected cycle after DFS cycle breaking.
        for edge in edges.iter_mut() {
            let (from, to) = edge_direction(edge);
            let from_degree = indegree.get(from).copied().unwrap_or(0);
            let to_degree = indegree.get(to).copied().unwrap_or(0);
            if from_degree > 0 && to_degree > 0 {
                edge.reversed = !edge.reversed;
                break;
            }
        }
    }
    node_ids.to_vec()
}

/// Go `assignLayers`: longest-path layering over the topological order.
fn assign_layers(order: &[String], edges: &[WorkEdge]) -> HashMap<String, usize> {
    let mut layers: HashMap<String, usize> = HashMap::with_capacity(order.len());
    for id in order {
        layers.entry(id.clone()).or_insert(0);
        for e in edges {
            let (from, to) = edge_direction(e);
            if from == id.as_str() && from != to {
                let candidate = layers.get(from).copied().unwrap_or(0) + 1;
                if layers.get(to).copied().unwrap_or(0) < candidate {
                    layers.insert(to.to_owned(), candidate);
                }
            }
        }
    }
    layers
}

/// Go `makeLayers`.
fn make_layers(node_ids: &[String], layers: &HashMap<String, usize>) -> Vec<Vec<String>> {
    let layer_of = |id: &String| layers.get(id).copied().unwrap_or(0);
    let max_layer = node_ids.iter().map(layer_of).max().unwrap_or(0);
    let mut result: Vec<Vec<String>> = vec![Vec::new(); max_layer + 1];
    for id in node_ids {
        result[layer_of(id)].push(id.clone());
    }
    result
}

fn reduce_crossings(layers: &mut [Vec<String>], segments: &[Segment]) {
    for _iteration in 0..8 {
        for layer in 1..layers.len() {
            median_order(layers, segments, layer, false);
        }
        for layer in (0..layers.len().saturating_sub(1)).rev() {
            median_order(layers, segments, layer, true);
        }
        transpose(layers, segments);
    }
}

fn median_order(layers: &mut [Vec<String>], segments: &[Segment], layer: usize, look_down: bool) {
    if layers[layer].len() < 2 {
        return;
    }
    let mut positions: HashMap<String, usize> = HashMap::with_capacity(layers[layer].len());
    for (i, id) in layers[layer].iter().enumerate() {
        positions.insert(id.clone(), i);
    }
    let neighbor_layer = if look_down {
        &layers[layer + 1]
    } else {
        &layers[layer - 1]
    };
    let mut neighbor_positions: HashMap<String, usize> = HashMap::new();
    for (i, id) in neighbor_layer.iter().enumerate() {
        neighbor_positions.insert(id.clone(), i);
    }
    // `bary` depends only on the node id, never on the order being sorted.
    // One pass over the segments gathers each layer node's neighbour slots.
    let mut neighbours: HashMap<&str, Vec<usize>> = HashMap::with_capacity(positions.len());
    for s in segments {
        let (own, other) = if look_down {
            (&s.from, &s.to)
        } else {
            (&s.to, &s.from)
        };
        if positions.contains_key(own.as_str())
            && let Some(&p) = neighbor_positions.get(other.as_str())
        {
            neighbours.entry(own.as_str()).or_default().push(p);
        }
    }
    let mut bary: HashMap<String, Option<f64>> = HashMap::with_capacity(positions.len());
    for id in &layers[layer] {
        if bary.contains_key(id) {
            continue;
        }
        let median = match neighbours.get_mut(id.as_str()) {
            Some(values) if !values.is_empty() => {
                values.sort_unstable();
                Some(values[values.len() / 2] as f64)
            }
            _ => None,
        };
        bary.insert(id.clone(), median);
    }
    go_slice_stable(&mut layers[layer], &|x: &String, y: &String| {
        let a = bary.get(x).copied().flatten();
        let b = bary.get(y).copied().flatten();
        let (Some(a), Some(b)) = (a, b) else {
            return positions.get(x).copied().unwrap_or(0) < positions.get(y).copied().unwrap_or(0);
        };
        if a != b {
            return a < b;
        }
        x < y
    });
}

/// Node ids interned to dense indices: the transposition pass counts
/// crossings twice per candidate swap, so the counter must not hash strings.
struct Interned {
    layers: Vec<Vec<usize>>,
    segments: Vec<(usize, usize)>,
    nodes: usize,
}

fn intern<'a>(ids: &mut HashMap<&'a str, usize>, id: &'a str) -> usize {
    let next = ids.len();
    *ids.entry(id).or_insert(next)
}

impl Interned {
    fn new(layers: &[Vec<String>], segments: &[Segment]) -> Interned {
        let mut ids: HashMap<&str, usize> = HashMap::new();
        let layers: Vec<Vec<usize>> = layers
            .iter()
            .map(|layer| layer.iter().map(|id| intern(&mut ids, id)).collect())
            .collect();
        let segments = segments
            .iter()
            .map(|s| (intern(&mut ids, &s.from), intern(&mut ids, &s.to)))
            .collect();
        Interned {
            layers,
            segments,
            nodes: ids.len(),
        }
    }
}

#[allow(clippy::needless_range_loop)] // `layer` indexes both `layers` and `interned.layers`
fn transpose(layers: &mut [Vec<String>], segments: &[Segment]) {
    let mut interned = Interned::new(layers, segments);
    let mut full = Crossings::new(interned.nodes, interned.layers.len());
    // Swapping two adjacent nodes only changes crossings between segments
    // incident to those two nodes, so `after < before` is decided locally.
    // With a node id in more than one slot (never produced by the layout)
    // fall back to the full recount.
    let mut local = LocalCrossings::new(&interned);
    for (layer, row) in layers.iter_mut().enumerate() {
        let mut changed = true;
        while changed {
            changed = false;
            let mut i = 0;
            while i + 1 < row.len() {
                let improves = if let Some(local) = local.as_mut() {
                    let (u, v) = (interned.layers[layer][i], interned.layers[layer][i + 1]);
                    let improves = local.swap_improves(u, v, i);
                    if improves {
                        interned.layers[layer].swap(i, i + 1);
                        local.index_of[u] = i + 1;
                        local.index_of[v] = i;
                    }
                    improves
                } else {
                    let before = full.count(&interned);
                    interned.layers[layer].swap(i, i + 1);
                    let after = full.count(&interned);
                    if after >= before {
                        interned.layers[layer].swap(i, i + 1);
                    }
                    after < before
                };
                if improves {
                    row.swap(i, i + 1);
                    changed = true;
                }
                i += 1;
            }
        }
    }
}

/// Strict inversion of two inter-layer segments given as (upper, lower) slots.
fn crosses(a: (usize, usize), b: (usize, usize)) -> bool {
    (a.0 < b.0 && a.1 > b.1) || (a.0 > b.0 && a.1 < b.1)
}

/// Slot positions kept current across swaps, plus the neighbours of every
/// node across adjacent-layer segments. Only valid when every node id sits
/// in exactly one slot.
struct LocalCrossings {
    index_of: Vec<usize>,
    /// For a node, the `from` nodes of segments ending at it (previous layer).
    up: Vec<Vec<usize>>,
    /// For a node, the `to` nodes of segments leaving it (next layer).
    down: Vec<Vec<usize>>,
}

impl LocalCrossings {
    fn new(g: &Interned) -> Option<LocalCrossings> {
        let mut layer_of = vec![UNPLACED; g.nodes];
        let mut index_of = vec![0; g.nodes];
        for (l, layer) in g.layers.iter().enumerate() {
            for (j, &id) in layer.iter().enumerate() {
                if layer_of[id] != UNPLACED {
                    return None;
                }
                layer_of[id] = l;
                index_of[id] = j;
            }
        }
        let mut up = vec![Vec::new(); g.nodes];
        let mut down = vec![Vec::new(); g.nodes];
        for &(from, to) in &g.segments {
            let (fl, tl) = (layer_of[from], layer_of[to]);
            if fl != UNPLACED && tl != UNPLACED && tl == fl + 1 {
                down[from].push(to);
                up[to].push(from);
            }
        }
        Some(LocalCrossings { index_of, up, down })
    }

    /// Whether exchanging `u` (slot `i`) and `v` (slot `i + 1`) lowers the
    /// number of crossings.
    fn swap_improves(&self, u: usize, v: usize, i: usize) -> bool {
        let ix = &self.index_of;
        let (mut before, mut after) = (0usize, 0usize);
        for &p in &self.up[u] {
            for &q in &self.up[v] {
                before += usize::from(crosses((ix[p], i), (ix[q], i + 1)));
                after += usize::from(crosses((ix[p], i + 1), (ix[q], i)));
            }
        }
        for &p in &self.down[u] {
            for &q in &self.down[v] {
                before += usize::from(crosses((i, ix[p]), (i + 1, ix[q])));
                after += usize::from(crosses((i + 1, ix[p]), (i, ix[q])));
            }
        }
        after < before
    }
}

/// Go `countCrossings`: a node id resolves to the first layer holding it and
/// its last index inside that layer.
fn count_crossings(layers: &[Vec<String>], segments: &[Segment]) -> usize {
    let interned = Interned::new(layers, segments);
    Crossings::new(interned.nodes, interned.layers.len()).count(&interned)
}

/// Reusable buffers of the crossing counter.
struct Crossings {
    layer_of: Vec<usize>,
    index_of: Vec<usize>,
    pairs: Vec<Vec<(usize, usize)>>,
}

const UNPLACED: usize = usize::MAX;

impl Crossings {
    fn new(nodes: usize, layers: usize) -> Crossings {
        Crossings {
            layer_of: vec![UNPLACED; nodes],
            index_of: vec![0; nodes],
            pairs: vec![Vec::new(); layers],
        }
    }

    fn count(&mut self, g: &Interned) -> usize {
        self.layer_of.fill(UNPLACED);
        for (l, layer) in g.layers.iter().enumerate() {
            for (j, &id) in layer.iter().enumerate() {
                if self.layer_of[id] == UNPLACED || self.layer_of[id] == l {
                    self.layer_of[id] = l;
                    self.index_of[id] = j;
                }
            }
        }
        for p in &mut self.pairs {
            p.clear();
        }
        for &(from, to) in &g.segments {
            let (fl, tl) = (self.layer_of[from], self.layer_of[to]);
            if fl == UNPLACED || tl == UNPLACED || tl != fl + 1 {
                continue;
            }
            self.pairs[fl].push((self.index_of[from], self.index_of[to]));
        }
        let mut count = 0;
        for layer_pairs in &self.pairs {
            for i in 0..layer_pairs.len() {
                for j in i + 1..layer_pairs.len() {
                    let (a, b) = (layer_pairs[i], layer_pairs[j]);
                    if (a.0 < b.0 && a.1 > b.1) || (a.0 > b.0 && a.1 < b.1) {
                        count += 1;
                    }
                }
            }
        }
        count
    }
}

/// Go's builtin-style `max(a, b float64)` helper (`a > b ? a : b`).
fn go_max(a: f64, b: f64) -> f64 {
    if a > b { a } else { b }
}

fn assign_coordinates(
    result: &mut LayeredLayout,
    layers: &[Vec<String>],
    nodes: &HashMap<&str, &super::ir::Node>,
    measurer: &Measurer,
    direction: &str,
) {
    let options = NodeMeasureOptions::default();
    let mut widths: HashMap<&str, f64> = HashMap::with_capacity(nodes.len());
    let mut heights: HashMap<&str, f64> = HashMap::with_capacity(nodes.len());
    for (&id, node) in nodes {
        let size = measurer.measure_node(&node.label, &node.shape, options);
        let (mut w, mut h) = (size.width, size.height);
        if node.width > 0.0 {
            w = node.width;
        }
        if node.height > 0.0 {
            h = node.height;
        }
        widths.insert(id, w);
        heights.insert(id, h);
    }
    let size_of = |id: &str| {
        let (mut w, mut h) = (
            widths.get(id).copied().unwrap_or(0.0),
            heights.get(id).copied().unwrap_or(0.0),
        );
        if w == 0.0 {
            w = 1.0;
            h = 1.0;
        }
        (w, h)
    };

    let horizontal = is_horizontal(direction);
    let mut layer_spans = vec![0.0f64; layers.len()];
    let mut layer_sizes = vec![0.0f64; layers.len()];
    let mut max_span = 0.0f64;
    for (i, layer) in layers.iter().enumerate() {
        let (mut span, mut size) = (0.0f64, 0.0f64);
        for id in layer {
            let (w, h) = size_of(id);
            if horizontal {
                span += h;
                size = go_max(size, w);
            } else {
                span += w;
                size = go_max(size, h);
            }
        }
        if layer.len() > 1 {
            span += (layer.len() - 1) as f64 * NODE_GAP;
        }
        layer_spans[i] = span;
        layer_sizes[i] = size;
        max_span = go_max(max_span, span);
    }

    let mut layer_offsets = vec![0.0f64; layers.len()];
    let mut cursor = MARGIN;
    for i in 0..layers.len() {
        layer_offsets[i] = cursor;
        cursor += layer_sizes[i] + LAYER_GAP;
    }
    let total_primary = cursor - LAYER_GAP + MARGIN;
    let total_secondary = max_span + MARGIN * 2.0;
    if horizontal {
        result.width = total_primary;
        result.height = total_secondary;
    } else {
        result.width = total_secondary;
        result.height = total_primary;
    }

    for (layer_index, layer) in layers.iter().enumerate() {
        let mut secondary = MARGIN + (max_span - layer_spans[layer_index]) / 2.0;
        for (order, id) in layer.iter().enumerate() {
            let (w, h) = size_of(id);
            let dummy = result.dummies.get(id).is_some_and(|b| b.dummy);
            let placed = if horizontal {
                let x = layer_offsets[layer_index] + (layer_sizes[layer_index] - w) / 2.0;
                let b = LayoutBox {
                    x,
                    y: secondary,
                    width: w,
                    height: h,
                    layer: layer_index,
                    order,
                    dummy,
                };
                secondary += h + NODE_GAP;
                b
            } else {
                let y = layer_offsets[layer_index] + (layer_sizes[layer_index] - h) / 2.0;
                let b = LayoutBox {
                    x: secondary,
                    y,
                    width: w,
                    height: h,
                    layer: layer_index,
                    order,
                    dummy,
                };
                secondary += w + NODE_GAP;
                b
            };
            result.nodes.insert(id.clone(), placed);
        }
    }

    match direction {
        "BT" => {
            let height = result.height;
            for b in result.nodes.values_mut() {
                b.y = height - b.y - b.height;
            }
        }
        "RL" => {
            let width = result.width;
            for b in result.nodes.values_mut() {
                b.x = width - b.x - b.width;
            }
        }
        _ => {}
    }
}

fn make_route(
    edge: &WorkEdge,
    chain: &[String],
    nodes: &BTreeMap<String, LayoutBox>,
    direction: &str,
) -> Route {
    let mut route = Route {
        from: edge.from.clone(),
        to: edge.to.clone(),
        points: Vec::new(),
        original_edge: edge.index,
        reversed: edge.reversed,
        self_loop: false,
    };
    let horizontal = is_horizontal(direction);
    if edge.from == edge.to {
        let b = nodes.get(&edge.from).copied().unwrap_or_default();
        route.points = if horizontal {
            let x = b.x + b.width;
            let y = b.y + b.height / 2.0;
            vec![(x, y), (x + 28.0, y - 24.0), (x + 28.0, y + 24.0), (x, y)]
        } else {
            let x = b.x + b.width / 2.0;
            let y = b.y + b.height;
            vec![(x, y), (x + 28.0, y + 18.0), (x + 28.0, y + 42.0), (x, y)]
        };
        route.self_loop = true;
        return route;
    }
    if chain.len() < 2 {
        return route;
    }
    let mut points: Vec<(f64, f64)> = Vec::with_capacity(chain.len());
    for (i, id) in chain.iter().enumerate() {
        let b = nodes.get(id).copied().unwrap_or_default();
        let mut center = (b.x + b.width / 2.0, b.y + b.height / 2.0);
        if i == 0 {
            if horizontal {
                center.0 = b.x + b.width;
            } else {
                center.1 = b.y + b.height;
            }
        } else if i == chain.len() - 1 {
            if horizontal {
                center.0 = b.x;
            } else {
                center.1 = b.y;
            }
        }
        points.push(center);
    }
    if edge.reversed {
        points.reverse();
    }
    route.points = points;
    route
}

/// Go `json.Marshal` of a `float64`, with the encoder's rejection of NaN/Inf.
fn put_number(out: &mut String, value: f64) -> Result<(), String> {
    if value.is_nan() {
        return Err("json: unsupported value: NaN".to_owned());
    }
    if value.is_infinite() {
        let sign = if value > 0.0 { "+Inf" } else { "-Inf" };
        return Err(format!("json: unsupported value: {sign}"));
    }
    write_f64(out, value);
    Ok(())
}

fn put_box(out: &mut String, b: &LayoutBox) -> Result<(), String> {
    out.push_str("{\"X\":");
    put_number(out, b.x)?;
    out.push_str(",\"Y\":");
    put_number(out, b.y)?;
    out.push_str(",\"Width\":");
    put_number(out, b.width)?;
    out.push_str(",\"Height\":");
    put_number(out, b.height)?;
    out.push_str(&format!(
        ",\"Layer\":{},\"Order\":{},\"Dummy\":{}}}",
        b.layer, b.order, b.dummy
    ));
    Ok(())
}

fn put_box_map(out: &mut String, map: &BTreeMap<String, LayoutBox>) -> Result<(), String> {
    out.push('{');
    for (i, (id, b)) in map.iter().enumerate() {
        if i > 0 {
            out.push(',');
        }
        write_string(out, id);
        out.push(':');
        put_box(out, b)?;
    }
    out.push('}');
    Ok(())
}

impl LayeredLayout {
    /// The exact `json.Marshal(*layout.Result)` bytes, or Go's marshal error.
    pub fn to_json(&self) -> Result<String, String> {
        let mut out = String::from("{\"Nodes\":");
        put_box_map(&mut out, &self.nodes)?;
        out.push_str(",\"Dummies\":");
        put_box_map(&mut out, &self.dummies)?;
        out.push_str(",\"Routes\":[");
        for (i, r) in self.routes.iter().enumerate() {
            if i > 0 {
                out.push(',');
            }
            out.push_str("{\"From\":");
            write_string(&mut out, &r.from);
            out.push_str(",\"To\":");
            write_string(&mut out, &r.to);
            out.push_str(",\"Points\":");
            if r.points.is_empty() {
                out.push_str("null");
            } else {
                out.push('[');
                for (j, (x, y)) in r.points.iter().enumerate() {
                    if j > 0 {
                        out.push(',');
                    }
                    out.push_str("{\"x\":");
                    put_number(&mut out, *x)?;
                    out.push_str(",\"y\":");
                    put_number(&mut out, *y)?;
                    out.push('}');
                }
                out.push(']');
            }
            out.push_str(&format!(
                ",\"OriginalEdge\":{},\"Reversed\":{},\"SelfLoop\":{}}}",
                r.original_edge, r.reversed, r.self_loop
            ));
        }
        out.push_str("],\"Layers\":[");
        for (i, layer) in self.layers.iter().enumerate() {
            if i > 0 {
                out.push(',');
            }
            if layer.is_empty() {
                out.push_str("null");
                continue;
            }
            out.push('[');
            for (j, id) in layer.iter().enumerate() {
                if j > 0 {
                    out.push(',');
                }
                write_string(&mut out, id);
            }
            out.push(']');
        }
        out.push_str(&format!("],\"Crossings\":{},\"Width\":", self.crossings));
        put_number(&mut out, self.width)?;
        out.push_str(",\"Height\":");
        put_number(&mut out, self.height)?;
        out.push('}');
        Ok(out)
    }
}

// ---------------------------------------------------------------------------
// Sequence layout

const SEQ_COL_GAP: f64 = 180.0;
const SEQ_ROW_GAP: f64 = 48.0;
const SEQ_MARGIN_X: f64 = 120.0;
const SEQ_MARGIN_Y: f64 = 80.0;
const SEQ_HEADER_H: f64 = 60.0;

/// Go `layout.SequenceLayout`.
#[derive(Debug, Clone, PartialEq)]
pub struct SequenceLayout {
    pub actor_x: BTreeMap<String, f64>,
    pub message_y: Vec<f64>,
    pub width: f64,
    pub height: f64,
}

/// Go `layout.SequenceLayoutFromIR`.
pub fn layout_sequence(d: &Diagram) -> Result<SequenceLayout, String> {
    if d.kind != "sequence" {
        return Err("sequence layout requires sequence kind, got *ir.Diagram".to_owned());
    }
    let raw_actors = d
        .custom
        .get("actors")
        .ok_or_else(|| "sequence diagram missing actors".to_owned())?;
    let raw_messages = d
        .custom
        .get("messages")
        .ok_or_else(|| "sequence diagram missing messages".to_owned())?;

    let actor_ids = decode_records(
        raw_actors,
        "SequenceActor",
        &[("ID", FieldType::Str), ("Label", FieldType::Str)],
        0,
    )
    .map_err(|e| format!("decode actors: {e}"))?;
    let messages = decode_records(
        raw_messages,
        "SequenceMessage",
        &[
            ("From", FieldType::Str),
            ("To", FieldType::Str),
            ("Text", FieldType::Str),
            ("Order", FieldType::Int),
        ],
        usize::MAX,
    )
    .map_err(|e| format!("decode messages: {e}"))?;

    let mut actor_x = BTreeMap::new();
    for (i, id) in actor_ids.iter().enumerate() {
        actor_x.insert(id.clone(), SEQ_MARGIN_X + i as f64 * SEQ_COL_GAP);
    }
    let message_y: Vec<f64> = (0..messages.len())
        .map(|i| SEQ_MARGIN_Y + SEQ_HEADER_H + i as f64 * SEQ_ROW_GAP)
        .collect();
    let width = SEQ_MARGIN_X * 2.0 + actor_ids.len().saturating_sub(1).max(1) as f64 * SEQ_COL_GAP;
    let height = SEQ_MARGIN_Y + SEQ_HEADER_H + messages.len() as f64 * SEQ_ROW_GAP + SEQ_MARGIN_Y;
    Ok(SequenceLayout {
        actor_x,
        message_y,
        width,
        height,
    })
}

#[derive(Clone, Copy, PartialEq)]
enum FieldType {
    Str,
    Int,
}

fn json_kind(v: &Value) -> &'static str {
    match v {
        Value::Null => "null",
        Value::Bool(_) => "bool",
        Value::Num(_) | Value::Int(_) => "number",
        Value::Str(_) => "string",
        Value::Arr(_) => "array",
        Value::Map(_) | Value::Struct(_) => "object",
    }
}

/// Go `encoding/json` decoding of `[]T` where `T` is an untagged struct of
/// string/int fields (`json.Marshal` of the `custom` value, then
/// `json.Unmarshal`). Returns the first saved error, like Go's decoder
/// (which keeps decoding after a type mismatch). The `id_field` index is the
/// field whose string value is collected for each record (empty when absent or
/// not a string).
fn decode_records(
    value: &Value,
    type_name: &str,
    fields: &[(&str, FieldType)],
    id_field: usize,
) -> Result<Vec<String>, String> {
    let items = match value {
        Value::Null => return Ok(Vec::new()),
        Value::Arr(items) => items,
        other => {
            return Err(format!(
                "json: cannot unmarshal {} into Go value of type []layout.{type_name}",
                json_kind(other)
            ));
        }
    };
    let mut first_error: Option<String> = None;
    let mut ids = Vec::with_capacity(items.len());
    for item in items {
        let mut id = String::new();
        let entries: Vec<(&str, &Value)> = match item {
            Value::Null => Vec::new(),
            Value::Map(map) => map.iter().map(|(k, v)| (k.as_str(), v)).collect(),
            Value::Struct(list) => list.iter().map(|(k, v)| (*k, v)).collect(),
            other => {
                first_error.get_or_insert_with(|| {
                    format!(
                        "json: cannot unmarshal {} into Go value of type layout.{type_name}",
                        json_kind(other)
                    )
                });
                ids.push(id);
                continue;
            }
        };
        for (key, v) in entries {
            // Exact match first, then ASCII case-insensitive (none of the
            // field names holds a letter with a non-ASCII case fold).
            let field = fields
                .iter()
                .position(|(name, _)| *name == key)
                .or_else(|| {
                    fields
                        .iter()
                        .position(|(name, _)| name.eq_ignore_ascii_case(key))
                });
            let Some(field) = field else { continue };
            let (name, kind) = fields[field];
            if matches!(v, Value::Null) {
                continue;
            }
            let mismatch = match (kind, v) {
                (FieldType::Str, Value::Str(s)) => {
                    if field == id_field {
                        id = s.clone();
                    }
                    None
                }
                (FieldType::Int, Value::Int(_)) => None,
                (FieldType::Int, Value::Num(n)) => {
                    let literal = go_json_number(*n);
                    if literal.parse::<i64>().is_ok() {
                        None
                    } else {
                        Some(format!("number {literal}"))
                    }
                }
                (_, other) => Some(json_kind(other).to_owned()),
            };
            if let Some(found) = mismatch {
                first_error.get_or_insert_with(|| {
                    format!(
                        "json: cannot unmarshal {found} into Go struct field {type_name}.{name} of type {}",
                        if kind == FieldType::Int { "int" } else { "string" }
                    )
                });
            }
        }
        ids.push(id);
    }
    match first_error {
        Some(e) => Err(e),
        None => Ok(ids),
    }
}

impl SequenceLayout {
    /// The exact `json.Marshal(*layout.SequenceLayout)` bytes.
    pub fn to_json(&self) -> Result<String, String> {
        let mut out = String::from("{\"ActorX\":{");
        for (i, (id, x)) in self.actor_x.iter().enumerate() {
            if i > 0 {
                out.push(',');
            }
            write_string(&mut out, id);
            out.push(':');
            put_number(&mut out, *x)?;
        }
        out.push_str("},\"MessageY\":[");
        for (i, y) in self.message_y.iter().enumerate() {
            if i > 0 {
                out.push(',');
            }
            put_number(&mut out, *y)?;
        }
        out.push_str("],\"Width\":");
        put_number(&mut out, self.width)?;
        out.push_str(",\"Height\":");
        put_number(&mut out, self.height)?;
        out.push('}');
        Ok(out)
    }
}

// ---------------------------------------------------------------------------
// Go's sort.SliceStable

/// Go `sort.SliceStable` (`stable_func`): insertion-sorted blocks of 20
/// merged with `symMerge`. The exact algorithm matters because the comparator
/// used by `median_order` is not a strict weak ordering.
pub(super) fn go_slice_stable<T>(data: &mut [T], less: &dyn Fn(&T, &T) -> bool) {
    let n = data.len();
    let mut block_size = 20usize;
    let (mut a, mut b) = (0usize, block_size);
    while b <= n {
        insertion_sort(data, a, b, less);
        a = b;
        b += block_size;
    }
    insertion_sort(data, a, n, less);

    while block_size < n {
        a = 0;
        b = 2 * block_size;
        while b <= n {
            sym_merge(data, a, a + block_size, b, less);
            a = b;
            b += 2 * block_size;
        }
        let m = a + block_size;
        if m < n {
            sym_merge(data, a, m, n, less);
        }
        block_size *= 2;
    }
}

fn insertion_sort<T>(data: &mut [T], a: usize, b: usize, less: &dyn Fn(&T, &T) -> bool) {
    let mut i = a + 1;
    while i < b {
        let mut j = i;
        while j > a && less(&data[j], &data[j - 1]) {
            data.swap(j, j - 1);
            j -= 1;
        }
        i += 1;
    }
}

fn sym_merge<T>(data: &mut [T], a: usize, m: usize, b: usize, less: &dyn Fn(&T, &T) -> bool) {
    // Direct insertion of data[a] into data[m:b].
    if m - a == 1 {
        let (mut i, mut j) = (m, b);
        while i < j {
            let h = (i + j) >> 1;
            if less(&data[h], &data[a]) {
                i = h + 1;
            } else {
                j = h;
            }
        }
        let mut k = a;
        while k + 1 < i {
            data.swap(k, k + 1);
            k += 1;
        }
        return;
    }
    // Direct insertion of data[m] into data[a:m].
    if b - m == 1 {
        let (mut i, mut j) = (a, m);
        while i < j {
            let h = (i + j) >> 1;
            if !less(&data[m], &data[h]) {
                i = h + 1;
            } else {
                j = h;
            }
        }
        let mut k = m;
        while k > i {
            data.swap(k, k - 1);
            k -= 1;
        }
        return;
    }

    let mid = (a + b) >> 1;
    let n = mid + m;
    let (mut start, mut r) = if m > mid { (n - b, mid) } else { (a, m) };
    let p = n - 1;
    while start < r {
        let c = (start + r) >> 1;
        if !less(&data[p - c], &data[c]) {
            start = c + 1;
        } else {
            r = c;
        }
    }
    let end = n - start;
    if start < m && m < end {
        rotate(data, start, m, end);
    }
    if a < start && start < mid {
        sym_merge(data, a, start, mid, less);
    }
    if mid < end && end < b {
        sym_merge(data, mid, end, b, less);
    }
}

fn rotate<T>(data: &mut [T], a: usize, m: usize, b: usize) {
    let mut i = m - a;
    let mut j = b - m;
    while i != j {
        if i > j {
            swap_range(data, m - i, m, j);
            i -= j;
        } else {
            swap_range(data, m - i, m + j - i, i);
            j -= i;
        }
    }
    swap_range(data, m - i, m, i);
}

fn swap_range<T>(data: &mut [T], a: usize, b: usize, n: usize) {
    for i in 0..n {
        data.swap(a + i, b + i);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn go_stable_sort_is_stable_and_sorts_consistent_orderings() {
        // 200 pairs with many equal keys: stable and sorted.
        let mut data: Vec<(u32, usize)> = (0..200).map(|i| (((i * 7919) % 13) as u32, i)).collect();
        go_slice_stable(&mut data, &|x, y| x.0 < y.0);
        for pair in data.windows(2) {
            assert!(pair[0].0 < pair[1].0 || (pair[0].0 == pair[1].0 && pair[0].1 < pair[1].1));
        }
        let mut empty: Vec<u8> = Vec::new();
        go_slice_stable(&mut empty, &|x, y| x < y);
        let mut one = vec![1u8];
        go_slice_stable(&mut one, &|x, y| x < y);
        assert_eq!(one, [1]);
    }

    /// Go's `transpose` verbatim: every candidate swap recounts all crossings
    /// with Go's `countCrossings` (string maps, first layer, last index).
    fn transpose_reference(layers: &mut [Vec<String>], segments: &[Segment]) {
        fn count(layers: &[Vec<String>], segments: &[Segment]) -> usize {
            let pos: Vec<HashMap<&str, usize>> = layers
                .iter()
                .map(|l| {
                    l.iter()
                        .enumerate()
                        .map(|(j, id)| (id.as_str(), j))
                        .collect()
                })
                .collect();
            let find = |id: &str| pos.iter().position(|p| p.contains_key(id));
            let mut n = 0;
            for layer in 0..layers.len().saturating_sub(1) {
                let mut pairs = Vec::new();
                for s in segments {
                    let (Some(f), Some(t)) = (find(&s.from), find(&s.to)) else {
                        continue;
                    };
                    if f == layer && t == layer + 1 {
                        pairs.push((pos[layer][s.from.as_str()], pos[layer + 1][s.to.as_str()]));
                    }
                }
                for i in 0..pairs.len() {
                    for j in i + 1..pairs.len() {
                        n += usize::from(crosses(pairs[i], pairs[j]));
                    }
                }
            }
            n
        }
        for layer in 0..layers.len() {
            let mut changed = true;
            while changed {
                changed = false;
                for i in 0..layers[layer].len().saturating_sub(1) {
                    let before = count(layers, segments);
                    layers[layer].swap(i, i + 1);
                    if count(layers, segments) < before {
                        changed = true;
                    } else {
                        layers[layer].swap(i, i + 1);
                    }
                }
            }
        }
    }

    #[test]
    fn transpose_matches_the_full_recount_on_random_layers() {
        let mut seed = 0x2545_f491_4f6c_dd1du64;
        let mut next = move |n: usize| {
            seed ^= seed << 13;
            seed ^= seed >> 7;
            seed ^= seed << 17;
            (seed % n as u64) as usize
        };
        for case in 0..400 {
            let depth = 2 + next(4);
            let mut layers: Vec<Vec<String>> = (0..depth)
                .map(|l| (0..1 + next(9)).map(|j| format!("n{l}_{j}")).collect())
                .collect();
            if case % 7 == 0 {
                // One id in two layers: exercises the full-recount fallback.
                let dup = layers[0][0].clone();
                layers[1].push(dup);
            }
            let mut segments = Vec::new();
            for l in 0..depth - 1 {
                for _ in 0..next(25) {
                    // Repeated pairs (multi-edges) and unplaced ids included.
                    let from = if next(20) == 0 {
                        "ghost".to_owned()
                    } else {
                        layers[l][next(layers[l].len())].clone()
                    };
                    segments.push(Segment {
                        from,
                        to: layers[l + 1][next(layers[l + 1].len())].clone(),
                    });
                }
            }
            let mut fast = layers.clone();
            let mut slow = layers.clone();
            transpose(&mut fast, &segments);
            transpose_reference(&mut slow, &segments);
            assert_eq!(fast, slow, "case {case}");
            assert_eq!(
                count_crossings(&fast, &segments),
                count_crossings(&slow, &segments)
            );
        }
    }
}
