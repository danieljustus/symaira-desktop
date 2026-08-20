package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-ingest/internal/store"
)

func TestRunMCP_StoreOpenFailure(t *testing.T) {
	// Pass a directory as the database path to cause store.Open to fail.
	dbDir := t.TempDir()

	err := runMCP([]string{"-db", dbDir})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to open document store") {
		t.Errorf("expected error message to contain 'failed to open document store', got: %v", err)
	}
}

func TestMCPServer_ServeIO(t *testing.T) {
	// Setup db and store
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer st.Close()

	// Create new server using our helper
	server := newMCPServer(st, "eng", t.TempDir(), t.TempDir())

	// Create client/server connection pair
	c1, c2 := net.Pipe()
	defer c1.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Serve the MCP server on c1
	errChan := make(chan error, 1)
	go func() {
		errChan <- server.ServeIO(ctx, c1, c1)
	}()

	// Send initialize request on c2 (using line-mode JSON-RPC)
	initReq := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test-client","version":"1.0.0"}}}` + "\n"

	_, err = c2.Write([]byte(initReq))
	if err != nil {
		t.Fatalf("failed to write initialize request: %v", err)
	}

	// Read initialize response
	reader := bufio.NewReader(c2)
	responseLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("failed to read initialize response: %v", err)
	}

	var initResp struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Result  struct {
			ServerInfo struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(responseLine), &initResp); err != nil {
		t.Fatalf("failed to unmarshal initialize response: %v\nLine: %s", err, responseLine)
	}

	if initResp.Result.ServerInfo.Name != "symingest" {
		t.Errorf("expected server name 'symingest', got %q", initResp.Result.ServerInfo.Name)
	}

	// Send list tools request
	listReq := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n"
	_, err = c2.Write([]byte(listReq))
	if err != nil {
		t.Fatalf("failed to write tools/list request: %v", err)
	}

	// Read list tools response
	responseLine, err = reader.ReadString('\n')
	if err != nil {
		t.Fatalf("failed to read tools/list response: %v", err)
	}

	var listResp struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Result  struct {
			Tools []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(responseLine), &listResp); err != nil {
		t.Fatalf("failed to unmarshal tools/list response: %v\nLine: %s", err, responseLine)
	}

	if len(listResp.Result.Tools) == 0 {
		t.Error("expected registered tools, got 0")
	}

	// Shutdown server by closing connection
	c2.Close()
	select {
	case serveErr := <-errChan:
		if serveErr != nil {
			t.Errorf("ServeIO returned error: %v", serveErr)
		}
	case <-time.After(time.Second):
		t.Error("timed out waiting for server to shutdown")
	}
}
