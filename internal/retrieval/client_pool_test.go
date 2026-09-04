package retrieval

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func prepareClientPoolTest(t testing.TB) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	return home
}

func countedPool(t testing.TB) (*ClientPool, *int32) {
	t.Helper()
	var opens int32
	pool := newClientPoolWithOpener(func(vaultRoot string) (*Client, error) {
		atomic.AddInt32(&opens, 1)
		return OpenForVault(vaultRoot)
	})
	t.Cleanup(func() { _ = pool.Close() })
	return pool, &opens
}

func acquirePoolClient(t testing.TB, pool *ClientPool, vaultRoot string) (*Client, func()) {
	t.Helper()
	client, release, err := pool.Acquire(vaultRoot)
	if err != nil {
		t.Fatalf("Acquire(%q): %v", vaultRoot, err)
	}
	return client, release
}

func TestClientPoolRejectsBlankVault(t *testing.T) {
	pool := NewClientPool()
	defer func() { _ = pool.Close() }()
	for _, vaultRoot := range []string{"", " "} {
		_, release, err := pool.Acquire(vaultRoot)
		release()
		if err == nil {
			t.Errorf("Acquire(%q) succeeded, want vault validation error", vaultRoot)
		}
	}
}

func TestClientPoolReusesCanonicalVaultClient(t *testing.T) {
	prepareClientPoolTest(t)
	pool, opens := countedPool(t)
	vaultRoot := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(vaultRoot, alias); err != nil {
		t.Fatal(err)
	}
	first, releaseFirst := acquirePoolClient(t, pool, vaultRoot)
	second, releaseSecond := acquirePoolClient(t, pool, alias)
	releaseFirst()
	releaseSecond()
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
	first, releaseFirst := acquirePoolClient(t, pool, t.TempDir())
	second, releaseSecond := acquirePoolClient(t, pool, t.TempDir())
	releaseFirst()
	releaseSecond()
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
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("index_path = \""+shared+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pool, opens := countedPool(t)
	first, releaseFirst := acquirePoolClient(t, pool, t.TempDir())
	second, releaseSecond := acquirePoolClient(t, pool, t.TempDir())
	releaseFirst()
	releaseSecond()
	if first != second {
		t.Fatal("explicit shared index opened duplicate clients")
	}
	if got := atomic.LoadInt32(opens); got != 1 {
		t.Fatalf("open count = %d, want 1", got)
	}
}

func TestClientPoolConcurrentAcquireOpensOnce(t *testing.T) {
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
			client, release, err := pool.Acquire(vaultRoot)
			if err != nil {
				errs <- err
				return
			}
			clients[i] = client
			release()
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	for i := 1; i < callers; i++ {
		if clients[i] != clients[0] {
			t.Fatal("concurrent Acquire calls returned different clients")
		}
	}
	if got := atomic.LoadInt32(opens); got != 1 {
		t.Fatalf("open count = %d, want 1", got)
	}
}

func TestClientPoolKeysOpenedClientByActualPath(t *testing.T) {
	home := prepareClientPoolTest(t)
	configPath := filepath.Join(home, ".config", "symseek", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	requested := filepath.Join(t.TempDir(), "requested.db")
	actual := filepath.Join(t.TempDir(), "actual.db")
	if err := os.WriteFile(configPath, []byte("index_path = \""+requested+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var opens int32
	pool := newClientPoolWithOpener(func(vaultRoot string) (*Client, error) {
		atomic.AddInt32(&opens, 1)
		if err := os.WriteFile(configPath, []byte("index_path = \""+actual+"\"\n"), 0o600); err != nil {
			return nil, err
		}
		return OpenForVault(vaultRoot)
	})
	defer func() { _ = pool.Close() }()
	first, releaseFirst := acquirePoolClient(t, pool, t.TempDir())
	releaseFirst()
	second, releaseSecond := acquirePoolClient(t, pool, t.TempDir())
	releaseSecond()
	if first != second {
		t.Fatal("actual opened index path was not reused after config change")
	}
	if got := atomic.LoadInt32(&opens); got != 1 {
		t.Fatalf("open count = %d, want 1", got)
	}
}

func TestClientPoolInvalidateRetiresUntilLastRelease(t *testing.T) {
	prepareClientPoolTest(t)
	pool, opens := countedPool(t)
	vaultRoot := t.TempDir()
	first, releaseFirst := acquirePoolClient(t, pool, vaultRoot)
	pool.Invalidate(first)
	if err := first.Delete("missing"); err != nil {
		t.Fatalf("invalidated client closed while lease active: %v", err)
	}
	second, releaseSecond := acquirePoolClient(t, pool, vaultRoot)
	if first == second {
		t.Fatal("Invalidate did not create a replacement")
	}
	if got := atomic.LoadInt32(opens); got != 2 {
		t.Fatalf("open count = %d, want 2", got)
	}
	releaseFirst()
	if err := first.Delete("missing"); err == nil {
		t.Fatal("retired client remained open after last lease release")
	}
	pool.Invalidate(first)
	third, releaseThird := acquirePoolClient(t, pool, vaultRoot)
	if third != second {
		t.Fatal("stale client invalidation removed the replacement")
	}
	releaseThird()
	releaseSecond()
}

func TestClientPoolRepeatedInvalidationDoesNotAccumulateRetiredHandles(t *testing.T) {
	prepareClientPoolTest(t)
	pool, _ := countedPool(t)
	vaultRoot := t.TempDir()
	for i := 0; i < 8; i++ {
		client, release := acquirePoolClient(t, pool, vaultRoot)
		pool.Invalidate(client)
		release()
		pool.mu.Lock()
		retired, closing := len(pool.retired), pool.closing
		pool.mu.Unlock()
		if retired != 0 || closing != 0 {
			t.Fatalf("iteration %d left retired=%d closing=%d", i, retired, closing)
		}
	}
}

func TestClientPoolCloseWaitsForLease(t *testing.T) {
	prepareClientPoolTest(t)
	pool, _ := countedPool(t)
	vaultRoot := t.TempDir()
	client, release := acquirePoolClient(t, pool, vaultRoot)
	done := make(chan error, 1)
	go func() { done <- pool.Close() }()
	select {
	case err := <-done:
		t.Fatalf("Close returned before lease release: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	release()
	if err := <-done; err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := client.Delete("missing"); err == nil {
		t.Fatal("client remained open after lease-aware pool close")
	}
	_, noop, err := pool.Acquire(vaultRoot)
	noop()
	if !errors.Is(err, errClientPoolClosed) {
		t.Fatalf("Acquire after Close error = %v, want pool closed", err)
	}
}

func BenchmarkClientPoolAcquisition(b *testing.B) {
	prepareClientPoolTest(b)
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
	b.Run("pooled_warm_acquire", func(b *testing.B) {
		pool := NewClientPool()
		defer func() { _ = pool.Close() }()
		client, release, err := pool.Acquire(vaultRoot)
		if err != nil || client == nil {
			b.Fatalf("initial Acquire: client=%v err=%v", client, err)
		}
		release()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, release, err := pool.Acquire(vaultRoot)
			if err != nil {
				b.Fatal(err)
			}
			release()
		}
	})
}
