import SwiftUI
import SymairaTheme
import SymDeskCore

struct GraphView: View {
    @EnvironmentObject var core: DeskCore
    @State private var graphData: GraphData?
    @State private var nodePositions: [String: CGPoint] = [:]
    @State private var filterText = ""
    @State private var canvasSize = CGSize.zero
    @State private var zoomScale: CGFloat = 1
    @State private var panOffset = CGSize.zero
    @State private var dragOrigin: CGSize?
    @State private var magnificationOrigin: CGFloat?

    var onSelectNode: ((String) -> Void)?

    private var filteredData: GraphData? {
        guard let data = graphData else { return nil }
        guard !filterText.isEmpty else { return data }

        let lower = filterText.lowercased()
        let filteredNodes = data.nodes.filter {
            $0.id.lowercased().contains(lower) || $0.label.lowercased().contains(lower)
        }
        let validIDs = Set(filteredNodes.map(\.id))
        let filteredEdges = data.edges.filter {
            validIDs.contains($0.source) && validIDs.contains($0.target)
        }
        return GraphData(nodes: filteredNodes, edges: filteredEdges)
    }

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 8) {
                TextField("Filter by name or path...", text: $filterText)
                    .textFieldStyle(.symaira)

                Button {
                    refitGraph()
                } label: {
                    Label("Fit to Window", systemImage: "arrow.up.left.and.arrow.down.right")
                }
                .buttonStyle(.bordered)
                .controlSize(.small)
                .disabled(filteredData == nil || canvasSize == .zero)
                .help("Center the graph and fit it to the available window")

                Button {
                    zoomScale = min(GraphLayout.maximumZoom, zoomScale + 0.1)
                } label: {
                    Label("Zoom In", systemImage: "plus.magnifyingglass")
                }
                .buttonStyle(.bordered)
                .controlSize(.small)
                .disabled(filteredData == nil)
                .accessibilityLabel("Zoom in")

                Button {
                    zoomScale = max(GraphLayout.minimumZoom, zoomScale - 0.1)
                } label: {
                    Label("Zoom Out", systemImage: "minus.magnifyingglass")
                }
                .buttonStyle(.bordered)
                .controlSize(.small)
                .disabled(filteredData == nil)
                .accessibilityLabel("Zoom out")

                Text("\(Int(zoomScale * 100))%")
                    .font(.system(size: 12, design: .monospaced))
                    .foregroundStyle(SymairaTheme.textSecondary)
                    .frame(minWidth: 42, alignment: .trailing)
                    .accessibilityLabel("Zoom level")
                    .accessibilityValue("\(Int(zoomScale * 100)) percent")
            }
            .padding(.horizontal)
            .padding(.vertical, 8)

            GeometryReader { geo in
                if let data = filteredData {
                    graphCanvas(data: data, size: geo.size)
                        .onAppear {
                            canvasSize = geo.size
                            refitGraph(data: data, size: geo.size)
                        }
                        .onChange(of: geo.size) { _, newSize in
                            canvasSize = newSize
                            // Recompute instead of clamping old coordinates. This
                            // avoids stale bounds after a window resize.
                            refitGraph(data: data, size: newSize)
                        }
                        .onChange(of: data) { _, newData in
                            refitGraph(data: newData, size: canvasSize)
                        }
                } else {
                    ProgressView()
                        .tint(SymairaTheme.goldPrimary)
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                }
            }
        }
        .task {
            do {
                graphData = try await core.getGraph()
            } catch {
                print("Graph error: \(error)")
            }
        }
    }

    @ViewBuilder
    private func graphCanvas(data: GraphData, size: CGSize) -> some View {
        Canvas { context, canvasSize in
            for edge in data.edges {
                guard let source = nodePositions[edge.source],
                      let target = nodePositions[edge.target] else { continue }
                var path = Path()
                path.move(to: screenPoint(source, in: canvasSize))
                path.addLine(to: screenPoint(target, in: canvasSize))
                context.stroke(
                    path,
                    with: .color(SymairaTheme.goldPrimary.opacity(0.25)),
                    lineWidth: 1
                )
            }

            for node in data.nodes {
                guard let position = nodePositions[node.id] else { continue }
                let point = screenPoint(position, in: canvasSize)
                let glowRect = CGRect(x: point.x - 8, y: point.y - 8, width: 16, height: 16)
                context.fill(
                    Path(ellipseIn: glowRect),
                    with: .color(SymairaTheme.goldPrimary.opacity(0.18))
                )
                let rect = CGRect(x: point.x - 5, y: point.y - 5, width: 10, height: 10)
                context.fill(Path(ellipseIn: rect), with: .color(SymairaTheme.goldPrimary))

                // GraphicsContext.draw needs a real Text, so the role font is
                // applied directly here (issue #352). Truncate before drawing
                // so even long labels stay within the canvas.
                let label = GraphLayout.displayLabel(node.label, in: canvasSize)
                let text = Text(label)
                    .font(SymairaTextRole.caption.font)
                    .foregroundColor(SymairaTheme.textSecondary)
                context.draw(text, at: CGPoint(x: point.x, y: point.y + 12))
            }
        }
        .background(SymairaTheme.bgDark)
        .contentShape(Rectangle())
        .gesture(
            DragGesture(minimumDistance: 0)
                .onChanged { value in
                    if dragOrigin == nil {
                        dragOrigin = panOffset
                    }
                    guard let origin = dragOrigin else { return }
                    panOffset = CGSize(
                        width: origin.width + value.translation.width,
                        height: origin.height + value.translation.height
                    )
                }
                .onEnded { value in
                    defer { dragOrigin = nil }
                    if hypot(value.translation.width, value.translation.height) < 4,
                       let clickedNode = findNode(at: value.location, data: data, size: size) {
                        onSelectNode?(clickedNode.id)
                    }
                }
        )
        .simultaneousGesture(
            MagnificationGesture()
                .onChanged { value in
                    if magnificationOrigin == nil {
                        magnificationOrigin = zoomScale
                    }
                    guard let origin = magnificationOrigin else { return }
                    zoomScale = min(
                        GraphLayout.maximumZoom,
                        max(GraphLayout.minimumZoom, origin * value)
                    )
                }
                .onEnded { _ in
                    magnificationOrigin = nil
                }
        )
    }

    private func refitGraph() {
        guard let data = filteredData else { return }
        refitGraph(data: data, size: canvasSize)
    }

    private func refitGraph(data: GraphData, size: CGSize) {
        guard size.width > 1, size.height > 1 else { return }
        nodePositions = GraphLayout.layout(data: data, in: size)
        zoomScale = 1
        panOffset = .zero
    }

    private func screenPoint(_ point: CGPoint, in size: CGSize) -> CGPoint {
        let center = CGPoint(x: size.width / 2, y: size.height / 2)
        return CGPoint(
            x: center.x + (point.x - center.x) * zoomScale + panOffset.width,
            y: center.y + (point.y - center.y) * zoomScale + panOffset.height
        )
    }

    private func graphPoint(_ point: CGPoint, in size: CGSize) -> CGPoint {
        let center = CGPoint(x: size.width / 2, y: size.height / 2)
        return CGPoint(
            x: (point.x - center.x - panOffset.width) / zoomScale + center.x,
            y: (point.y - center.y - panOffset.height) / zoomScale + center.y
        )
    }

    private func findNode(at point: CGPoint, data: GraphData, size: CGSize) -> GraphNode? {
        let graphPoint = graphPoint(point, in: size)
        let hitRadius = 15 / max(zoomScale, 0.01)
        return data.nodes.first { node in
            guard let position = nodePositions[node.id] else { return false }
            return hypot(position.x - graphPoint.x, position.y - graphPoint.y) < hitRadius
        }
    }
}

/// Deterministic, bounded force-directed layout for the graph canvas.
///
/// Positions are kept in canvas coordinates. Pan and zoom are applied only at
/// render/hit-test time, so changing the window size can safely recompute the
/// layout without retaining bounds from the previous canvas.
struct GraphLayout {
    static let minimumZoom: CGFloat = 0.5
    static let maximumZoom: CGFloat = 4

    private static let nodeRadius: CGFloat = 5
    private static let labelHeight: CGFloat = 14
    private static let canvasPadding: CGFloat = 12
    private static let characterWidth: CGFloat = 7

    static func layout(data: GraphData, in size: CGSize) -> [String: CGPoint] {
        guard !data.nodes.isEmpty, size.width > 1, size.height > 1 else { return [:] }

        let center = CGPoint(x: size.width / 2, y: size.height / 2)
        let count = data.nodes.count
        let radius = max(0, min(size.width, size.height) * 0.28)
        var positions = data.nodes.enumerated().map { index, node in
            let angle = CGFloat(index) / CGFloat(max(count, 1)) * 2 * .pi
            let point = CGPoint(
                x: center.x + cos(angle) * radius,
                y: center.y + sin(angle) * radius
            )
            return constrain(point, label: node.label, in: size)
        }

        let nodeIndices = Dictionary(uniqueKeysWithValues: data.nodes.enumerated().map { ($1.id, $0) })
        let area = max(size.width * size.height, 1)
        // The ideal distance scales with occupied area and node count rather
        // than with just the width. This prevents attraction from exploding on
        // a narrow window and keeps sparse graphs centered and readable.
        let idealDistance = max(10, sqrt(area / CGFloat(count)) * 0.65)
        let centerForce: CGFloat = 0.035
        let iterations = count > 500 ? 12 : 55
        let repulsionStride = count > 500 ? max(1, count / 100) : 1

        for iteration in 0..<iterations {
            var forces = Array(repeating: CGPoint.zero, count: count)

            // Repulsion. Large graphs use a deterministic sample to keep a
            // redraw responsive while preserving the same force scale.
            for index in 0..<count {
                var other = index + 1
                while other < count {
                    let dx = positions[index].x - positions[other].x
                    let dy = positions[index].y - positions[other].y
                    let distance = max(hypot(dx, dy), 0.5)
                    let force = idealDistance * idealDistance / distance
                    let fx = dx / distance * force
                    let fy = dy / distance * force
                    forces[index].x += fx
                    forces[index].y += fy
                    forces[other].x -= fx
                    forces[other].y -= fy
                    other += repulsionStride
                }
            }

            // Attraction along edges uses the same ideal-distance scale as
            // repulsion, with a modest coefficient to avoid edge overshoot.
            for edge in data.edges {
                guard let source = nodeIndices[edge.source],
                      let target = nodeIndices[edge.target] else { continue }
                let dx = positions[source].x - positions[target].x
                let dy = positions[source].y - positions[target].y
                let distance = max(hypot(dx, dy), 0.5)
                let force = (distance * distance / idealDistance) * 0.012
                let fx = dx / distance * force
                let fy = dy / distance * force
                forces[source].x -= fx
                forces[source].y -= fy
                forces[target].x += fx
                forces[target].y += fy
            }

            // A small centering force prevents disconnected components from
            // drifting to a canvas edge while still allowing graph structure
            // to determine the final arrangement.
            for index in 0..<count {
                forces[index].x += (center.x - positions[index].x) * centerForce
                forces[index].y += (center.y - positions[index].y) * centerForce
            }

            let progress = CGFloat(iteration) / CGFloat(max(iterations - 1, 1))
            let temperature = max(1.5, min(size.width, size.height) * 0.09) * (1 - progress)
            for index in 0..<count {
                let magnitude = max(hypot(forces[index].x, forces[index].y), 0.001)
                let step = min(temperature, magnitude)
                let candidate = CGPoint(
                    x: positions[index].x + forces[index].x / magnitude * step,
                    y: positions[index].y + forces[index].y / magnitude * step
                )
                positions[index] = constrain(candidate, label: data.nodes[index].label, in: size)
            }
        }

        return Dictionary(uniqueKeysWithValues: data.nodes.enumerated().map {
            ($1.id, positions[$0])
        })
    }

    static func displayLabel(_ label: String, in size: CGSize) -> String {
        let maxWidth = max(0, size.width - canvasPadding * 2)
        let maxCharacters = Int(maxWidth / characterWidth)
        guard maxCharacters > 0 else { return "" }
        guard label.count > maxCharacters else { return label }
        guard maxCharacters > 1 else { return "…" }
        return String(label.prefix(maxCharacters - 1)) + "…"
    }

    static func labelRect(for label: String, at position: CGPoint, in size: CGSize) -> CGRect {
        let display = displayLabel(label, in: size)
        let width = min(
            maxWidth(for: size),
            max(characterWidth, CGFloat(display.count) * characterWidth)
        )
        return CGRect(
            x: position.x - width / 2,
            y: position.y + 12 - labelHeight / 2,
            width: width,
            height: labelHeight
        )
    }

    private static func constrain(_ point: CGPoint, label: String, in size: CGSize) -> CGPoint {
        let display = displayLabel(label, in: size)
        let labelWidth = min(
            maxWidth(for: size),
            max(characterWidth, CGFloat(display.count) * characterWidth)
        )
        let minX = canvasPadding + labelWidth / 2
        let maxX = size.width - canvasPadding - labelWidth / 2
        let minY = canvasPadding + nodeRadius
        let maxY = size.height - canvasPadding - nodeRadius - labelHeight
        return CGPoint(
            x: minX <= maxX ? min(max(point.x, minX), maxX) : size.width / 2,
            y: minY <= maxY ? min(max(point.y, minY), maxY) : size.height / 2
        )
    }

    private static func maxWidth(for size: CGSize) -> CGFloat {
        max(0, size.width - canvasPadding * 2)
    }
}
