package service

import (
	"path/filepath"
	"strings"

	"github.com/danieljustus/symaira-desktop/internal/simhash"
)

// DefaultDuplicateThreshold is the single similarity percentage that decides
// what counts as a near-duplicate. Every entry point into the SimHash scan —
// the `duplicates` command, the `vault health` scan and the SimilarAll
// fallback — resolves to this value, so the same vault yields the same answer
// whichever one is used (issue #452). The value was measured while fixing
// #439: at 85 a genuinely near-identical pair is still grouped, while
// documents that merely share a frontmatter and heading skeleton are not.
// The macOS app's DeskCore.defaultDuplicateThreshold mirrors it.
const DefaultDuplicateThreshold = 85

// ResolveDuplicateThreshold maps a caller-supplied threshold onto the value
// actually used by the scan: a non-positive threshold means "unset" and falls
// back to DefaultDuplicateThreshold.
func ResolveDuplicateThreshold(threshold int) int {
	if threshold <= 0 {
		return DefaultDuplicateThreshold
	}
	return threshold
}

// DuplicateGroup is one cluster of mutually similar documents, assembled by a
// vault-wide pairwise simhash scan (issue #307).
type DuplicateGroup struct {
	// Representative of the group: the first document in path order.
	Path  string `json:"path"`
	Title string `json:"title"`
	// Members sorted by similarity to the representative, highest first.
	Members []DuplicateMember `json:"members"`
}

type DuplicateMember struct {
	Path       string `json:"path"`
	Title      string `json:"title"`
	Similarity int    `json:"similarity"`
}

// vaultRel converts an absolute vault path to a vault-relative path (with
// forward slashes), matching the app-facing `docs list` output. Absolute
// paths outside the vault are passed through unchanged.
func vaultRel(vaultRoot, path string) string {
	rel, err := filepath.Rel(vaultRoot, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

// SimilarAll scans every indexed document with non-empty body content and
// returns clusters of near-duplicates above the given similarity threshold
// (0-100). Hashes are computed from body text here rather than trusting the
// persisted frontmatter simhash, so titles and metadata cannot create a match.
// Clusters are built by connected components: two documents belong to the same
// group when either is similar to the group's representative, which keeps
// genuine chains (A~B, B~C) together without exploding into O(n²) pair rows.
func (s *Service) SimilarAll(threshold int) ([]DuplicateGroup, error) {
	docs, err := s.DB.AllSimhashes()
	if err != nil {
		return nil, err
	}
	threshold = ResolveDuplicateThreshold(threshold)

	type node struct {
		path    string
		title   string
		body    string
		hash    uint64
		group   int
		visited bool
	}
	nodes := make([]node, 0, len(docs))
	for _, d := range docs {
		body := strings.TrimSpace(d.Body)
		if body == "" {
			continue
		}
		nodes = append(nodes, node{
			path: d.Path, title: d.Title, body: body,
			hash: simhash.Compute(body), group: -1,
		})
	}

	var groups [][]int // indices into nodes
	for i := range nodes {
		if nodes[i].visited {
			continue
		}
		// BFS over the similarity graph.
		queue := []int{i}
		nodes[i].visited = true
		nodes[i].group = len(groups)
		var member []int
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			member = append(member, cur)
			for j := range nodes {
				if nodes[j].visited {
					continue
				}
				if simhash.SimilarityForContent(
					nodes[cur].hash, nodes[j].hash, nodes[cur].body, nodes[j].body,
				) >= threshold {
					nodes[j].visited = true
					nodes[j].group = nodes[cur].group
					queue = append(queue, j)
				}
			}
		}
		if len(member) > 1 {
			groups = append(groups, member)
		}
	}

	// Assemble output: representative = path-order first member; members
	// sorted by similarity to the representative. Paths are vault-relative,
	// matching `docs list` so the app can route them back through the CLI.
	result := make([]DuplicateGroup, 0, len(groups))
	for _, member := range groups {
		rep := nodes[member[0]]
		g := DuplicateGroup{Path: vaultRel(s.VaultRoot, rep.path), Title: rep.title}
		for _, idx := range member[1:] {
			g.Members = append(g.Members, DuplicateMember{
				Path:       vaultRel(s.VaultRoot, nodes[idx].path),
				Title:      nodes[idx].title,
				Similarity: simhash.SimilarityForContent(rep.hash, nodes[idx].hash, rep.body, nodes[idx].body),
			})
		}
		// Stable sort: similarity desc, path asc.
		for i := 1; i < len(g.Members); i++ {
			for j := i; j > 0; j-- {
				a, b := g.Members[j-1], g.Members[j]
				if a.Similarity < b.Similarity || (a.Similarity == b.Similarity && a.Path > b.Path) {
					g.Members[j-1], g.Members[j] = b, a
				} else {
					break
				}
			}
		}
		result = append(result, g)
	}
	return result, nil
}
