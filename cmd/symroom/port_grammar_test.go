package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/room/mcp"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

const (
	symroomGrammarFixtureRel = "../../testdata/port/cli/symroom-parser-grammar.json"
	symroomToolsFixtureRel   = "../../testdata/port/mcp/symroom-tools.json"
)

var symroomOracle = inventory.Oracle{
	Commit:  "ae86331930fdfa2b128b68ae5af7437091b9949a",
	Release: "v0.12.2",
}

func TestSymRoomParserGrammar(t *testing.T) {
	doc, err := buildSymRoomGrammar(symroomOracle)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Subcommands) != 16 {
		t.Fatalf("expected 16 subcommands, got %d", len(doc.Subcommands))
	}
	assertSymRoomAction(t, doc, "run", []string{"approve", "cancel", "deny", "list", "request", "show", "start", "wait"})
	assertSymRoomAction(t, doc, "checkpoint", []string{"request", "resolve"})
	writeOrCompareFixture(t, symroomGrammarFixtureRel, doc)
}

func TestSymRoomMCPInventory(t *testing.T) {
	doc, err := buildSymRoomMCPDocument(symroomOracle)
	if err != nil {
		t.Fatalf("build symroom mcp doc: %v", err)
	}
	if len(doc.Tools) != 8 {
		t.Fatalf("expected 8 tools, got %d", len(doc.Tools))
	}
	writeOrCompareFixture(t, symroomToolsFixtureRel, doc)
}

func writeOrCompareFixture(t *testing.T, fixtureRel string, value any) {
	t.Helper()
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	content = append(content, '\n')
	fixturePath := filepath.Clean(fixtureRel)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(fixturePath), 0o750); err != nil {
			t.Fatalf("mkdir fixture dir: %v", err)
		}
		if err := os.WriteFile(fixturePath, content, 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		return
	}
	existing, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read fixture %s: %v (run make port-fixtures-generate)", fixturePath, err)
	}
	if !bytes.Equal(existing, content) {
		t.Fatalf("fixture has drifted from %s; run make port-fixtures-generate", fixturePath)
	}
}

// buildSymRoomGrammar derives the parser inventory from the production dispatch
// and flag declarations. This avoids maintaining a second, hand-written grammar
// that can claim flags or actions the real parser does not accept.
func buildSymRoomGrammar(oracle inventory.Oracle) (inventory.SymRoomGrammarDocument, error) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		return inventory.SymRoomGrammarDocument{}, fmt.Errorf("resolve test source path")
	}
	dir := filepath.Dir(currentFile)
	fset := token.NewFileSet()
	//nolint:staticcheck // ParseDir is intentional: single local package, no build tags
	packages, err := parser.ParseDir(fset, dir, func(info os.FileInfo) bool {
		return strings.HasSuffix(info.Name(), ".go") && !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		return inventory.SymRoomGrammarDocument{}, fmt.Errorf("parse symroom production source: %w", err)
	}
	pkg := packages["main"]
	if pkg == nil {
		return inventory.SymRoomGrammarDocument{}, fmt.Errorf("production package main not found")
	}

	functions := make(map[string]*ast.FuncDecl)
	for _, file := range pkg.Files {
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok {
				functions[fn.Name.Name] = fn
			}
		}
	}
	dispatch := functions["dispatch"]
	if dispatch == nil {
		return inventory.SymRoomGrammarDocument{}, fmt.Errorf("dispatch function not found")
	}
	handlers := dispatchHandlers(dispatch)
	descriptions, order := usageSubcommands(usageText)
	if len(order) != 16 {
		return inventory.SymRoomGrammarDocument{}, fmt.Errorf("usage text exposes %d subcommands, want 16", len(order))
	}

	doc := inventory.SymRoomGrammarDocument{SchemaVersion: 1, Oracle: oracle, UsageText: usageText}
	for _, name := range order {
		handlerName := handlers[name]
		fn := functions[handlerName]
		if handlerName == "" || fn == nil {
			return inventory.SymRoomGrammarDocument{}, fmt.Errorf("usage subcommand %q has no production dispatch handler", name)
		}
		sub := inventory.SymRoomSubcommand{
			Name:         name,
			Description:  descriptions[name],
			Handler:      handlerName,
			Source:       sourceFile(fset, fn),
			UsageStrings: collectUsageStrings(fn.Body),
		}
		if actionSwitch := commandActionSwitch(fn.Body); actionSwitch != nil {
			for _, clauseNode := range actionSwitch.Body.List {
				clause := clauseNode.(*ast.CaseClause)
				names := caseStrings(clause)
				for _, actionName := range names {
					if actionName == "-h" || actionName == "--help" || actionName == "help" {
						continue
					}
					sub.Actions = append(sub.Actions, inventory.SymRoomAction{
						Name:         actionName,
						UsageStrings: collectUsageStrings(clause),
						Flags:        collectASTFlags(fset, clause),
					})
				}
			}
		} else if name == "index" {
			for _, actionName := range comparedActionStrings(fn.Body) {
				sub.Actions = append(sub.Actions, inventory.SymRoomAction{
					Name:         actionName,
					UsageStrings: collectUsageStrings(fn.Body),
				})
			}
		} else {
			sub.Flags = collectASTFlags(fset, fn.Body)
		}
		sort.Slice(sub.Actions, func(i, j int) bool { return sub.Actions[i].Name < sub.Actions[j].Name })
		doc.Subcommands = append(doc.Subcommands, sub)
	}
	return doc, nil
}

func dispatchHandlers(fn *ast.FuncDecl) map[string]string {
	result := make(map[string]string)
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		switchNode, ok := node.(*ast.SwitchStmt)
		if !ok {
			return true
		}
		for _, item := range switchNode.Body.List {
			clause := item.(*ast.CaseClause)
			handler := returnedFunction(clause.Body)
			if handler == "" {
				continue
			}
			for _, name := range caseStrings(clause) {
				if !strings.HasPrefix(name, "-") && name != "help" {
					result[name] = handler
				}
			}
		}
		return false
	})
	return result
}

func returnedFunction(statements []ast.Stmt) string {
	for _, statement := range statements {
		ret, ok := statement.(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			continue
		}
		call, ok := ret.Results[0].(*ast.CallExpr)
		if !ok {
			continue
		}
		if ident, ok := call.Fun.(*ast.Ident); ok && strings.HasPrefix(ident.Name, "run") {
			return ident.Name
		}
	}
	return ""
}

func usageSubcommands(value string) (map[string]string, []string) {
	descriptions := make(map[string]string)
	var order []string
	inList := false
	for _, line := range strings.Split(value, "\n") {
		if line == "Available Subcommands:" {
			inList = true
			continue
		}
		if !inList || strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasPrefix(line, "Use ") {
			break
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := fields[0]
		descriptions[name] = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), name))
		order = append(order, name)
	}
	return descriptions, order
}

func sourceFile(fset *token.FileSet, fn *ast.FuncDecl) string {
	return filepath.Base(fset.Position(fn.Pos()).Filename)
}

func commandActionSwitch(body *ast.BlockStmt) *ast.SwitchStmt {
	var best *ast.SwitchStmt
	bestActions := 0
	ast.Inspect(body, func(node ast.Node) bool {
		switchNode, ok := node.(*ast.SwitchStmt)
		if !ok {
			return true
		}
		count := 0
		for _, item := range switchNode.Body.List {
			for _, value := range caseStrings(item.(*ast.CaseClause)) {
				if value != "-h" && value != "--help" && value != "help" {
					count++
				}
			}
		}
		if count > bestActions {
			best, bestActions = switchNode, count
		}
		return true
	})
	return best
}

func caseStrings(clause *ast.CaseClause) []string {
	var result []string
	for _, expr := range clause.List {
		literal, ok := expr.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			continue
		}
		value, err := strconv.Unquote(literal.Value)
		if err == nil {
			result = append(result, value)
		}
	}
	return result
}

func comparedActionStrings(body *ast.BlockStmt) []string {
	set := make(map[string]bool)
	ast.Inspect(body, func(node ast.Node) bool {
		binary, ok := node.(*ast.BinaryExpr)
		if !ok || (binary.Op != token.EQL && binary.Op != token.NEQ) {
			return true
		}
		literal, ok := binary.Y.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		if _, ok := binary.X.(*ast.IndexExpr); !ok {
			return true
		}
		value, err := strconv.Unquote(literal.Value)
		if err == nil && value != "" {
			set[value] = true
		}
		return true
	})
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func collectUsageStrings(node ast.Node) []string {
	set := make(map[string]bool)
	ast.Inspect(node, func(child ast.Node) bool {
		literal, ok := child.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(literal.Value)
		if err == nil && (strings.Contains(value, "Usage: symroom") || strings.HasPrefix(value, "       symroom")) {
			set[value] = true
		}
		return true
	})
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func collectASTFlags(fset *token.FileSet, node ast.Node) []inventory.SymRoomFlag {
	flags := make(map[string]inventory.SymRoomFlag)
	ast.Inspect(node, func(child ast.Node) bool {
		call, ok := child.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		method := selector.Sel.Name
		nameIndex, defaultIndex, usageIndex := 0, 1, 2
		if strings.HasSuffix(method, "Var") {
			method = strings.TrimSuffix(method, "Var")
			nameIndex, defaultIndex, usageIndex = 1, 2, 3
		}
		supported := map[string]bool{"String": true, "Bool": true, "Int": true, "Int64": true, "Uint": true, "Duration": true, "Float64": true}
		if !supported[method] || len(call.Args) <= usageIndex {
			return true
		}
		name, ok := stringLiteral(call.Args[nameIndex])
		if !ok {
			return true
		}
		usage, _ := stringLiteral(call.Args[usageIndex])
		flag := inventory.SymRoomFlag{
			Name:    name,
			Type:    strings.ToLower(method),
			Default: expressionText(fset, call.Args[defaultIndex]),
			Usage:   usage,
		}
		flags[name] = flag
		return true
	})
	result := make([]inventory.SymRoomFlag, 0, len(flags))
	for _, flag := range flags {
		result = append(result, flag)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func stringLiteral(expr ast.Expr) (string, bool) {
	literal, ok := expr.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(literal.Value)
	return value, err == nil
}

func expressionText(fset *token.FileSet, expr ast.Expr) string {
	if value, ok := stringLiteral(expr); ok {
		return value
	}
	var out bytes.Buffer
	if err := printer.Fprint(&out, fset, expr); err != nil {
		return "<unprintable>"
	}
	return out.String()
}

func assertSymRoomAction(t *testing.T, doc inventory.SymRoomGrammarDocument, command string, want []string) {
	t.Helper()
	for _, sub := range doc.Subcommands {
		if sub.Name != command {
			continue
		}
		got := make([]string, len(sub.Actions))
		for i, action := range sub.Actions {
			got[i] = action.Name
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("%s actions = %v, want %v", command, got, want)
		}
		return
	}
	t.Fatalf("subcommand %q not found", command)
}

func buildSymRoomMCPDocument(oracle inventory.Oracle) (inventory.MCPToolDocument, error) {
	srv := mcp.NewServer(".", nil, "")
	in := bytes.NewBufferString("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\"}\n")
	var out bytes.Buffer
	if err := srv.ServeIO(context.Background(), in, &out); err != nil {
		return inventory.MCPToolDocument{}, err
	}
	var response struct {
		Result struct {
			Tools []struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				InputSchema json.RawMessage `json:"inputSchema"`
				Annotations *struct {
					ReadOnlyHint    bool `json:"readOnlyHint"`
					DestructiveHint bool `json:"destructiveHint"`
				} `json:"annotations"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &response); err != nil {
		return inventory.MCPToolDocument{}, err
	}
	tools := make([]inventory.MCPToolSpec, len(response.Result.Tools))
	for i, tool := range response.Result.Tools {
		tools[i] = inventory.MCPToolSpec{
			Name:        tool.Name,
			Order:       i + 1,
			Description: tool.Description,
			InputSchema: tool.InputSchema,
		}
		if tool.Annotations != nil {
			tools[i].ReadOnly = tool.Annotations.ReadOnlyHint
			tools[i].Destructive = tool.Annotations.DestructiveHint
		}
	}
	return inventory.MCPToolDocument{
		SchemaVersion: 1,
		Oracle:        oracle,
		ServerName:    "symroom",
		ServerVersion: "0.1.0",
		Instructions:  "Use room_* tools to inspect and record the signed room work record. There is no approval-granting tool in this server.",
		Tools:         tools,
	}, nil
}

func TestBuildSymRoomGrammarIsDeterministic(t *testing.T) {
	first, err := buildSymRoomGrammar(inventory.Oracle{Commit: "test", Release: "test"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildSymRoomGrammar(inventory.Oracle{Commit: "test", Release: "test"})
	if err != nil {
		t.Fatal(err)
	}
	left, _ := json.Marshal(first)
	right, _ := json.Marshal(second)
	if !bytes.Equal(left, right) {
		t.Fatal("SymRoom grammar generation is not deterministic")
	}
}
