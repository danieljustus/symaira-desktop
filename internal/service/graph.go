package service

import (
	"path/filepath"

	"github.com/danieljustus/symaira-desktop/internal/compose"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

type GraphNode struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type GraphEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

type GraphData struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

// Graph returns all nodes and edges for the vault graph.
func (s *Service) Graph() (*GraphData, error) {
	// We need a custom query to get all files and links
	docs, err := s.DB.ListFiles("")
	if err != nil {
		return nil, err
	}

	nodes := make([]GraphNode, 0, len(docs))
	nodeMap := make(map[string]bool)

	for _, d := range docs {
		relPath, _ := filepath.Rel(s.VaultRoot, d.Path)
		nodes = append(nodes, GraphNode{
			ID:    relPath,
			Label: d.Title,
		})
		nodeMap[relPath] = true
	}

	// We don't have a GetLinks method that returns all links efficiently.
	// Let's just expose a method in sidecar to get all edges.
	edgesRaw, err := s.DB.GetAllLinks()
	if err != nil {
		return nil, err
	}

	var edges []GraphEdge
	for _, e := range edgesRaw {
		relSource, _ := filepath.Rel(s.VaultRoot, e.Source)
		targetID := e.Target
		if !nodeMap[targetID] {
			if nodeMap[targetID+".md"] {
				targetID = targetID + ".md"
			}
		}

		edges = append(edges, GraphEdge{
			Source: relSource,
			Target: targetID,
		})
	}

	// Enrich with symmemory if available
	if ok, _ := compose.HasSymmemory(); ok {
		entities, err := compose.ListEntities()
		if err == nil {
			for _, e := range entities {
				hasMatch := false
				for _, d := range docs {
					otherDoc, err := vault.ParseFile(d.Path)
					if err == nil && matchesOther(otherDoc, e) {
						hasMatch = true
						relPath, _ := filepath.Rel(s.VaultRoot, d.Path)
						edges = append(edges, GraphEdge{
							Source: relPath,
							Target: "entity:" + e.Name,
						})
					}
				}

				if hasMatch {
					nodes = append(nodes, GraphNode{
						ID:    "entity:" + e.Name,
						Label: e.Name + " (" + e.Type + ")",
					})

					// Fetch neighbors of the matched entity to get relations
					neighbors, err := compose.GetNeighbors(e.Name)
					if err == nil && neighbors != nil {
						for _, node := range neighbors.Nodes {
							nodeID := "entity:" + node.Name
							nodeAdded := false
							for _, gn := range nodes {
								if gn.ID == nodeID {
									nodeAdded = true
									break
								}
							}
							if !nodeAdded {
								nodes = append(nodes, GraphNode{
									ID:    nodeID,
									Label: node.Name + " (" + node.Type + ")",
								})
							}

							for _, rel := range neighbors.Edges {
								if (rel.FromEntityID == e.ID && rel.ToEntityID == node.ID) ||
									(rel.FromEntityID == node.ID && rel.ToEntityID == e.ID) {
									fromName := e.Name
									toName := node.Name
									if rel.FromEntityID == node.ID {
										fromName = node.Name
										toName = e.Name
									}
									// Avoid duplicate edges by searching existing ones
									edgeExists := false
									edgeSrc := "entity:" + fromName
									edgeTgt := "entity:" + toName
									for _, eg := range edges {
										if eg.Source == edgeSrc && eg.Target == edgeTgt {
											edgeExists = true
											break
										}
									}
									if !edgeExists {
										edges = append(edges, GraphEdge{
											Source: edgeSrc,
											Target: edgeTgt,
										})
									}
								}
							}
						}
					}
				}
			}
		}
	}

	return &GraphData{
		Nodes: nodes,
		Edges: edges,
	}, nil
}
