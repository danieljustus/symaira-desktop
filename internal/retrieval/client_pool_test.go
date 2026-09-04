package retrieval

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func prepareClientPoolTest(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	return home
}

func countedPool(t *testing.T) (*ClientPool, *int32) {
	t.Helper()
	var opens int32
	pool := newClientPoolWithOpener(func(vaultRoot string) (*Client, error) {
		atomic.AddInt32(&opens, 1)
		return OpenForVault(vaultRoot)
	})
	t.Cleanup(func() { _ = pool.Close() })
	return pool, &opens
}

func TestClientPoolReusesCanonicalVaultClient(t *testing.T) {
	prepareClientPoolTest(t)
	pool, opens := countedPool(t)
	vaultRoot := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(vaultRoot, alias); err != nil {
		t.Fatal(err)
	}

	first, err := pool.Get(vaultRoot)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	second, err := pool.Get(alias)
	if err != nil {
		t.Fatalf("symlink Get: %v", err)
	}
	if first != second {
		t.Fatal("canonical and symlinked vaults opened different clients")
	}
	if got := atomic.LoadInt32(opens); got != 1 {
		t.Fatalf("open count = %d, want 1", got)
	}
}

func TestClientPoolSeparatesVaultIndexes(t *testing.T) {
	prepareClientPoolTest(t)
	pool, opens := countedPool(t)
	first, err := pool.Get(t.TempDir())
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	second, err := pool.Get(t.TempDir())
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if first == second {
		t.Fatal("different vaults shared a retrieval client")
	}
	if got := atomic.LoadInt32(opens); got != 2 {
		t.Fatalf("open count = %d, want 2", got)
	}
}

func TestClientPoolCollapsesExplicitSharedIndex(t *testing.T) {
	home := prepareClientPoolTest(t)
	shared := filepath.Join(t.TempDir(), "shared.db")
	configPath := filepath.Join(home, ".config", "symseek", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("index_path = \""+shared+"\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	pool, opens := countedPool(t)
	first, err := pool.Get(t.TempDir())
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	second, err := pool.Get(t.TempDir())
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if first != second {
		t.Fatal("explicit shared index opened duplicate clients")
	}
	if got := atomic.LoadInt32(opens); got != 1 {
		t.Fatalf("open count = %d, want 1", got)
	}
}

func TestClientPoolConcurrentGetOpensOnce(t *testing.T) {
	prepareClientPoolTest(t)
	pool, opens := countedPool(t)
	vaultRoot := t.TempDir()
	const callers = 16
	clients := make([]*Client, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			clients[i], _ = pool.Get(vaultRoot)
			if clients[i] == nil {
				errs <- errors.New("Get returned nil client")
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	for i := 1; i < callers; i++ {
		if clients[i] != clients[0] {
			t.Fatal("concurrent Get calls returned different clients")
		}
	}
	if got := atomic.LoadInt32(opens); got != 1 {
		t.Fatalf("open count = %d, want 1", got)
	}
}

func BenchmarkClientPoolAcquisition(b *testing.B) {
	home := b.TempDir()
	b.Setenv("HOME", home)
	b.Setenv("XDG_DATA_HOME", b.TempDir())
	vaultRoot := b.TempDir()

	seed, err := OpenForVault(vaultRoot)
	if err != nil {
		b.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		b.Fatal(err)
	}

	b.Run("per_request_open_close", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			client, err := OpenForVault(vaultRoot)
			if err != nil {
				b.Fatal(err)
			}
			if err := client.Close(); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("pooled_warm_get", func(b *testing.B) {
		pool := NewClientPool()
		defer func() { _ = pool.Close() }()
		if _, err := pool.Get(vaultRoot); err != nil {
			b.Fatal(err)
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := pool.Get(vaultRoot); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func TestClientPoolInvalidateRetiresAndReopens(t *testing.T) {
	prepareClientPoolTest(t)
	pool, opens := countedPool(t)
	vaultRoot := t.TempDir()
	first, err := pool.Get(vaultRoot)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	pool.Invalidate(vaultRoot, first)
	second, err := pool.Get(vaultRoot)
	if err != nil {
		t.Fatalf("replacement Get: %v", err)
	}
	if first == second {
		t.Fatal("Invalidate did not remove the active client")
	}
	if got := atomic.LoadInt32(opens); got != 2 {
		t.Fatalf("open count = %d, want 2", got)
	}
	if err := pool.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := pool.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := pool.Get(vaultRoot); !errors.Is(err, errClientPoolClosed) {
		t.Fatalf("Get after Close error = %v, want pool closed", err)
	}
	if err := first.Delete("missing"); err == nil {
		t.Fatal("retired client remained usable after pool shutdown")
	}
	if err := second.Delete("missing"); err == nil {
		t.Fatal("active client remained usable after pool shutdown")
	}
}
