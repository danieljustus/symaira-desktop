package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/danieljustus/symaira-ingest/internal/extract"
	"github.com/danieljustus/symaira-ingest/internal/store"
	"github.com/fsnotify/fsnotify"
)

// Watcher monitors a directory for files to ingest.
type Watcher struct {
	store         *store.Store
	inboxDir      string
	watcher       *fsnotify.Watcher
	pending       map[string]*fileState
	stableFor     time.Duration
	clock         Clock
	processingDir string
	processedDir  string
	failedDir     string
	mu            sync.Mutex
}

type fileState struct {
	lastSize    int64
	lastModTime time.Time
	createdAt   time.Time
	timer       timer
}

type WatcherOptions struct {
	StableFor     time.Duration
	ProcessingDir string
	ProcessedDir  string
	FailedDir     string
	Clock         Clock
}

// NewWatcher creates a new Watcher instance for the given inbox directory.
func NewWatcher(s *store.Store, inboxDir string) (*Watcher, error) {
	return NewWatcherWithOptions(s, inboxDir, WatcherOptions{})
}

func NewWatcherWithOptions(s *store.Store, inboxDir string, opts WatcherOptions) (*Watcher, error) {
	inboxDir = filepath.Clean(inboxDir)
	if opts.StableFor <= 0 {
		opts.StableFor = time.Second
	}
	if opts.Clock == nil {
		opts.Clock = realClock{}
	}
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create fsnotify watcher: %w", err)
	}

	processingDir, err := cleanOptionalDir(opts.ProcessingDir)
	if err != nil {
		fw.Close()
		return nil, fmt.Errorf("resolve processing dir: %w", err)
	}
	processedDir, err := cleanOptionalDir(opts.ProcessedDir)
	if err != nil {
		fw.Close()
		return nil, fmt.Errorf("resolve processed dir: %w", err)
	}
	failedDir, err := cleanOptionalDir(opts.FailedDir)
	if err != nil {
		fw.Close()
		return nil, fmt.Errorf("resolve failed dir: %w", err)
	}

	return &Watcher{
		store:         s,
		inboxDir:      inboxDir,
		watcher:       fw,
		pending:       make(map[string]*fileState),
		stableFor:     opts.StableFor,
		clock:         opts.Clock,
		processingDir: processingDir,
		processedDir:  processedDir,
		failedDir:     failedDir,
	}, nil
}

var getwdFn = os.Getwd

func cleanOptionalDir(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	pwd, err := getwdFn()
	if err != nil {
		return "", err
	}
	return filepath.Clean(filepath.Join(pwd, path)), nil
}

// Close closes the underlying fsnotify watcher.
func (w *Watcher) Close() error {
	return w.watcher.Close()
}

// Start initiates directory watching and processes events until context cancellation.
func (w *Watcher) Start(ctx context.Context) error {
	// 1. Initial recursive scan and watch setup
	err := filepath.WalkDir(w.inboxDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if w.shouldIgnore(path) {
				return filepath.SkipDir
			}
			log.Printf("[Watcher] Watching directory: %s", path)
			if err := w.watcher.Add(path); err != nil {
				return fmt.Errorf("watch dir %s: %w", path, err)
			}
		} else {
			if !w.shouldIgnore(path) {
				w.debounceFile(ctx, path)
			}
		}
		return nil
	})
	if err != nil {
		w.watcher.Close()
		return fmt.Errorf("initial watch setup walk: %w", err)
	}

	// 2. Event loop
	go func() {
		defer w.watcher.Close()
		defer func() {
			w.mu.Lock()
			for _, state := range w.pending {
				w.clock.StopTimer(state.timer)
			}
			w.pending = make(map[string]*fileState)
			w.mu.Unlock()
		}()

		for {
			select {
			case <-ctx.Done():
				log.Println("[Watcher] Shutting down event loop...")
				return
			case event, ok := <-w.watcher.Events:
				if !ok {
					return
				}
				if w.shouldIgnore(event.Name) {
					continue
				}

				// If a new directory is created, watch it recursively
				if event.Op&fsnotify.Create == fsnotify.Create {
					info, err := os.Stat(event.Name)
					if err == nil && info.IsDir() {
						log.Printf("[Watcher] New directory detected, adding to watch: %s", event.Name)
						w.watchDirectoryRecursive(ctx, event.Name)
						continue
					}
				}

				// Handle file modifications/creations
				if event.Op&(fsnotify.Create|fsnotify.Write) != 0 {
					w.debounceFile(ctx, event.Name)
				}

				// Handle removals
				if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
					w.cancelDebounce(event.Name)
				}

			case err, ok := <-w.watcher.Errors:
				if !ok {
					return
				}
				log.Printf("[Watcher] fsnotify error: %v", err)
			}
		}
	}()

	return nil
}

func (w *Watcher) watchDirectoryRecursive(ctx context.Context, dir string) {
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if w.shouldIgnore(path) {
				return filepath.SkipDir
			}
			log.Printf("[Watcher] Recursively watching directory: %s", path)
			_ = w.watcher.Add(path)
		} else {
			if !w.shouldIgnore(path) {
				w.debounceFile(ctx, path)
			}
		}
		return nil
	})
}

func (w *Watcher) debounceFile(ctx context.Context, path string) {
	w.mu.Lock()

	info, err := os.Stat(path)
	if err != nil {
		// File might have been deleted, clean up
		if state, ok := w.pending[path]; ok {
			w.clock.StopTimer(state.timer)
			delete(w.pending, path)
		}
		w.mu.Unlock()
		return
	}

	if state, ok := w.pending[path]; ok {
		w.clock.StopTimer(state.timer)
		state.lastSize = info.Size()
		state.lastModTime = info.ModTime()
	} else {
		w.pending[path] = &fileState{
			lastSize:    info.Size(),
			lastModTime: info.ModTime(),
			createdAt:   w.clock.Now(),
		}
	}

	state := w.pending[path]
	state.timer = w.clock.AfterFunc(w.stableFor, func() {
		w.checkStability(ctx, path)
	})

	// Release the lock before the timer callback may fire. With the real
	// clock the callback runs in a goroutine; with a fake clock tests
	// call Advance externally after debounceFile returns.
	w.mu.Unlock()
}

func (w *Watcher) checkStability(ctx context.Context, path string) {
	w.mu.Lock()
	state, exists := w.pending[path]
	if !exists {
		w.mu.Unlock()
		return
	}

	const maxPendingAge = 5 * time.Minute
	if w.clock.Now().Sub(state.createdAt) > maxPendingAge {
		delete(w.pending, path)
		w.clock.StopTimer(state.timer)
		w.mu.Unlock()

		log.Printf("[Watcher] File pending longer than %v, force-enqueuing: %s", maxPendingAge, path)
		if err := w.enqueueFile(ctx, path); err != nil {
			log.Printf("[Watcher] Error force-enqueuing file %s: %v", path, err)
		}
		return
	}

	info, err := os.Stat(path)
	if err != nil {
		delete(w.pending, path)
		w.mu.Unlock()
		return
	}

	if info.IsDir() {
		delete(w.pending, path)
		w.mu.Unlock()
		return
	}

	currentSize := info.Size()
	currentModTime := info.ModTime()

	if currentSize == state.lastSize && currentModTime.Equal(state.lastModTime) {
		delete(w.pending, path)
		w.mu.Unlock()

		log.Printf("[Watcher] File is stable, submitting to queue: %s", path)
		if err := w.enqueueFile(ctx, path); err != nil {
			log.Printf("[Watcher] Error enqueuing file %s: %v", path, err)
		}
		return
	}

	state.lastSize = currentSize
	state.lastModTime = currentModTime
	state.timer = w.clock.AfterFunc(w.stableFor, func() {
		w.checkStability(ctx, path)
	})
	w.mu.Unlock()
}

func (w *Watcher) cancelDebounce(path string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if state, ok := w.pending[path]; ok {
		w.clock.StopTimer(state.timer)
		delete(w.pending, path)
	}
}

func (w *Watcher) enqueueFile(ctx context.Context, path string) error {
	workPath, err := w.moveToProcessing(path)
	if err != nil {
		return err
	}

	kind, err := extract.Detect(workPath)
	if err != nil {
		w.handlePreQueueFailure(workPath, "detect", err)
		return fmt.Errorf("detect file kind: %w", err)
	}

	hash, err := hashFile(workPath)
	if err != nil {
		w.handlePreQueueFailure(workPath, "hash", err)
		return fmt.Errorf("hash file: %w", err)
	}

	doc, created, err := w.store.CreateOrGet(ctx, workPath, hash, string(kind))
	if err != nil {
		w.handlePreQueueFailure(workPath, "store", err)
		return fmt.Errorf("create or get document: %w", err)
	}

	if !created {
		log.Printf("[Watcher] File %s (hash %s) already ingested or enqueued. Recording skipped job.", workPath, hash)
		if _, err := w.store.EnqueueSkippedJob(ctx, doc.ID, string(kind), "duplicate"); err != nil {
			w.handlePreQueueFailure(workPath, "store", err)
			return fmt.Errorf("enqueue skipped job: %w", err)
		}
		return nil
	}

	_, err = w.store.EnqueueJob(ctx, doc.ID, string(kind))
	if err != nil {
		w.handlePreQueueFailure(workPath, "store", err)
		return fmt.Errorf("enqueue job: %w", err)
	}

	return nil
}

func (w *Watcher) moveToProcessing(path string) (string, error) {
	if w.processingDir == "." || w.processingDir == "" {
		return path, nil
	}
	return moveFileToDir(path, w.processingDir)
}

func (w *Watcher) handlePreQueueFailure(path, stage string, err error) {
	if w.failedDir == "." || w.failedDir == "" {
		return
	}
	failedPath, moveErr := moveFileToDir(path, w.failedDir)
	if moveErr != nil {
		log.Printf("[Watcher] Failed to move %s to failed dir: %v", path, moveErr)
		return
	}
	if sidecarErr := writeFailureSidecar(failedPath, stage, err); sidecarErr != nil {
		log.Printf("[Watcher] Failed to write error sidecar for %s: %v", failedPath, sidecarErr)
	}
}

func (w *Watcher) shouldIgnore(path string) bool {
	if isIgnored(path) {
		return true
	}
	for _, dir := range []string{w.processingDir, w.processedDir, w.failedDir} {
		if dir == "" || dir == "." {
			continue
		}
		if isPathWithin(path, dir) {
			return true
		}
	}
	return false
}

func isPathWithin(path, dir string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel))
}

func moveFileToDir(path, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create target directory: %w", err)
	}
	base := filepath.Base(path)
	dst := filepath.Join(dir, base)
	for i := 2; fileExists(dst); i++ {
		ext := filepath.Ext(base)
		stem := strings.TrimSuffix(base, ext)
		dst = filepath.Join(dir, fmt.Sprintf("%s-%d%s", stem, i, ext))
	}
	if err := os.Rename(path, dst); err != nil {
		return "", fmt.Errorf("move %s to %s: %w", path, dst, err)
	}
	return dst, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

type failureSidecar struct {
	SourcePath string    `json:"source_path"`
	Stage      string    `json:"stage"`
	Error      string    `json:"error"`
	FailedAt   time.Time `json:"failed_at"`
}

func writeFailureSidecar(path, stage string, err error) error {
	payload := failureSidecar{SourcePath: path, Stage: stage, Error: err.Error(), FailedAt: time.Now().UTC()}
	data, marshalErr := json.MarshalIndent(payload, "", "  ")
	if marshalErr != nil {
		return marshalErr
	}
	return os.WriteFile(path+".error.json", append(data, '\n'), 0o600)
}

func isIgnored(path string) bool {
	base := filepath.Base(path)
	if strings.HasPrefix(base, ".") {
		return true
	}
	if strings.HasSuffix(base, ".swp") || strings.HasSuffix(base, ".swx") {
		return true
	}
	if strings.HasSuffix(base, ".tmp") || strings.HasSuffix(base, "~") {
		return true
	}
	return false
}
