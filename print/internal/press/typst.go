package press

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/danieljustus/symaira-print/internal/assets"
)

// cmarkerVersion pins the vendored @preview/cmarker package that parses
// CommonMark inside the Typst binary. The package files are embedded in the
// binary (internal/assets/packages) and materialized per render, so typst
// resolves the import locally without network access.
const (
	cmarkerVersion = "0.1.9"
	mitexVersion   = "0.2.7"
)

// assetsCacheLayoutVersion is bumped whenever the on-disk layout or semantics
// of the persistent assets cache change, so directories written by an older
// layout are ignored instead of misread. It is part of the cache directory
// name, next to the content hash of the embedded assets.
const assetsCacheLayoutVersion = "v1"

// assetsCompleteMarker is written inside a fully materialized cache directory
// before it is atomically renamed into place. A versioned directory without
// this marker is never treated as valid: it is either a partial materialization
// or a stale leftover, and it is replaced.
const assetsCompleteMarker = ".complete"

// assetsCacheTempPrefix names in-progress materialization directories inside
// the cache root. Leftovers from crashed processes are swept after a
// successful materialization; the prefix keeps the sweep from ever touching
// foreign directories.
const assetsCacheTempPrefix = ".symprint-assets-tmp-"

// staleAssetsCacheAge is how old a markerless directory (or a leftover temp
// directory) must be before it is considered abandoned and safe to delete.
// Fresh directories are left alone: another process may still be working on
// them.
const staleAssetsCacheAge = time.Hour

// userCacheDirFunc returns the user cache directory that hosts the persistent
// assets cache. It is a variable so tests can redirect the cache into a temp
// directory or force a lookup failure to exercise the fallback path. It is
// test-only: production code must not override it.
var userCacheDirFunc = os.UserCacheDir

var (
	assetsCacheMu  sync.Mutex
	assetsCacheDir string
	assetsCacheErr error
)

// ResetAssetsCache removes the process's cached assets directory and the
// persistent versioned cache directory on disk, then resets cache state so the
// next render re-materializes the embedded assets from scratch. It is intended
// for testing and is safe to call at any time (including before any
// initialization).
func ResetAssetsCache() {
	assetsCacheMu.Lock()
	defer assetsCacheMu.Unlock()
	if assetsCacheDir != "" {
		os.RemoveAll(assetsCacheDir)
		assetsCacheDir = ""
	}
	assetsCacheErr = nil
	if dir, err := persistentAssetsCacheDir(); err == nil {
		os.RemoveAll(dir)
	}
}

// persistentAssetsCacheDir returns the versioned cache directory for the
// currently embedded assets, e.g.
// <UserCacheDir>/symprint/assets-v1-<sha256 of all embedded trees>. The name
// changes automatically whenever templates, fonts or packages change (see
// assets.VersionKey), so a stale cache generation is never reused. An error
// means the user cache is unavailable and callers must fall back.
func persistentAssetsCacheDir() (string, error) {
	cacheRoot, err := userCacheDirFunc()
	if err != nil {
		return "", err
	}
	key, err := assets.VersionKey()
	if err != nil {
		return "", err
	}
	return filepath.Join(cacheRoot, "symprint", "assets-"+assetsCacheLayoutVersion+"-"+key), nil
}

// assetsCacheComplete reports whether dir is a fully materialized assets cache,
// i.e. its completion marker exists. Anything else — missing directory, partial
// materialization, stale leftover — is treated as invalid.
func assetsCacheComplete(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, assetsCompleteMarker))
	return err == nil && fi.Mode().IsRegular()
}

// materializeAssets writes the embedded templates, fonts and packages into dir,
// mirroring the layout renderTypst symlinks into the per-render work dir.
func materializeAssets(dir string) error {
	if err := assets.Materialize(filepath.Join(dir, "templates")); err != nil {
		return err
	}
	if _, err := assets.MaterializeFonts(filepath.Join(dir, "fonts")); err != nil {
		return err
	}
	return assets.MaterializePackages(dir)
}

// sweepStaleAssetsTemps removes abandoned in-progress materialization
// directories (crash leftovers) from the cache root. It is best-effort and
// only ever touches directories that are clearly older than any live
// materialization could be.
func sweepStaleAssetsTemps(cacheRoot string) {
	entries, err := os.ReadDir(cacheRoot)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-staleAssetsCacheAge)
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), assetsCacheTempPrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			os.RemoveAll(filepath.Join(cacheRoot, e.Name()))
		}
	}
}

// initializePersistentAssetsCache materializes the embedded assets into the
// versioned cache directory under the user cache dir. Content is first written
// into a fresh temp directory inside the cache root; only after every file and
// the completion marker are in place is the directory atomically renamed into
// its final name, so a versioned directory is either absent or fully complete.
// When a concurrent process wins the rename race, the loser detects the
// winner's completion marker and reuses it. Any failure returns an error so
// getOrInitializeAssetsCache can fall back to a per-process temp cache.
func initializePersistentAssetsCache() (string, error) {
	versionedDir, err := persistentAssetsCacheDir()
	if err != nil {
		return "", err
	}
	if assetsCacheComplete(versionedDir) {
		return versionedDir, nil
	}

	cacheRoot := filepath.Dir(filepath.Dir(versionedDir))
	// The rename target's parent (…/symprint) must exist before os.Rename.
	if err := os.MkdirAll(filepath.Dir(versionedDir), 0o755); err != nil {
		return "", err
	}

	// The loop handles a stale markerless directory blocking the rename. It is
	// bounded so a pathological filesystem cannot hang initialization;
	// exhausting it falls back to the per-process temp cache.
	for attempt := 0; attempt < 3; attempt++ {
		if assetsCacheComplete(versionedDir) {
			// A concurrent process finished first; reuse its directory.
			return versionedDir, nil
		}

		tmpDir, err := os.MkdirTemp(cacheRoot, assetsCacheTempPrefix+"*")
		if err != nil {
			return "", err
		}
		if err := materializeAssets(tmpDir); err != nil {
			os.RemoveAll(tmpDir)
			return "", err
		}
		if err := os.WriteFile(filepath.Join(tmpDir, assetsCompleteMarker), []byte(versionedDir), 0o644); err != nil {
			os.RemoveAll(tmpDir)
			return "", err
		}

		renameErr := os.Rename(tmpDir, versionedDir)
		if renameErr == nil {
			sweepStaleAssetsTemps(cacheRoot)
			return versionedDir, nil
		}
		os.RemoveAll(tmpDir)

		if assetsCacheComplete(versionedDir) {
			// Lost the rename race: the winner's directory is complete.
			return versionedDir, nil
		}
		info, statErr := os.Stat(versionedDir)
		if statErr != nil {
			// The rename failed for an unrelated reason (e.g. the cache root
			// became unwritable): fall back to the per-process temp cache.
			return "", renameErr
		}
		if time.Since(info.ModTime()) < staleAssetsCacheAge {
			// A fresh markerless directory may belong to a concurrent
			// initializer with a different protocol: leave it alone and fall
			// back so rendering still works.
			return "", fmt.Errorf("assets cache dir %s exists without completion marker", versionedDir)
		}
		// An old markerless directory is a stale/partial leftover: drop it
		// and retry with a fresh materialization.
		os.RemoveAll(versionedDir)
	}
	return "", fmt.Errorf("could not initialize assets cache at %s", versionedDir)
}

// getOrInitializeAssetsCache returns the process-wide assets cache directory,
// materializing persistent assets when possible and using a per-process temp
// directory when the user cache is unavailable.
func getOrInitializeAssetsCache() (string, error) {
	assetsCacheMu.Lock()
	defer assetsCacheMu.Unlock()
	if assetsCacheDir != "" {
		return assetsCacheDir, nil
	}
	if assetsCacheErr != nil {
		return "", assetsCacheErr
	}

	dir, err := initializePersistentAssetsCache()
	if err == nil {
		assetsCacheDir = dir
		return dir, nil
	}

	// Keep the historical temp-dir behavior so a cache problem never fails a
	// render.
	dir, err = os.MkdirTemp("", "symprint-assets-cache-*")
	if err != nil {
		assetsCacheErr = err
		return "", err
	}
	if err := materializeAssets(dir); err != nil {
		os.RemoveAll(dir)
		assetsCacheErr = err
		return "", err
	}
	assetsCacheDir = dir
	return dir, nil
}

type typstJob struct {
	profile          Profile
	front            Frontmatter
	body             []byte
	sourceDir        string
	outputPath       string
	pdfStandard      []string
	reproducible     bool
	fontPaths        []string
	ignoreFonts      bool
	shortDiagnostics bool
	timeout          time.Duration
}

// renderTypst materializes the work dir (templates + fonts + meta.json + body.md
// + main.typ) and runs `typst compile`, capturing stderr separately so a failure
// yields the engine's real diagnostic instead of a swallowed exit code.
func renderTypst(ctx context.Context, eng EngineInfo, job typstJob) (*Result, error) {
	start := time.Now()

	work, err := os.MkdirTemp("", "symprint-*")
	if err != nil {
		return nil, &RenderError{Stage: "write", Message: "could not create work dir", Err: err}
	}
	defer os.RemoveAll(work)

	cacheDir, err := getOrInitializeAssetsCache()
	if err != nil {
		return nil, &RenderError{Stage: "write", Message: "could not initialize assets cache", Err: err}
	}

	tplDir := filepath.Join(work, "templates")
	if err := os.Symlink(filepath.Join(cacheDir, "templates"), tplDir); err != nil {
		if err := assets.Materialize(tplDir); err != nil {
			return nil, &RenderError{Stage: "write", Message: "could not materialize templates", Err: err}
		}
	}

	fontDir := filepath.Join(work, "fonts")
	if err := os.Symlink(filepath.Join(cacheDir, "fonts"), fontDir); err != nil {
		if _, err := assets.MaterializeFonts(fontDir); err != nil {
			return nil, &RenderError{Stage: "write", Message: "could not materialize fonts", Err: err}
		}
	}

	pkgRoot := filepath.Join(work, "packages")
	if err := os.Symlink(filepath.Join(cacheDir, "packages"), pkgRoot); err != nil {
		if err := assets.MaterializePackages(work); err != nil {
			return nil, &RenderError{Stage: "write", Message: "could not materialize vendored packages", Err: err}
		}
	}

	metaJSON, err := json.MarshalIndent(job.front, "", "  ")
	if err != nil {
		return nil, &RenderError{Stage: "write", Message: "could not encode metadata", Err: err}
	}
	if err := os.WriteFile(filepath.Join(work, "meta.json"), metaJSON, 0o644); err != nil {
		return nil, &RenderError{Stage: "write", Message: "could not write metadata", Err: err}
	}
	if err := os.WriteFile(filepath.Join(work, "body.md"), job.body, 0o644); err != nil {
		return nil, &RenderError{Stage: "write", Message: "could not write body", Err: err}
	}
	// Copy locally referenced Markdown images into the work dir so the engine
	// resolves them under --root. Fails closed on missing files, absolute
	// paths, traversal and symlink escapes.
	if err := collectAssets(job.sourceDir, work, job.body); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(work, "main.typ"), []byte(mainTyp(job.profile.Template)), 0o644); err != nil {
		return nil, &RenderError{Stage: "write", Message: "could not write entry", Err: err}
	}

	out, err := filepath.Abs(job.outputPath)
	if err != nil {
		return nil, &RenderError{Stage: "write", Message: "invalid output path", Err: err}
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return nil, &RenderError{Stage: "write", Message: "could not create output dir", Err: err}
	}

	args := []string{"compile", "--root", work}

	// Resolve @preview/* imports (cmarker) from the vendored, embedded tree
	// instead of the network, and point the package cache at a per-render dir
	// so typst never needs the user's global package cache for these imports.
	args = append(args, "--package-path", pkgRoot)
	args = append(args, "--package-cache-path", filepath.Join(work, "package-cache"))

	// Machine-facing surfaces (MCP, --json) request typst's short diagnostic
	// format (one line per diagnostic: file:line:col: error: message);
	// interactive terminal renders keep the rich human format. Requires
	// typst >= 0.13; cleanTypstError stays as the truncation backstop.
	if job.shortDiagnostics {
		args = append(args, "--diagnostic-format", "short")
	}

	// Always include the embedded font directory for machine-independent output.
	args = append(args, "--font-path", fontDir)

	// Add user-specified font paths after the embedded dir (user paths take
	// priority when font names collide).
	for _, fp := range job.fontPaths {
		args = append(args, "--font-path", fp)
	}

	// When embedded fonts are available, ignore system fonts by default for
	// deterministic output. Users can override via --ignore-system-fonts=false.
	if job.ignoreFonts || len(job.fontPaths) == 0 {
		args = append(args, "--ignore-system-fonts")
	}
	if len(job.pdfStandard) > 0 {
		args = append(args, "--pdf-standard", strings.Join(job.pdfStandard, ","))
	}
	args = append(args, filepath.Join(work, "main.typ"), out)

	timeout := job.timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, eng.Path, args...)
	cmd.Dir = work
	cmd.WaitDelay = 5 * time.Second
	if job.reproducible {
		cmd.Env = append(os.Environ(), "SOURCE_DATE_EPOCH=0")
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if cctx.Err() == context.DeadlineExceeded {
			return nil, &RenderError{
				Stage:   "compile",
				Message: fmt.Sprintf("typst compile timed out after %s", timeout),
				Hint:    "raise engine.timeout_seconds in the config",
				Err:     err,
			}
		}
		return nil, &RenderError{
			Stage:   "compile",
			Message: "typst could not render the document",
			Detail:  cleanTypstError(stderr.String()),
			Hint:    "fix the source/template, then re-run; full engine output is above",
			Err:     err,
		}
	}

	res := &Result{
		OutputPath:    out,
		Profile:       job.profile.Name,
		Engine:        "typst",
		EngineVersion: eng.Version,
		PDFStandard:   job.pdfStandard,
		Reproducible:  job.reproducible,
		DurationMS:    time.Since(start).Milliseconds(),
	}
	if fi, err := os.Stat(out); err == nil {
		res.Bytes = fi.Size()
	}
	return res, nil
}

// mainTyp is the generated entrypoint. It renders the Markdown body with cmarker
// (CommonMark parsed in-process — no pandoc), delegates LaTeX math to the
// vendored mitex package, and applies the selected profile's `apply(meta, doc)`
// show rule. Every equation receives its original non-empty Markdown source as
// Typst alt text so PDF/UA-1 does not fail closed on an untagged equation.
func mainTyp(template string) string {
	return fmt.Sprintf(`// Generated by symprint — do not edit.
#import "@preview/cmarker:%s"
#import "@preview/mitex:%s": mitex
#import "/templates/%s" as profile
#let meta = json("/meta.json")
#show: profile.apply.with(meta)
#cmarker.render(read("/body.md"),
  math: (source, block: false) => mitex(source, block: block, alt: source),
  scope: (
    image: (source, ..args) => image(source, ..args),
  ),
)
`, cmarkerVersion, mitexVersion, template)
}

// cleanTypstError keeps typst's helpful, location-pointing error lines and drops
// noise, so the CLI/MCP surface a readable diagnostic rather than a raw dump.
func cleanTypstError(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 40 {
		lines = append(lines[:40], "  … (truncated)")
	}
	return strings.Join(lines, "\n")
}
