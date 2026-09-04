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

	first, err := server.retrievalPool.Get(server.cfg.VaultRoot)
	if err != nil {
		t.Fatalf("initial pool Get: %v", err)
	}
	for i := 0; i < 2; i++ {
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
			t.Fatalf("notebooks request %d returned %d", i, response.StatusCode)
		}
	}
	second, err := server.retrievalPool.Get(server.cfg.VaultRoot)
	if err != nil {
		t.Fatalf("second pool Get: %v", err)
	}
	if first != second {
		t.Fatal("repeated self-host requests did not reuse retrieval client")
	}

	if err := server.Close(); err != nil {
		t.Fatalf("server Close: %v", err)
	}
	if _, err := server.retrievalPool.Get(server.cfg.VaultRoot); err == nil {
		t.Fatal("server shutdown left retrieval pool open")
	}
	if err := first.Delete("missing"); err == nil {
		t.Fatal("server shutdown did not close pooled retrieval client")
	}
}
