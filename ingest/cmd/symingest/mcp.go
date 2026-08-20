package main

import (
	"context"
	"flag"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-corekit/mcpserver"
	"github.com/danieljustus/symaira-ingest/internal/mcp"
	"github.com/danieljustus/symaira-ingest/internal/ocr"
	"github.com/danieljustus/symaira-ingest/internal/store"
	"github.com/danieljustus/symaira-ingest/internal/version"
)

func runMCP(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	ocrLang, vault, archive, db := registerSharedFlags(fs)
	configureUsage(fs, "mcp [flags]", "Start the MCP server for AI-powered document processing.")
	help, err := parseFlags(fs, args, "invalid mcp flags")
	if help || err != nil {
		return err
	}
	cfg, err := resolveConfig(fs, ocrLang, vault, archive, db)
	if err != nil {
		return err
	}

	st, err := store.Open(cfg.db)
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindConfig,
			"failed to open document store")
	}
	defer st.Close()

	server := newMCPServer(st, cfg.ocrLang, cfg.vault, cfg.archive)

	ctx := context.Background()
	if err := server.ServeStdio(ctx); err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitSoftware, exitcodes.KindInternal,
			"mcp server failed")
	}
	return nil
}

func newMCPServer(st *store.Store, ocrLang, vault, archive string) *mcpserver.Server {
	engine := ocr.DefaultRunner(ocrLang)
	server := mcpserver.New("symingest", version.Version)
	mcp.Register(server, st, engine, vault, archive)
	return server
}
