import SwiftUI
import SymDeskCore

struct GraphView: View {
    @EnvironmentObject var core: DeskCore
    @State private var graphData: GraphData?
    @State private var nodePositions: [String: CGPoint] = [:]
    
    var onSelectNode: ((String) -> Void)?
    
    var body: some View {
        GeometryReader { geo in
            if let data = graphData {
                Canvas { context, size in
                    // Draw edges
                    for edge in data.edges {
                        if let p1 = nodePositions[edge.source], let p2 = nodePositions[edge.target] {
                            var path = Path()
                            path.move(to: p1)
                            path.addLine(to: p2)
                            context.stroke(path, with: .color(.gray.opacity(0.3)), lineWidth: 1)
                        }
                    }
                    
                    // Draw nodes
                    for node in data.nodes {
                        if let p = nodePositions[node.id] {
                            let rect = CGRect(x: p.x - 5, y: p.y - 5, width: 10, height: 10)
                            context.fill(Path(ellipseIn: rect), with: .color(.accentColor))
                            
                            // Draw label
                            let text = Text(node.label).font(.caption).foregroundColor(.secondary)
                            context.draw(text, at: CGPoint(x: p.x, y: p.y + 10))
                        }
                    }
                }
                .gesture(DragGesture(minimumDistance: 0).onEnded { value in
                    // Handle click
                    if let clickedNode = findNode(at: value.location) {
                        onSelectNode?(clickedNode.id)
                    }
                })
                .onAppear {
                    initializePositions(size: geo.size, data: data)
                    runSimulation(size: geo.size)
                }
            } else {
                ProgressView()
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .task {
            do {
                self.graphData = try await core.getGraph()
            } catch {
                print("Graph error: \(error)")
            }
        }
    }
    
    private func findNode(at point: CGPoint) -> GraphNode? {
        guard let data = graphData else { return nil }
        for node in data.nodes {
            if let p = nodePositions[node.id] {
                let dist = hypot(p.x - point.x, p.y - point.y)
                if dist < 15 {
                    return node
                }
            }
        }
        return nil
    }
    
    private func initializePositions(size: CGSize, data: GraphData) {
        for node in data.nodes {
            nodePositions[node.id] = CGPoint(
                x: CGFloat.random(in: 50...(size.width - 50)),
                y: CGFloat.random(in: 50...(size.height - 50))
            )
        }
    }
    
    // Extremely simplified force-directed simulation
    private func runSimulation(size: CGSize) {
        guard let data = graphData else { return }
        
        let k: CGFloat = sqrt((size.width * size.height) / CGFloat(data.nodes.count))
        let iterations = 50 // Keep low for large graphs
        
        Task {
            for _ in 0..<iterations {
                var displacements: [String: CGPoint] = [:]
                for n in data.nodes { displacements[n.id] = .zero }
                
                // Repulsion
                for i in 0..<data.nodes.count {
                    let v = data.nodes[i]
                    for j in (i+1)..<data.nodes.count {
                        let u = data.nodes[j]
                        if let pv = nodePositions[v.id], let pu = nodePositions[u.id] {
                            let dx = pv.x - pu.x
                            let dy = pv.y - pu.y
                            let dist = max(hypot(dx, dy), 1)
                            let force = (k * k) / dist
                            
                            let fx = (dx / dist) * force
                            let fy = (dy / dist) * force
                            
                            displacements[v.id]?.x += fx
                            displacements[v.id]?.y += fy
                            displacements[u.id]?.x -= fx
                            displacements[u.id]?.y -= fy
                        }
                    }
                }
                
                // Attraction
                for e in data.edges {
                    if let pv = nodePositions[e.source], let pu = nodePositions[e.target] {
                        let dx = pv.x - pu.x
                        let dy = pv.y - pu.y
                        let dist = max(hypot(dx, dy), 1)
                        let force = (dist * dist) / k
                        
                        let fx = (dx / dist) * force
                        let fy = (dy / dist) * force
                        
                        displacements[e.source]?.x -= fx
                        displacements[e.source]?.y -= fy
                        displacements[e.target]?.x += fx
                        displacements[e.target]?.y += fy
                    }
                }
                
                // Update
                await MainActor.run {
                    for n in data.nodes {
                        if let d = displacements[n.id], let p = nodePositions[n.id] {
                            // Limit max displacement
                            let maxD: CGFloat = 10
                            let dx = min(max(d.x, -maxD), maxD)
                            let dy = min(max(d.y, -maxD), maxD)
                            
                            var nx = p.x + dx
                            var ny = p.y + dy
                            
                            // Bounds
                            nx = min(max(nx, 20), size.width - 20)
                            ny = min(max(ny, 20), size.height - 20)
                            
                            nodePositions[n.id] = CGPoint(x: nx, y: ny)
                        }
                    }
                }
                
                try? await Task.sleep(nanoseconds: 16_000_000) // ~60fps
            }
        }
    }
}
