package service

import (
	"path/filepath"
	"strings"

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
		relPath = filepath.ToSlash(relPath)
		addNode(relPath, d.Title)
	}

	edgesRaw, err := s.DB.GetAllLinks()
	if err != nil {
		return nil, err
	}

	aliasMap, _ := s.DB.GetAllAliases()

	// Build resolution lookup maps in contract precedence order:
	// 1. Exact relative path
	// 2. Base name / title
	// 3. Aliases
	pathToNode := make(map[string]string, len(docs)*2)
	baseToNode := make(map[string]string, len(docs)*2)
	titleToNode := make(map[string]string, len(docs)*2)
	aliasToNode := make(map[string]string)

	for _, d := range docs {
		relPath, _ := filepath.Rel(s.VaultRoot, d.Path)
		relPath = filepath.ToSlash(relPath)
		lowerRel := strings.ToLower(relPath)
		lowerRelNoExt := strings.ToLower(strings.TrimSuffix(relPath, filepath.Ext(relPath)))
		if _, exists := pathToNode[lowerRel]; !exists {
			pathToNode[lowerRel] = relPath
		}
		if _, exists := pathToNode[lowerRelNoExt]; !exists {
			pathToNode[lowerRelNoExt] = relPath
		}

		baseName := filepath.Base(relPath)
		lowerBase := strings.ToLower(baseName)
		lowerBaseNoExt := strings.ToLower(strings.TrimSuffix(baseName, filepath.Ext(baseName)))
		if _, exists := baseToNode[lowerBase]; !exists {
			baseToNode[lowerBase] = relPath
		}
		if _, exists := baseToNode[lowerBaseNoExt]; !exists {
			baseToNode[lowerBaseNoExt] = relPath
		}

		if d.Title != "" {
			lowerTitle := strings.ToLower(strings.TrimSpace(d.Title))
			if _, exists := titleToNode[lowerTitle]; !exists {
				titleToNode[lowerTitle] = relPath
			}
			lowerTitleNoExt := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(d.Title), filepath.Ext(d.Title)))
			if _, exists := titleToNode[lowerTitleNoExt]; !exists {
				titleToNode[lowerTitleNoExt] = relPath
			}
		}

		if aliases, ok := aliasMap[d.Path]; ok {
			for _, a := range aliases {
				trimmed := strings.TrimSpace(a)
				if trimmed != "" {
					lowerAlias := strings.ToLower(trimmed)
					if _, exists := aliasToNode[lowerAlias]; !exists {
						aliasToNode[lowerAlias] = relPath
					}
					lowerAliasNoExt := strings.ToLower(strings.TrimSuffix(trimmed, filepath.Ext(trimmed)))
					if _, exists := aliasToNode[lowerAliasNoExt]; !exists {
						aliasToNode[lowerAliasNoExt] = relPath
					}
				}
			}
		}
	}

	resolveTarget := func(target string) string {
		trimmed := strings.TrimSpace(target)
		lower := strings.ToLower(trimmed)
		lowerNoExt := strings.ToLower(strings.TrimSuffix(trimmed, filepath.Ext(trimmed)))

		// 1. Exact path
		if node, ok := pathToNode[lower]; ok {
			return node
		}
		if node, ok := pathToNode[lowerNoExt]; ok {
			return node
		}
		// 2. Base name or title
		if node, ok := baseToNode[lower]; ok {
			return node
		}
		if node, ok := baseToNode[lowerNoExt]; ok {
			return node
		}
		if node, ok := titleToNode[lower]; ok {
			return node
		}
		if node, ok := titleToNode[lowerNoExt]; ok {
			return node
		}
		// 3. Aliases
		if node, ok := aliasToNode[lower]; ok {
			return node
		}
		if node, ok := aliasToNode[lowerNoExt]; ok {
			return node
		}

		// Fallback for compatibility
		if nodeSet[target] {
			return target
		}
		if nodeSet[target+".md"] {
			return target + ".md"
		}
		return target
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
		relSource = filepath.ToSlash(relSource)
		targetID := resolveTarget(e.Target)
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
