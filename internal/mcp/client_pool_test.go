package mcp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/retrieval"
	"github.com/danieljustus/symaira-desktop/internal/service"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
)

func TestSearchToolReusesServerOwnedRetrievalPool(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	vaultRoot := t.TempDir()
	pool := retrieval.NewClientPool()
	defer func() { _ = pool.Close() }()
	var clients []*retrieval.Client
	factory := func() (*service.Service, *sidecar.DB, error) {
		db, err := sidecar.Open(filepath.Join(vaultRoot, "sidecar.db"))
		if err != nil {
			return nil, nil, err
		}
		client, err := pool.Get(vaultRoot)
		if err != nil {
			_ = db.Close()
			return nil, nil, err
		}
		clients = append(clients, client)
		return service.NewWithRetrievalClient(vaultRoot, db, client), db, nil
	}

	tool := newSearchTool(factory)
	input := json.RawMessage(`{"query":"reused pool search"}`)
	for i := 0; i < 2; i++ {
		if _, err := tool.Handler(context.Background(), input); err != nil {
			t.Fatalf("search %d: %v", i, err)
		}
	}
	if len(clients) != 2 || clients[0] != clients[1] {
		t.Fatal("separately-created services did not share one retrieval client")
	}

	if err := pool.Close(); err != nil {
		t.Fatalf("pool Close: %v", err)
	}
	if err := clients[0].Delete("missing"); err == nil {
		t.Fatal("MCP pool shutdown did not close the retrieval client")
	}
}
