package selfhost

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

const selfhostHTTPFixtureRel = "../../testdata/port/http/routes.json"

func TestSelfhostHTTPInventory(t *testing.T) {
	oracle := inventory.Oracle{
		Commit:  "ae86331930fdfa2b128b68ae5af7437091b9949a",
		Release: "v0.12.2",
	}
	doc, err := buildHTTPRouteDocument(oracle)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Routes) != 21 {
		t.Fatalf("expected 21 production-derived routes, got %d", len(doc.Routes))
	}

	mux := http.NewServeMux()
	s := &Server{mux: mux}
	s.routes()
	for _, route := range doc.Routes {
		path := strings.NewReplacer(
			"{id}", "test-id",
			"{token}", "test-token",
		).Replace(route.Path)
		req := httptest.NewRequest(route.Method, path, nil)
		_, pattern := mux.Handler(req)
		if pattern == "" {
			t.Errorf("production-derived route %s %s was not recognized by Server mux", route.Method, route.Path)
		}
	}

	content, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("marshal http routes document: %v", err)
	}
	content = append(content, '\n')
	fixturePath := filepath.Clean(selfhostHTTPFixtureRel)
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
		t.Fatalf("Selfhost HTTP inventory has drifted from %s; run make port-fixtures-generate", fixturePath)
	}
}

// buildHTTPRouteDocument parses the production routes method rather than
// maintaining a second route table in tests. It freezes registration-level
// authentication; handler-internal role/permission checks belong to HTTP
// behavior fixtures in later slices.
func buildHTTPRouteDocument(oracle inventory.Oracle) (inventory.HTTPRouteDocument, error) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		return inventory.HTTPRouteDocument{}, fmt.Errorf("resolve route test source path")
	}
	serverPath := filepath.Join(filepath.Dir(currentFile), "server.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, serverPath, nil, 0)
	if err != nil {
		return inventory.HTTPRouteDocument{}, fmt.Errorf("parse production routes: %w", err)
	}
	var routesFunction *ast.FuncDecl
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Name.Name == "routes" {
			routesFunction = function
			break
		}
	}
	if routesFunction == nil {
		return inventory.HTTPRouteDocument{}, fmt.Errorf("production routes method not found")
	}

	var routes []inventory.HTTPRouteSpec
	ast.Inspect(routesFunction.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (selector.Sel.Name != "Handle" && selector.Sel.Name != "HandleFunc") || !isServerMux(selector.X) {
			return true
		}
		pattern, ok := stringLiteral(call.Args[0])
		if !ok {
			return true
		}
		method, path, ok := strings.Cut(pattern, " ")
		if !ok || method == "" || path == "" {
			return true
		}
		handler, authenticated := registeredHandler(call.Args[1])
		auth := "none"
		if authenticated {
			auth = "auth_middleware"
		}
		routes = append(routes, inventory.HTTPRouteSpec{
			Method:  method,
			Path:    path,
			Auth:    auth,
			Handler: handler,
		})
		return false
	})
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Path == routes[j].Path {
			return routes[i].Method < routes[j].Method
		}
		return routes[i].Path < routes[j].Path
	})
	return inventory.HTTPRouteDocument{SchemaVersion: 1, Oracle: oracle, Routes: routes}, nil
}

func isServerMux(expr ast.Expr) bool {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "mux" {
		return false
	}
	ident, ok := selector.X.(*ast.Ident)
	return ok && ident.Name == "s"
}

func registeredHandler(expr ast.Expr) (string, bool) {
	switch value := expr.(type) {
	case *ast.FuncLit:
		return "inline", false
	case *ast.SelectorExpr:
		if receiver, ok := value.X.(*ast.Ident); ok && receiver.Name == "s" {
			return value.Sel.Name, false
		}
	case *ast.CallExpr:
		if selector, ok := value.Fun.(*ast.SelectorExpr); ok {
			if receiver, ok := selector.X.(*ast.Ident); ok && receiver.Name == "s" && selector.Sel.Name == "auth" {
				handler, _ := registeredHandler(value.Args[0])
				return handler, true
			}
		}
		if len(value.Args) > 0 {
			return registeredHandler(value.Args[0])
		}
	}
	return "unknown", false
}

func stringLiteral(expr ast.Expr) (string, bool) {
	literal, ok := expr.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(literal.Value)
	return value, err == nil
}

func TestBuildHTTPRouteDocumentIsDeterministicAndProductionDerived(t *testing.T) {
	oracle := inventory.Oracle{Commit: "test", Release: "test"}
	first, err := buildHTTPRouteDocument(oracle)
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildHTTPRouteDocument(oracle)
	if err != nil {
		t.Fatal(err)
	}
	left, _ := json.Marshal(first)
	right, _ := json.Marshal(second)
	if !bytes.Equal(left, right) {
		t.Fatal("HTTP route document generation is not deterministic")
	}
	for _, route := range first.Routes {
		if route.Handler == "unknown" {
			t.Fatalf("route handler was not derived: %#v", route)
		}
	}
}
