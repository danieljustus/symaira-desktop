package parse

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/danieljustus/symaira-desktop/internal/draw/ir"
)

var (
	// Regex matching unsupported Mermaid diagram header keywords.
	unsupportedDiagramRegex = regexp.MustCompile(`(?i)^(sequencediagram|pie|classdiagram(-v2)?|statediagram(-v2)?|erdiagram|gantt|gitgraph|journey|mindmap|quadrantchart|xychart(-beta)?|requirementdiagram|c4context|c4container|c4component|c4dynamic|c4deployment|architecture(-beta)?|packet(-beta)?|kanban|block(-beta)?|sankey(-beta)?)\b`)

	// Regex matching unsupported inline directives in flowchart source.
	unsupportedDirectiveRegex = regexp.MustCompile(`(?i)^(click|style|classdef|class|linkstyle|acctitle|accdescr|direction|interpolate)\b`)

	// Delimiter pairs for node shapes, ordered by delimiter length descending.
	nodeShapeDelimiters = []struct {
		open  string
		close string
		shape ir.NodeShape
	}{
		{open: "((", close: "))", shape: ir.ShapeCircle},
		{open: "([", close: "])", shape: ir.ShapeStadium},
		{open: "[[", close: "]]", shape: ir.ShapeSubroutine},
		{open: "[(", close: ")]", shape: ir.ShapeCylinder},
		{open: "{{", close: "}}", shape: ir.ShapeHexagon},
		{open: ">", close: "]", shape: ir.ShapeAsymmetric},
		{open: "[", close: "]", shape: ir.ShapeRect},
		{open: "(", close: ")", shape: ir.ShapeRound},
		{open: "{", close: "}", shape: ir.ShapeDiamond},
	}
)

type connectorInfo struct {
	style ir.EdgeStyle
	arrow ir.ArrowType
	label string
}

// ParseMermaid parses a Mermaid diagram source text adhering to the documented
// SymDraw Mermaid subset contract into an ir.Diagram. Unsupported constructs
// return a typed ParseError naming the offending construct with an actionable hint.
func ParseMermaid(source string) (*ir.Diagram, error) {
	return parseMermaidInternal(source)
}

func parseMermaidInternal(source string) (*ir.Diagram, error) {
	lines := strings.Split(source, "\n")
	if len(lines) == 0 {
		return nil, &ParseError{
			Stage:   "parse",
			Message: "empty diagram source",
			Hint:    "Provide a valid Mermaid diagram starting with 'graph <direction>' or 'flowchart <direction>' (e.g. 'graph TD').",
		}
	}

	diag := &ir.Diagram{
		Kind:    ir.KindGraph,
		Version: DialectVersion,
	}

	lineIdx := 0

	// 1. Check optional YAML frontmatter block (--- ... ---)
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		lineIdx = 1
		for lineIdx < len(lines) {
			trimmed := strings.TrimSpace(lines[lineIdx])
			if trimmed == "---" {
				lineIdx++
				break
			}
			if strings.HasPrefix(trimmed, "title:") {
				diag.Title = strings.TrimSpace(strings.TrimPrefix(trimmed, "title:"))
				diag.Title = strings.Trim(diag.Title, `"'`)
			} else if strings.HasPrefix(trimmed, "theme:") {
				diag.Theme = strings.TrimSpace(strings.TrimPrefix(trimmed, "theme:"))
				diag.Theme = strings.Trim(diag.Theme, `"'`)
			}
			lineIdx++
		}
	}

	// 2. Find and parse header line
	var headerLine string
	var headerLineNum int

	for lineIdx < len(lines) {
		raw := lines[lineIdx]
		lineNum := lineIdx + 1
		lineIdx++

		cleaned := stripComment(raw)
		trimmed := strings.TrimSpace(cleaned)
		if trimmed == "" {
			continue
		}

		headerLine = trimmed
		headerLineNum = lineNum
		break
	}

	if headerLine == "" {
		return nil, &ParseError{
			Stage:   "parse",
			Message: "empty diagram source",
			Hint:    "Provide a valid Mermaid diagram starting with 'graph <direction>' or 'flowchart <direction>' (e.g. 'graph TD').",
		}
	}

	// Check if header is an unsupported diagram kind
	if match := unsupportedDiagramRegex.FindString(headerLine); match != "" {
		hint := unsupportedDiagramHint(match)
		return nil, NewUnsupportedDiagramError(match, hint, headerLineNum, lines[headerLineNum-1])
	}

	headerParts := strings.Fields(headerLine)
	firstWord := strings.ToLower(headerParts[0])

	if firstWord != "graph" && firstWord != "flowchart" {
		return nil, NewSyntaxError(
			fmt.Sprintf("unknown or unsupported diagram header %q", headerParts[0]),
			headerLineNum,
			lines[headerLineNum-1],
			"Diagram source must start with 'graph <direction>' or 'flowchart <direction>' (e.g. 'graph TD' or 'flowchart LR').",
		)
	}

	direction := ir.DirTD
	if len(headerParts) > 1 {
		dirStr := strings.ToUpper(headerParts[1])
		switch dirStr {
		case "TD", "TB":
			direction = ir.DirTD
		case "BT":
			direction = ir.DirBT
		case "LR":
			direction = ir.DirLR
		case "RL":
			direction = ir.DirRL
		default:
			return nil, NewUnsupportedConstructError(
				fmt.Sprintf("direction %q", headerParts[1]),
				"Supported diagram flow directions are TD, TB, BT, LR, RL.",
				headerLineNum,
				lines[headerLineNum-1],
			)
		}
	}
	diag.Direction = direction

	// 3. Process diagram body
	nodes := make([]ir.Node, 0)
	nodeMap := make(map[string]int) // ID -> index in nodes
	edges := make([]ir.Edge, 0)
	groups := make([]ir.Group, 0)
	subgraphStack := make([]int, 0) // indices in groups

	subgraphLineNums := make(map[int]int)

	registerNode := func(n ir.Node) {
		idx, exists := nodeMap[n.ID]
		if !exists {
			idx = len(nodes)
			nodeMap[n.ID] = idx
			nodes = append(nodes, n)
		} else {
			// Update label, shape, and note if new definition provides explicit values
			if n.Label != "" && (nodes[idx].Label == "" || nodes[idx].Label == n.ID) {
				nodes[idx].Label = n.Label
			}
			if n.Shape != "" && nodes[idx].Shape == ir.ShapeRect {
				nodes[idx].Shape = n.Shape
			}
			if n.Note != "" && nodes[idx].Note == "" {
				nodes[idx].Note = n.Note
			}
		}

		// Add to active subgraphs
		for _, gIdx := range subgraphStack {
			alreadyMember := false
			for _, m := range groups[gIdx].Members {
				if m == n.ID {
					alreadyMember = true
					break
				}
			}
			if !alreadyMember {
				groups[gIdx].Members = append(groups[gIdx].Members, n.ID)
			}
		}
	}

	for lineIdx < len(lines) {
		raw := lines[lineIdx]
		lineNum := lineIdx + 1
		lineIdx++

		cleaned := stripComment(raw)
		trimmed := strings.TrimSpace(cleaned)
		if trimmed == "" {
			continue
		}

		// Split line into semicolon-separated statements (outside quotes)
		stmts, err := splitStatements(trimmed, lineNum, raw)
		if err != nil {
			return nil, err
		}

		for _, stmt := range stmts {
			stmt = strings.TrimSpace(stmt)
			if stmt == "" {
				continue
			}

			// Check title statement
			if strings.HasPrefix(strings.ToLower(stmt), "title ") || strings.HasPrefix(strings.ToLower(stmt), "title:") {
				if diag.Title == "" {
					diag.Title = strings.TrimSpace(stmt[6:])
					diag.Title = strings.Trim(diag.Title, `"'`)
				}
				continue
			}

			// Check subgraph opening
			if isSubgraphStart(stmt) {
				g, err := parseSubgraphHeader(stmt, len(groups)+1, lineNum, raw)
				if err != nil {
					return nil, err
				}
				gIdx := len(groups)
				groups = append(groups, g)
				subgraphStack = append(subgraphStack, gIdx)
				subgraphLineNums[gIdx] = lineNum
				continue
			}

			// Check subgraph closing 'end'
			if strings.EqualFold(stmt, "end") {
				if len(subgraphStack) == 0 {
					return nil, NewSyntaxError(
						"unexpected 'end' outside subgraph",
						lineNum,
						raw,
						"Remove the extraneous 'end' directive.",
					)
				}
				subgraphStack = subgraphStack[:len(subgraphStack)-1]
				continue
			}

			// Check unsupported directives
			if match := unsupportedDirectiveRegex.FindString(stmt); match != "" {
				hint := unsupportedDirectiveHint(match)
				return nil, NewUnsupportedConstructError(match, hint, lineNum, raw)
			}

			// Check init directives
			if strings.Contains(strings.ToLower(stmt), "%%{init:") {
				return nil, NewUnsupportedConstructError(
					"directive %%{init:...}%%",
					"Mermaid init directives are not supported; configure themes via note frontmatter ('theme: ...').",
					lineNum,
					raw,
				)
			}

			// Check invisible link '~~~'
			if strings.Contains(stmt, "~~~") {
				return nil, NewUnsupportedConstructError(
					"invisible link '~~~'",
					"Invisible links ('~~~') are not supported in SymDraw layout engine.",
					lineNum,
					raw,
				)
			}

			// Parse node and edge definitions in statement
			parsedEdges, err := parseStatement(stmt, lineNum, raw, registerNode)
			if err != nil {
				return nil, err
			}
			edges = append(edges, parsedEdges...)
		}
	}

	if len(subgraphStack) > 0 {
		unclosedIdx := subgraphStack[len(subgraphStack)-1]
		g := groups[unclosedIdx]
		startLine := subgraphLineNums[unclosedIdx]
		return nil, &ParseError{
			Stage:   "parse",
			Message: fmt.Sprintf("unclosed subgraph %q", g.ID),
			Detail:  fmt.Sprintf("subgraph started at line %d is missing 'end'", startLine),
			Hint:    "Close the subgraph with 'end'.",
			Line:    startLine,
		}
	}

	// Filter out empty groups to ensure valid IR
	validGroups := make([]ir.Group, 0, len(groups))
	for _, g := range groups {
		if len(g.Members) > 0 {
			validGroups = append(validGroups, g)
		}
	}

	diag.Nodes = nodes
	diag.Edges = edges
	diag.Groups = validGroups

	if err := ir.Validate(diag); err != nil {
		var valErr *ir.ValidationError
		if fmt.Sprintf("%T", err) == "*ir.ValidationError" {
			valErr = err.(*ir.ValidationError)
			return nil, NewContractError(valErr.Field, valErr.Message, "Check diagram structure against SymDraw IR rules.", err)
		}
		return nil, NewContractError("", err.Error(), "Check diagram structure.", err)
	}

	return diag, nil
}

func stripComment(line string) string {
	var sb strings.Builder
	inQuote := false
	var quoteChar rune

	runes := []rune(line)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if inQuote {
			sb.WriteRune(r)
			if r == '\\' && i+1 < len(runes) {
				i++
				sb.WriteRune(runes[i])
			} else if r == quoteChar {
				inQuote = false
			}
			continue
		}

		if r == '"' || r == '\'' {
			inQuote = true
			quoteChar = r
			sb.WriteRune(r)
			continue
		}

		if r == '%' && i+1 < len(runes) && runes[i+1] == '%' && (i+2 >= len(runes) || runes[i+2] != '{') {
			// Comment begins; ignore the rest of the line. An init
			// directive (%%{init: ...}%%) is not a comment and reaches
			// the unsupported-directive check below.
			break
		}

		sb.WriteRune(r)
	}

	return sb.String()
}

func splitStatements(line string, lineNum int, rawLine string) ([]string, error) {
	var stmts []string
	var cur strings.Builder
	inQuote := false
	var quoteChar rune

	runes := []rune(line)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if inQuote {
			cur.WriteRune(r)
			if r == '\\' && i+1 < len(runes) {
				i++
				cur.WriteRune(runes[i])
			} else if r == quoteChar {
				inQuote = false
			}
			continue
		}

		if r == '"' || r == '\'' {
			inQuote = true
			quoteChar = r
			cur.WriteRune(r)
			continue
		}

		if r == ';' {
			stmts = append(stmts, cur.String())
			cur.Reset()
			continue
		}

		cur.WriteRune(r)
	}

	if inQuote {
		return nil, NewSyntaxError(
			"unclosed quote string literal",
			lineNum,
			rawLine,
			fmt.Sprintf("Close string literal with matching quote (%c).", quoteChar),
		)
	}

	stmts = append(stmts, cur.String())
	return stmts, nil
}

func isSubgraphStart(stmt string) bool {
	lower := strings.ToLower(stmt)
	return strings.HasPrefix(lower, "subgraph ") || strings.HasPrefix(lower, "subgraph[") || strings.HasPrefix(lower, "subgraph(") || strings.HasPrefix(lower, "subgraph\"")
}

func parseSubgraphHeader(stmt string, autoIdx int, lineNum int, rawLine string) (ir.Group, error) {
	trimmed := strings.TrimSpace(stmt)
	// Remove "subgraph" prefix case-insensitively
	rest := strings.TrimSpace(trimmed[len("subgraph"):])

	if rest == "" {
		id := fmt.Sprintf("subgraph_%d", autoIdx)
		return ir.Group{ID: id, Label: id, Members: []string{}}, nil
	}

	// Check if id is followed by [Label] or (Label)
	for _, shape := range []struct{ open, close string }{{"[", "]"}, {"(", ")"}} {
		if openIdx := strings.Index(rest, shape.open); openIdx != -1 {
			if strings.HasSuffix(rest, shape.close) {
				id := strings.TrimSpace(rest[:openIdx])
				label := strings.TrimSpace(rest[openIdx+1 : len(rest)-1])
				label = strings.Trim(label, `"'`)
				if id == "" {
					id = slugify(label)
					if id == "" {
						id = fmt.Sprintf("subgraph_%d", autoIdx)
					}
				}
				return ir.Group{ID: id, Label: label, Members: []string{}}, nil
			}
		}
	}

	// Subgraph Title
	title := strings.Trim(rest, `"'`)
	id := slugify(title)
	if id == "" {
		id = fmt.Sprintf("subgraph_%d", autoIdx)
	}
	return ir.Group{ID: id, Label: title, Members: []string{}}, nil
}

func slugify(s string) string {
	var sb strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			sb.WriteRune(r)
		} else if r == ' ' || r == '\t' {
			sb.WriteRune('_')
		}
	}
	return sb.String()
}

func parseStatement(stmt string, lineNum int, rawLine string, registerNode func(ir.Node)) ([]ir.Edge, error) {
	curr := stmt
	var fromNodes []ir.Node
	var edges []ir.Edge

	// 1. Scan the first node group
	nodes, rest, err := scanNodeGroup(curr, lineNum, rawLine)
	if err != nil {
		return nil, err
	}
	for _, n := range nodes {
		registerNode(n)
	}
	fromNodes = nodes
	curr = strings.TrimSpace(rest)

	// If no connector follows, this was a standalone node definition
	if curr == "" {
		return nil, nil
	}

	// 2. Loop while connectors and next node groups exist
	for curr != "" {
		conn, restConn, isConn, err := scanConnector(curr, lineNum, rawLine)
		if err != nil {
			return nil, err
		}
		if !isConn {
			return nil, NewSyntaxError(
				fmt.Sprintf("unexpected token %q", curr),
				lineNum,
				rawLine,
				"Expected an arrow connector (e.g. '-->', '-.->', '==>') or statement delimiter.",
			)
		}

		curr = strings.TrimSpace(restConn)
		if curr == "" {
			return nil, NewSyntaxError(
				"missing target node in edge definition",
				lineNum,
				rawLine,
				"Specify a destination node after the arrow (e.g. 'A --> B').",
			)
		}

		toNodes, restTo, err := scanNodeGroup(curr, lineNum, rawLine)
		if err != nil {
			return nil, err
		}
		if len(toNodes) == 0 {
			return nil, NewSyntaxError(
				"missing target node in edge definition",
				lineNum,
				rawLine,
				"Specify a destination node after the arrow (e.g. 'A --> B').",
			)
		}

		for _, n := range toNodes {
			registerNode(n)
		}

		for _, fn := range fromNodes {
			for _, tn := range toNodes {
				edges = append(edges, ir.Edge{
					From:  fn.ID,
					To:    tn.ID,
					Label: conn.label,
					Style: conn.style,
					Arrow: conn.arrow,
				})
			}
		}

		fromNodes = toNodes
		curr = strings.TrimSpace(restTo)
	}

	return edges, nil
}

func scanNodeGroup(s string, lineNum int, rawLine string) ([]ir.Node, string, error) {
	curr := strings.TrimSpace(s)
	if curr == "" {
		return nil, "", nil
	}

	var nodes []ir.Node
	for {
		node, rest, err := scanNodeSpec(curr, lineNum, rawLine)
		if err != nil {
			return nil, "", err
		}
		nodes = append(nodes, node)
		curr = strings.TrimSpace(rest)

		if strings.HasPrefix(curr, "&") {
			curr = strings.TrimSpace(strings.TrimPrefix(curr, "&"))
			if curr == "" {
				return nil, "", NewSyntaxError(
					"missing node following '&' operator",
					lineNum,
					rawLine,
					"Specify a valid node identifier following '&' (e.g. 'A & B --> C').",
				)
			}
			continue
		}
		break
	}

	return nodes, curr, nil
}

func scanNodeSpec(s string, lineNum int, rawLine string) (ir.Node, string, error) {
	curr := strings.TrimSpace(s)
	if curr == "" {
		return ir.Node{}, "", NewSyntaxError("expected node identifier", lineNum, rawLine, "Specify a node identifier (e.g. 'A').")
	}

	// Scan ID until whitespace, shape delimiter or connector
	runes := []rune(curr)
	endID := 0
	for endID < len(runes) {
		r := runes[endID]
		// Shape opener or group operator
		if r == '[' || r == '(' || r == '{' || r == '>' || r == '&' || r == ';' {
			break
		}
		if r == ' ' || r == '\t' {
			break
		}
		// Check if char starts an arrow connector
		sub := string(runes[endID:])
		if isArrowConnectorStart(sub) {
			break
		}
		endID++
	}

	id := string(runes[:endID])
	if id == "" {
		return ir.Node{}, "", NewSyntaxError(
			fmt.Sprintf("invalid node identifier near %q", curr),
			lineNum,
			rawLine,
			"Node identifier must be an alphanumeric name (e.g. 'A', 'node_1').",
		)
	}

	rest := strings.TrimSpace(string(runes[endID:]))
	if rest == "" {
		return ir.Node{ID: id, Label: id, Shape: ir.ShapeRect}, "", nil
	}

	// Check unsupported shape brackets
	if strings.HasPrefix(rest, "(((") {
		return ir.Node{}, "", NewUnsupportedConstructError(
			"shape '(((...)))' (double circle)",
			"Double circle shape is not supported. Use circle ((...)) or stadium ([...]).",
			lineNum,
			rawLine,
		)
	}
	if strings.HasPrefix(rest, "[/") {
		return ir.Node{}, "", NewUnsupportedConstructError(
			"shape '[/.../]' or '[/...\\\\]'",
			"Parallelogram and trapezoid shapes are not supported. Use standard shapes like [rect], (round), {diamond}, or {{hexagon}}.",
			lineNum,
			rawLine,
		)
	}
	if strings.HasPrefix(rest, `[\`) {
		return ir.Node{}, "", NewUnsupportedConstructError(
			"shape '[\\...\\\\]' or '[\\.../]'",
			"Parallelogram and trapezoid shapes are not supported. Use standard shapes like [rect], (round), {diamond}, or {{hexagon}}.",
			lineNum,
			rawLine,
		)
	}

	// Check supported shape delimiters
	for _, shapeDef := range nodeShapeDelimiters {
		if strings.HasPrefix(rest, shapeDef.open) {
			inner, after, err := extractDelimitedContent(rest[len(shapeDef.open):], shapeDef.close, lineNum, rawLine, shapeDef.open)
			if err != nil {
				return ir.Node{}, "", err
			}

			label := strings.TrimSpace(inner)
			note := ""

			// Handle quoted string inside shape brackets
			if (strings.HasPrefix(label, `"`) && strings.HasSuffix(label, `"`)) ||
				(strings.HasPrefix(label, `'`) && strings.HasSuffix(label, `'`)) {
				if len(label) >= 2 {
					label = label[1 : len(label)-1]
					label = unescapeString(label)
				}
			}

			// Handle wikilink syntax inside label: [[Target]] or [[Target|Display]]
			if strings.HasPrefix(label, "[[") && strings.HasSuffix(label, "]]") && len(label) >= 4 {
				innerLink := label[2 : len(label)-2]
				parts := strings.SplitN(innerLink, "|", 2)
				note = strings.TrimSpace(parts[0])
				if len(parts) > 1 {
					label = strings.TrimSpace(parts[1])
				} else {
					label = note
				}
			} else if shapeDef.shape == ir.ShapeSubroutine {
				// For subroutine shape `A[[Label]]`, set Note as vault reference if valid note name
				note = label
			}

			if label == "" {
				label = id
			}

			return ir.Node{
				ID:    id,
				Label: label,
				Shape: shapeDef.shape,
				Note:  note,
			}, after, nil
		}
	}

	// No shape brackets; bare node identifier
	return ir.Node{ID: id, Label: id, Shape: ir.ShapeRect}, rest, nil
}

func extractDelimitedContent(s, closeDelim string, lineNum int, rawLine string, openDelim string) (string, string, error) {
	runes := []rune(s)
	closeRunes := []rune(closeDelim)
	inQuote := false
	var quoteChar rune

	for i := 0; i <= len(runes)-len(closeRunes); i++ {
		r := runes[i]
		if inQuote {
			if r == '\\' && i+1 < len(runes) {
				i++
			} else if r == quoteChar {
				inQuote = false
			}
			continue
		}

		if r == '"' || r == '\'' {
			inQuote = true
			quoteChar = r
			continue
		}

		// Check if closeDelim matches here
		match := true
		for j := 0; j < len(closeRunes); j++ {
			if runes[i+j] != closeRunes[j] {
				match = false
				break
			}
		}

		if match {
			inner := string(runes[:i])
			rest := string(runes[i+len(closeRunes):])
			return inner, rest, nil
		}
	}

	return "", "", NewSyntaxError(
		fmt.Sprintf("unclosed shape delimiter %q", openDelim),
		lineNum,
		rawLine,
		fmt.Sprintf("Close delimiter with matching %q.", closeDelim),
	)
}

func unescapeString(s string) string {
	var sb strings.Builder
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		if runes[i] == '\\' && i+1 < len(runes) {
			i++
			switch runes[i] {
			case 'n':
				sb.WriteRune('\n')
			case 't':
				sb.WriteRune('\t')
			case '"':
				sb.WriteRune('"')
			case '\'':
				sb.WriteRune('\'')
			case '\\':
				sb.WriteRune('\\')
			default:
				sb.WriteRune(runes[i])
			}
			continue
		}
		sb.WriteRune(runes[i])
	}
	return sb.String()
}

func isArrowConnectorStart(s string) bool {
	// Check standard arrow prefixes
	for _, p := range []string{
		"<-->", "-->", "---", "--o", "--x", "o--o", "x--x", "o--", "x--",
		"-.->", "-..->", "-.-", "-..-",
		"==>", "===",
		"..->", "..>", "...", "..",
		"--", "-.", "==",
	} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func scanConnector(s string, lineNum int, rawLine string) (connectorInfo, string, bool, error) {
	curr := strings.TrimSpace(s)
	if curr == "" {
		return connectorInfo{}, "", false, nil
	}

	// 1. Check inline labeled arrows: `-- label -->`, `-. label .->`, `== label ==>`
	if strings.HasPrefix(curr, "-- ") || strings.HasPrefix(curr, "-. ") || strings.HasPrefix(curr, "== ") {
		prefix := curr[:2]
		rest := curr[3:]

		// Find end arrow
		for _, endDef := range []struct {
			arrowStr string
			style    ir.EdgeStyle
			arrow    ir.ArrowType
		}{
			{arrowStr: "-->", style: ir.EdgeSolid, arrow: ir.ArrowSingle},
			{arrowStr: "---", style: ir.EdgeSolid, arrow: ir.ArrowNone},
			{arrowStr: "<-->", style: ir.EdgeSolid, arrow: ir.ArrowDouble},
			{arrowStr: "--o", style: ir.EdgeSolid, arrow: ir.ArrowCircle},
			{arrowStr: "--x", style: ir.EdgeSolid, arrow: ir.ArrowCross},
			{arrowStr: ".->", style: ir.EdgeDashed, arrow: ir.ArrowSingle},
			{arrowStr: "-.-", style: ir.EdgeDashed, arrow: ir.ArrowNone},
			{arrowStr: "==>", style: ir.EdgeThick, arrow: ir.ArrowSingle},
			{arrowStr: "===", style: ir.EdgeThick, arrow: ir.ArrowNone},
		} {
			if idx := strings.Index(rest, " "+endDef.arrowStr); idx != -1 {
				label := strings.TrimSpace(rest[:idx])
				label = strings.Trim(label, `"'`)
				restAfter := rest[idx+1+len(endDef.arrowStr):]
				style := endDef.style
				switch prefix {
				case "-.":
					style = ir.EdgeDashed
				case "==":
					style = ir.EdgeThick
				}
				return connectorInfo{
					style: style,
					arrow: endDef.arrow,
					label: label,
				}, restAfter, true, nil
			}
		}
	}

	// 2. Check standard arrow tokens (ordered by longest prefix first)
	for _, arrowDef := range []struct {
		token string
		style ir.EdgeStyle
		arrow ir.ArrowType
	}{
		{token: "<-->", style: ir.EdgeSolid, arrow: ir.ArrowDouble},
		{token: "o--o", style: ir.EdgeSolid, arrow: ir.ArrowCircle},
		{token: "x--x", style: ir.EdgeSolid, arrow: ir.ArrowCross},
		{token: "-..->", style: ir.EdgeDashed, arrow: ir.ArrowSingle},
		{token: "-.->", style: ir.EdgeDashed, arrow: ir.ArrowSingle},
		{token: "-..-", style: ir.EdgeDashed, arrow: ir.ArrowNone},
		{token: "-.-", style: ir.EdgeDashed, arrow: ir.ArrowNone},
		{token: "-->", style: ir.EdgeSolid, arrow: ir.ArrowSingle},
		{token: "---", style: ir.EdgeSolid, arrow: ir.ArrowNone},
		{token: "--o", style: ir.EdgeSolid, arrow: ir.ArrowCircle},
		{token: "--x", style: ir.EdgeSolid, arrow: ir.ArrowCross},
		{token: "o--", style: ir.EdgeSolid, arrow: ir.ArrowNone},
		{token: "x--", style: ir.EdgeSolid, arrow: ir.ArrowNone},
		{token: "==>", style: ir.EdgeThick, arrow: ir.ArrowSingle},
		{token: "===", style: ir.EdgeThick, arrow: ir.ArrowNone},
		{token: "..->", style: ir.EdgeDotted, arrow: ir.ArrowSingle},
		{token: "..>", style: ir.EdgeDotted, arrow: ir.ArrowSingle},
		{token: "...", style: ir.EdgeDotted, arrow: ir.ArrowNone},
		{token: "..", style: ir.EdgeDotted, arrow: ir.ArrowNone},
	} {
		if strings.HasPrefix(curr, arrowDef.token) {
			rest := curr[len(arrowDef.token):]
			label := ""

			// Check pipe label |label|
			trimmedRest := strings.TrimSpace(rest)
			if strings.HasPrefix(trimmedRest, "|") {
				closeIdx := strings.Index(trimmedRest[1:], "|")
				if closeIdx == -1 {
					return connectorInfo{}, "", false, NewSyntaxError(
						"unclosed edge pipe label '|...|'",
						lineNum,
						rawLine,
						"Close the edge label with a matching '|' (e.g. '-->|label|').",
					)
				}
				label = strings.TrimSpace(trimmedRest[1 : 1+closeIdx])
				label = strings.Trim(label, `"'`)
				rest = trimmedRest[1+closeIdx+1:]
			}

			return connectorInfo{
				style: arrowDef.style,
				arrow: arrowDef.arrow,
				label: label,
			}, rest, true, nil
		}
	}

	return connectorInfo{}, "", false, nil
}

func unsupportedDiagramHint(kind string) string {
	lower := strings.ToLower(kind)
	switch {
	case strings.Contains(lower, "sequence"):
		return "SymDraw Mermaid dialect supports 'graph' and 'flowchart'. For sequence diagrams, use SymDraw IR JSON with kind 'sequence'."
	case strings.Contains(lower, "pie"):
		return "SymDraw Mermaid dialect supports 'graph' and 'flowchart'. For pie charts, use SymDraw IR JSON with kind 'chart' (type 'pie')."
	case strings.Contains(lower, "class"):
		return "Class diagrams are not supported in Mermaid dialect. Use flowchart/graph diagrams or SymDraw IR."
	case strings.Contains(lower, "state"):
		return "State diagrams are not supported in Mermaid dialect. Use flowchart/graph diagrams or SymDraw IR."
	case strings.Contains(lower, "er"):
		return "Entity-Relationship diagrams are not supported in Mermaid dialect. Use flowchart/graph diagrams or SymDraw IR."
	case strings.Contains(lower, "gantt"):
		return "Gantt diagrams are not supported in Mermaid dialect. Use flowchart/graph diagrams or SymDraw IR with kind 'timeline'."
	case strings.Contains(lower, "mindmap"):
		return "Mindmap diagrams are not supported in Mermaid dialect. Use SymDraw IR with kind 'tree'."
	case strings.Contains(lower, "chart"):
		return "Charts are not supported in Mermaid dialect. Use SymDraw IR with kind 'chart'."
	default:
		return fmt.Sprintf("Diagram kind %q is not supported in the SymDraw Mermaid subset. Use flowchart/graph diagrams or SymDraw IR JSON.", kind)
	}
}

func unsupportedDirectiveHint(directive string) string {
	lower := strings.ToLower(directive)
	switch {
	case strings.HasPrefix(lower, "click"):
		return "Interactive click bindings ('click') are not supported in static vector export; use vault note wikilinks or frontmatter links instead."
	case strings.HasPrefix(lower, "style"):
		return "Inline 'style' directives are not supported. Use note frontmatter themes or SymDraw IR node styles."
	case strings.HasPrefix(lower, "classdef"):
		return "CSS class definitions ('classDef') are not supported. Use note frontmatter themes or SymDraw IR."
	case strings.HasPrefix(lower, "class"):
		return "CSS class assignments ('class') are not supported. Use note frontmatter themes or SymDraw IR."
	case strings.HasPrefix(lower, "linkstyle"):
		return "Link styling directives ('linkStyle') are not supported. Use edge arrow syntax (--> , -.-> , ==>) to control stroke styles."
	case strings.HasPrefix(lower, "acctitle"):
		return "Accessibility title directives ('accTitle:') are not supported in diagram source. Use note frontmatter."
	case strings.HasPrefix(lower, "accdescr"):
		return "Accessibility description directives ('accDescr:') are not supported in diagram source. Use note frontmatter."
	case strings.HasPrefix(lower, "direction"):
		return "Per-subgraph direction directives are not supported in SymDraw layered layout; diagram direction applies globally."
	case strings.HasPrefix(lower, "interpolate"):
		return "Interpolation directives ('interpolate') are not supported."
	default:
		return fmt.Sprintf("Directive %q is not supported in the SymDraw Mermaid dialect.", directive)
	}
}
