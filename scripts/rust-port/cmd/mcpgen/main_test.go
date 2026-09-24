package main

import "testing"

func TestMCP001InitializeCasesAreGenerated(t *testing.T) {
	cases := make(map[string]mcpCase)
	for _, testCase := range generated().Cases {
		cases[testCase.ID] = testCase
	}
	for _, id := range []string{
		"mcp001-initialize-string-id",
		"mcp001-initialize-null-id",
		"mcp001-ping-string-id",
		"mcp001-initialize-notification",
		"mcp001-null-method",
		"mcp001-invalid-array",
		"mcp001-invalid-method-type",
	} {
		if _, ok := cases[id]; !ok {
			t.Errorf("generated MCP fixture missing %q", id)
		}
	}
}
