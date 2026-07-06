package service

import (
	"path/filepath"
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
		// e.Target is either a title or a relative path
		// If it's a title, we might need to resolve it, but for now we just use it as is if it matches a node, or append ".md"
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

	return &GraphData{
		Nodes: nodes,
		Edges: edges,
	}, nil
}
