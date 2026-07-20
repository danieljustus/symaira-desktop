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

type graphEdgeKey struct {
	source string
	target string
}

// Graph returns all nodes and edges for the vault graph.
func (s *Service) Graph() (*GraphData, error) {
	// We need a custom query to get all files and links
	docs, err := s.DB.ListFiles("")
	if err != nil {
		return nil, err
	}

	nodes := make([]GraphNode, 0, len(docs))
	nodeSet := make(map[string]bool, len(docs))

	addNode := func(id, label string) {
		if nodeSet[id] {
			return
		}
		nodeSet[id] = true
		nodes = append(nodes, GraphNode{ID: id, Label: label})
	}

	for _, d := range docs {
		relPath, _ := filepath.Rel(s.VaultRoot, d.Path)
		addNode(relPath, d.Title)
	}

	// We don't have a GetLinks method that returns all links efficiently.
	// Let's just expose a method in sidecar to get all edges.
	edgesRaw, err := s.DB.GetAllLinks()
	if err != nil {
		return nil, err
	}

	edges := make([]GraphEdge, 0, len(edgesRaw))
	edgeSet := make(map[graphEdgeKey]bool, len(edgesRaw))
	addEdge := func(source, target string) {
		key := graphEdgeKey{source, target}
		if edgeSet[key] {
			return
		}
		edgeSet[key] = true
		edges = append(edges, GraphEdge{Source: source, Target: target})
	}

	for _, e := range edgesRaw {
		relSource, _ := filepath.Rel(s.VaultRoot, e.Source)
		targetID := e.Target
		if !nodeSet[targetID] {
			if nodeSet[targetID+".md"] {
				targetID = targetID + ".md"
			}
		}
		addEdge(relSource, targetID)
	}

	// Enrich with symmemory if available
	if ok, _ := compose.HasSymmemory(); ok {
		entities, err := compose.ListEntities()
		if err == nil {
			// Parse every vault file exactly once and reuse it across all
			// entities below, instead of re-parsing the whole vault from disk
			// per entity (O(entities x documents) file I/O).
			parsedDocs := make([]*vault.Document, 0, len(docs))
			for _, d := range docs {
				otherDoc, err := vault.ParseFile(d.Path)
				if err == nil {
					parsedDocs = append(parsedDocs, otherDoc)
				}
			}

			for _, e := range entities {
				hasMatch := false
				for _, otherDoc := range parsedDocs {
					if matchesOther(otherDoc, e) {
						hasMatch = true
						relPath, _ := filepath.Rel(s.VaultRoot, otherDoc.Path)
						addEdge(relPath, "entity:"+e.Name)
					}
				}

				if hasMatch {
					addNode("entity:"+e.Name, e.Name+" ("+e.Type+")")

					// Fetch neighbors of the matched entity to get relations
					neighbors, err := compose.GetNeighbors(e.Name)
					if err == nil && neighbors != nil {
						for _, node := range neighbors.Nodes {
							addNode("entity:"+node.Name, node.Name+" ("+node.Type+")")

							for _, rel := range neighbors.Edges {
								if (rel.FromEntityID == e.ID && rel.ToEntityID == node.ID) ||
									(rel.FromEntityID == node.ID && rel.ToEntityID == e.ID) {
									fromName := e.Name
									toName := node.Name
									if rel.FromEntityID == node.ID {
										fromName = node.Name
										toName = e.Name
									}
									addEdge("entity:"+fromName, "entity:"+toName)
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
