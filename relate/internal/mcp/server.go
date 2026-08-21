// Package mcp implements a narrow Model Context Protocol server exposing a
// reviewed subset of symrelate's contact-management contract to MCP
// clients over stdio JSON-RPC 2.0. See docs/MCP.md for the tool catalogue
// and docs/CLI_CONTRACT.md for the shared JSON versioning rules.
//
// The transport layer (framing, JSON-RPC 2.0 validation, error codes, the
// initialize handshake and the zero-stdout-pollution rule) is provided by
// corekit/mcpserver — the same stack the sibling symaira tools use — while
// the tool catalog and its handlers stay here. Every error message that
// can reach a client is passed through security.Redact at the handler
// boundary, so a contact-point value can never leak through an error path.
package mcp

import (
	"context"
	"io"

	"github.com/danieljustus/symaira-corekit/mcpserver"
	"github.com/danieljustus/symaira-relate/internal/app"
	"github.com/danieljustus/symaira-relate/internal/version"
)

// Server serves the reviewed tool catalog over the MCP stdio transport.
// The JSON-RPC machinery lives in *mcpserver.Server; this type wires it to
// the App service boundary shared with the CLI.
type Server struct {
	app *app.App
	srv *mcpserver.Server
}

// New wires a Server against an already-open App and registers the
// 8-tool catalog. The caller owns the App's lifecycle (Open/Close)
// exactly as CLI commands do.
func New(a *app.App) *Server {
	s := &Server{app: a, srv: mcpserver.New(version.Tool, version.Version)}
	s.registerTools()
	return s
}

// Run reads JSON-RPC 2.0 messages from r until EOF or ctx is cancelled,
// dispatches each to the matching method, and writes one response per
// request to w. Stdout carries protocol frames exclusively — mcpserver
// routes all diagnostics to stderr via log/slog — so w stays safe to use
// as a transport even if a caller pipes stdout somewhere unexpected.
func (s *Server) Run(ctx context.Context, r io.Reader, w io.Writer) error {
	return s.srv.ServeIO(ctx, r, w)
}
