package vault

import (
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// ExtractMarkdownLinks reads CommonMark link and image destinations, including
// references, without treating code or raw HTML as Markdown links. Wikilinks
// remain in Document.Links and retain their existing graph semantics.
func ExtractMarkdownLinks(body string) []string {
	source := []byte(body)
	document := goldmark.DefaultParser().Parse(text.NewReader(source))
	links := []string{}
	_ = ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		var destination []byte
		switch n := node.(type) {
		case *ast.Link:
			destination = n.Destination
		case *ast.Image:
			destination = n.Destination
		case *ast.AutoLink:
			destination = n.URL(source)
			if n.AutoLinkType == ast.AutoLinkEmail {
				destination = append([]byte("mailto:"), destination...)
			}
		default:
			return ast.WalkContinue, nil
		}
		destination = util.UnescapePunctuations(destination)
		destination = util.ResolveNumericReferences(destination)
		destination = util.ResolveEntityNames(destination)
		links = append(links, string(destination))
		return ast.WalkContinue, nil
	})
	return links
}
