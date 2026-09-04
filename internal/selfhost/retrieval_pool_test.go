package selfhost

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSelfhostReusesRetrievalPoolAndClosesIt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	server, err := NewServer(ServerConfig{
		VaultRoot:  t.TempDir(),
		Token:      testToken,
		Version:    "test",
		Executable: "/bin/false",
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	requestNotebooks := func() {
		t.Helper()
		request, err := http.NewRequest(http.MethodGet, httpServer.URL+"/api/v1/notebooks", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+testToken)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("notebooks request returned %d", response.StatusCode)
		}
	}

	requestNotebooks()
	firstClient, releaseFirst, err := server.retrievalPool.Acquire(server.cfg.VaultRoot)
	if err != nil {
		t.Fatalf("first pool inspection: %v", err)
	}
	releaseFirst()
	requestNotebooks()
	secondClient, releaseSecond, err := server.retrievalPool.Acquire(server.cfg.VaultRoot)
	if err != nil {
		t.Fatalf("second pool inspection: %v", err)
	}
	releaseSecond()
	if firstClient != secondClient {
		t.Fatal("repeated self-host requests did not reuse retrieval client")
	}

	if err := server.retrievalPool.Close(); err != nil {
		t.Fatalf("pool Close: %v", err)
	}
	// Handler construction remains lazy: endpoints that do not need retrieval
	// still work when retrieval is unavailable.
	requestNotebooks()
	if err := server.Close(); err != nil {
		t.Fatalf("server Close: %v", err)
	}
	if err := firstClient.Delete("missing"); err == nil {
		t.Fatal("server shutdown did not close pooled retrieval client")
	}
}
