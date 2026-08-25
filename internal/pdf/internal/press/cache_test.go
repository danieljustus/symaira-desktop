package press

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// withUserCacheDir redirects the injectable user-cache lookup to dir for the
// duration of the test. userCacheDirFunc is test-only; production code must
// not override it.
func withUserCacheDir(t *testing.T, dir string) {
	t.Helper()
	orig := userCacheDirFunc
	userCacheDirFunc = func() (string, error) { return dir, nil }
	t.Cleanup(func() { userCacheDirFunc = orig })
}

// persistentCacheDirForTest returns the exact versioned cache directory the
// implementation derives for the current embedded assets (mirrors production
// naming so the tests can plant stale directories at the real path).
func persistentCacheDirForTest(t *testing.T) string {
	t.Helper()
	dir, err := persistentAssetsCacheDir()
	if err != nil {
		t.Fatalf("persistentAssetsCacheDir: %v", err)
	}
	return dir
}

// cacheRootForTest returns the cache root that hosts the symprint cache tree
// (the parent of the symprint/ directory).
func cacheRootForTest(t *testing.T) string {
	t.Helper()
	return filepath.Dir(filepath.Dir(persistentCacheDirForTest(t)))
}

func TestAssetsCacheReuseAndPersistence(t *testing.T) {
	withUserCacheDir(t, t.TempDir())
	ResetAssetsCache()
	defer ResetAssetsCache()

	// First initialization materializes the persistent versioned cache.
	dir1, err := getOrInitializeAssetsCache()
	if err != nil {
		t.Fatalf("failed to initialize assets cache: %v", err)
	}
	if dir1 == "" {
		t.Fatal("expected non-empty cache directory path")
	}
	for _, sub := range []string{"templates", "fonts", "packages"} {
		if _, err := os.Stat(filepath.Join(dir1, sub)); err != nil {
			t.Errorf("%s dir missing in cache: %v", sub, err)
		}
	}
	if !assetsCacheComplete(dir1) {
		t.Error("expected completion marker after initialization")
	}

	// A second call in the same process returns the same directory.
	dir2, err := getOrInitializeAssetsCache()
	if err != nil {
		t.Fatalf("failed to get assets cache second time: %v", err)
	}
	if dir2 != dir1 {
		t.Errorf("expected same cache directory, got %q and %q", dir1, dir2)
	}

	// Simulate a fresh process: the persistent initializer is stateless, so a
	// new call must reuse the on-disk cache instead of re-materializing.
	// A sentinel file planted after the first materialization survives only
	// if the directory is reused as-is.
	sentinel := filepath.Join(dir1, "templates", "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("persisted"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir3, err := initializePersistentAssetsCache()
	if err != nil {
		t.Fatalf("persistent re-init failed: %v", err)
	}
	if dir3 != dir1 {
		t.Errorf("fresh-process init returned %q, want %q", dir3, dir1)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Errorf("cache content did not persist across calls: %v", err)
	}
}

func TestAssetsCacheVersioningAndStaleReplacement(t *testing.T) {
	withUserCacheDir(t, t.TempDir())
	ResetAssetsCache()
	defer ResetAssetsCache()

	realDir := persistentCacheDirForTest(t)

	// A stale/partial directory at the real versioned path (no completion
	// marker) must not be reused: it is replaced by a complete one.
	staleFile := filepath.Join(realDir, "templates", "stale.typ")
	writeTestFile(t, staleFile, []byte("stale"))
	// Backdate it so it is not mistaken for a fresh concurrent initializer.
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(realDir, old, old); err != nil {
		t.Fatal(err)
	}

	dir, err := getOrInitializeAssetsCache()
	if err != nil {
		t.Fatalf("failed to initialize assets cache: %v", err)
	}
	if dir != realDir {
		t.Errorf("expected versioned cache dir %q, got %q", realDir, dir)
	}
	if !assetsCacheComplete(realDir) {
		t.Error("expected completion marker after replacing stale dir")
	}
	if _, err := os.Stat(staleFile); !os.IsNotExist(err) {
		t.Errorf("stale content must be replaced, stat err: %v", err)
	}
	for _, sub := range []string{"templates", "fonts", "packages"} {
		if _, err := os.Stat(filepath.Join(realDir, sub)); err != nil {
			t.Errorf("%s dir missing after replacement: %v", sub, err)
		}
	}

	// A complete directory under a *different* version key is a different
	// cache generation: it must be left untouched.
	otherDir := filepath.Join(filepath.Dir(realDir), "assets-v1-deadbeefcafef00d")
	writeTestFile(t, filepath.Join(otherDir, "templates", "other.typ"), []byte("other"))
	if err := os.WriteFile(filepath.Join(otherDir, assetsCompleteMarker), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if otherDir == realDir {
		t.Fatal("test setup broken: version keys did not differ")
	}
	if _, err := initializePersistentAssetsCache(); err != nil {
		t.Fatalf("persistent re-init failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(otherDir, "templates", "other.typ")); err != nil {
		t.Errorf("other version dir must be untouched: %v", err)
	}
}

func TestAssetsCacheConcurrentInit(t *testing.T) {
	withUserCacheDir(t, t.TempDir())
	ResetAssetsCache()
	defer ResetAssetsCache()

	const n = 16
	// initializePersistentAssetsCache is the stateless per-call path, so
	// running it concurrently exercises the real materialize-and-rename race
	// that multiple processes would hit (the in-process mutex in
	// getOrInitializeAssetsCache would serialize these goroutines instead).
	dirs := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			dirs[i], errs[i] = initializePersistentAssetsCache()
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d failed: %v", i, errs[i])
		}
		if dirs[i] != dirs[0] {
			t.Errorf("goroutine %d got %q, want %q", i, dirs[i], dirs[0])
		}
	}
	if !assetsCacheComplete(dirs[0]) {
		t.Error("expected completion marker on the winning directory")
	}
	for _, sub := range []string{"templates", "fonts", "packages"} {
		if _, err := os.Stat(filepath.Join(dirs[0], sub)); err != nil {
			t.Errorf("%s dir missing after concurrent init: %v", sub, err)
		}
	}

	// No in-progress temp directories may remain behind.
	entries, err := os.ReadDir(cacheRootForTest(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if len(e.Name()) >= len(assetsCacheTempPrefix) && e.Name()[:len(assetsCacheTempPrefix)] == assetsCacheTempPrefix {
			t.Errorf("leftover temp dir after concurrent init: %s", e.Name())
		}
	}
}

func TestAssetsCacheFallbackWhenUserCacheUnavailable(t *testing.T) {
	// Force the user-cache lookup to fail.
	orig := userCacheDirFunc
	userCacheDirFunc = func() (string, error) { return "", os.ErrNotExist }
	t.Cleanup(func() { userCacheDirFunc = orig })
	ResetAssetsCache()
	defer ResetAssetsCache()

	dir, err := getOrInitializeAssetsCache()
	if err != nil {
		t.Fatalf("fallback init must not fail: %v", err)
	}
	if filepath.Dir(dir) != filepath.Clean(os.TempDir()) {
		t.Errorf("expected fallback dir under %s, got %q (parent %q)", os.TempDir(), dir, filepath.Dir(dir))
	}
	for _, sub := range []string{"templates", "fonts", "packages"} {
		if _, err := os.Stat(filepath.Join(dir, sub)); err != nil {
			t.Errorf("%s dir missing in fallback cache: %v", sub, err)
		}
	}
	if assetsCacheComplete(dir) {
		t.Error("fallback temp dir must not carry a completion marker")
	}

	// The fallback is cached for the process, like the old behavior.
	dir2, err := getOrInitializeAssetsCache()
	if err != nil {
		t.Fatalf("second fallback call failed: %v", err)
	}
	if dir2 != dir {
		t.Errorf("expected cached fallback dir, got %q and %q", dir, dir2)
	}

	// Rendering must still work with the fallback cache.
	binDir := t.TempDir()
	captureArgsTypst(t, binDir)
	eng := EngineInfo{Available: true, Path: filepath.Join(binDir, "typst"), Version: "0.15.0"}
	if _, err := renderTypst(context.Background(), eng, baseTestJob(t.TempDir())); err != nil {
		t.Fatalf("render with fallback cache failed: %v", err)
	}
}

func TestAssetsCacheFallbackWhenCacheRootUnusable(t *testing.T) {
	// The cache root is blocked by a regular file, so MkdirAll fails.
	blocker := filepath.Join(t.TempDir(), "blocker")
	writeTestFile(t, blocker, []byte("not a directory"))
	withUserCacheDir(t, blocker)
	ResetAssetsCache()
	defer ResetAssetsCache()

	dir, err := getOrInitializeAssetsCache()
	if err != nil {
		t.Fatalf("fallback init must not fail: %v", err)
	}
	if filepath.Dir(dir) != filepath.Clean(os.TempDir()) {
		t.Errorf("expected fallback dir under %s, got %q (parent %q)", os.TempDir(), dir, filepath.Dir(dir))
	}
	if _, err := os.Stat(filepath.Join(dir, "templates")); err != nil {
		t.Errorf("templates dir missing in fallback cache: %v", err)
	}
}

func TestResetAssetsCacheRemovesPersistentDir(t *testing.T) {
	withUserCacheDir(t, t.TempDir())
	ResetAssetsCache()
	defer ResetAssetsCache()

	dir, err := getOrInitializeAssetsCache()
	if err != nil {
		t.Fatalf("failed to initialize assets cache: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, assetsCompleteMarker)); err != nil {
		t.Fatalf("expected materialized cache: %v", err)
	}

	// Reset must drop process state AND the on-disk versioned directory.
	ResetAssetsCache()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("versioned dir must be removed by ResetAssetsCache, stat err: %v", err)
	}

	// Safe to call again without any prior initialization.
	ResetAssetsCache()

	// The next initialization re-materializes the same versioned path.
	dir2, err := getOrInitializeAssetsCache()
	if err != nil {
		t.Fatalf("re-init after reset failed: %v", err)
	}
	if dir2 != dir {
		t.Errorf("expected same versioned dir after reset, got %q and %q", dir, dir2)
	}
	if !assetsCacheComplete(dir2) {
		t.Error("expected completion marker after re-init")
	}
}

// blockPathWithFile plants a regular file at path so any os.MkdirAll or
// os.WriteFile targeting it (or a child of it) fails deterministically,
// regardless of the effective user or filesystem permissions.
func blockPathWithFile(t *testing.T, path string) {
	t.Helper()
	writeTestFile(t, path, []byte("blocker"))
}

func TestMaterializeAssetsFailsWhenTemplatesPathBlocked(t *testing.T) {
	// A regular file at <dir>/templates makes os.MkdirAll fail inside
	// assets.Materialize, exercising the first error branch of
	// materializeAssets.
	dir := t.TempDir()
	blockPathWithFile(t, filepath.Join(dir, "templates"))

	if err := materializeAssets(dir); err == nil {
		t.Fatal("expected materializeAssets to fail when the templates path is blocked")
	}
}

func TestMaterializeAssetsFailsWhenFontsPathBlocked(t *testing.T) {
	// Templates materialize fine; a regular file at <dir>/fonts makes
	// assets.MaterializeFonts fail, exercising the second error branch.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	blockPathWithFile(t, filepath.Join(dir, "fonts"))

	if err := materializeAssets(dir); err == nil {
		t.Fatal("expected materializeAssets to fail when the fonts path is blocked")
	}
}

func TestMaterializeAssetsFailsWhenPackagesPathBlocked(t *testing.T) {
	// Templates and fonts materialize fine; a regular file at <dir>/packages
	// makes assets.MaterializePackages fail, exercising the third error
	// branch.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "fonts"), 0o755); err != nil {
		t.Fatal(err)
	}
	blockPathWithFile(t, filepath.Join(dir, "packages"))

	if err := materializeAssets(dir); err == nil {
		t.Fatal("expected materializeAssets to fail when the packages path is blocked")
	}
}

func TestAssetsCacheFailureIsCached(t *testing.T) {
	// Both the persistent path (user cache lookup fails) and the per-process
	// temp fallback (TMPDIR points nowhere) must fail, so the error is
	// recorded and reused by later calls instead of being retried.
	origUserCache := userCacheDirFunc
	userCacheDirFunc = func() (string, error) { return "", os.ErrNotExist }
	t.Cleanup(func() { userCacheDirFunc = origUserCache })

	// Create the temp dir before breaking TMPDIR: t.TempDir itself uses
	// os.MkdirTemp and would fail afterwards.
	missingTmp := filepath.Join(t.TempDir(), "does-not-exist")
	origTmp, hadTmp := os.LookupEnv("TMPDIR")
	os.Setenv("TMPDIR", missingTmp)
	t.Cleanup(func() {
		if hadTmp {
			os.Setenv("TMPDIR", origTmp)
		} else {
			os.Unsetenv("TMPDIR")
		}
	})

	ResetAssetsCache()
	defer ResetAssetsCache()

	if _, err := getOrInitializeAssetsCache(); err == nil {
		t.Fatal("expected an error when both the persistent cache and the temp fallback fail")
	}

	// The failure is cached: a second call must return the same error
	// without retrying either path.
	if _, err := getOrInitializeAssetsCache(); err == nil {
		t.Fatal("expected the cached error to be returned on subsequent calls")
	}
}
