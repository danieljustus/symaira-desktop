package watcher

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/service"
	"github.com/fsnotify/fsnotify"
)

// EventSource abstracts the filesystem event source used by InboxWatcher so
// that unit tests can inject synthetic events instead of relying on real
// filesystem timing.
type EventSource interface {
	Events() <-chan fsnotify.Event
	Errors() <-chan error
	Close() error
}

// fsnotifySource is the production implementation of EventSource backed by
// fsnotify.
type fsnotifySource struct {
	watcher *fsnotify.Watcher
}

func (s *fsnotifySource) Events() <-chan fsnotify.Event { return s.watcher.Events }
func (s *fsnotifySource) Errors() <-chan error          { return s.watcher.Errors }
func (s *fsnotifySource) Close() error                  { return s.watcher.Close() }

type InboxWatcher struct {
	inboxDir         string
	svc              *service.Service
	source           EventSource
	tickerInterval   time.Duration
	stabilityTimeout time.Duration
}

func NewInboxWatcher(inboxDir string, svc *service.Service) (*InboxWatcher, error) {
	if err := os.MkdirAll(inboxDir, 0755); err != nil {
		return nil, err
	}
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if err := fw.Add(inboxDir); err != nil {
		fw.Close()
		return nil, err
	}
	return NewInboxWatcherWithSource(inboxDir, svc, &fsnotifySource{watcher: fw})
}

// NewInboxWatcherWithSource creates an InboxWatcher with an injected event
// source and timing parameters. It is used in tests to make the watcher fast
// and deterministic.
func NewInboxWatcherWithSource(
	inboxDir string,
	svc *service.Service,
	source EventSource,
) (*InboxWatcher, error) {
	return NewInboxWatcherWithTiming(inboxDir, svc, source, 1*time.Second, 2*time.Second)
}

// NewInboxWatcherWithTiming creates an InboxWatcher with explicit ticker and
// stability timing. Used by tests and advanced callers; prefer
// NewInboxWatcher for production use.
func NewInboxWatcherWithTiming(
	inboxDir string,
	svc *service.Service,
	source EventSource,
	tickerInterval time.Duration,
	stabilityTimeout time.Duration,
) (*InboxWatcher, error) {
	if err := os.MkdirAll(inboxDir, 0755); err != nil {
		return nil, err
	}
	return &InboxWatcher{
		inboxDir:         inboxDir,
		svc:              svc,
		source:           source,
		tickerInterval:   tickerInterval,
		stabilityTimeout: stabilityTimeout,
	}, nil
}

func (w *InboxWatcher) Start(ctx context.Context) error {
	type fileState struct {
		lastMod time.Time
		done    bool
	}
	stableFiles := make(map[string]*fileState)
	var mu sync.Mutex

	// Debounce and stability checker
	go func() {
		ticker := time.NewTicker(w.tickerInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				mu.Lock()
				now := time.Now()
				for path, state := range stableFiles {
					if state.done {
						continue
					}
					// If the file hasn't been modified for the stability timeout, process it
					if now.Sub(state.lastMod) >= w.stabilityTimeout {
						state.done = true
						go func(p string) {
							log.Printf("InboxWatcher: stable file detected, ingesting: %s", p)
							res, err := w.svc.Ingest(p)
							if err != nil {
								log.Printf("InboxWatcher: ingest failed: %v", err)
								mu.Lock()
								delete(stableFiles, p)
								mu.Unlock()
							} else {
								log.Printf("InboxWatcher: ingest succeeded: %v", res)
								// Delete the source file after a successful ingest
								if err := os.Remove(p); err != nil {
									log.Printf("InboxWatcher: failed to remove consumed file: %v", err)
								}
								mu.Lock()
								delete(stableFiles, p)
								mu.Unlock()
							}
						}(path)
					}
				}
				mu.Unlock()
			}
		}
	}()

	log.Printf("InboxWatcher: started watching %s", w.inboxDir)

	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-w.source.Events():
			if !ok {
				return nil
			}
			if strings.HasPrefix(filepath.Base(event.Name), ".") || filepath.Ext(event.Name) == ".md" {
				continue
			}
			info, err := os.Stat(event.Name)
			if err != nil {
				if event.Op&fsnotify.Remove == fsnotify.Remove {
					mu.Lock()
					delete(stableFiles, event.Name)
					mu.Unlock()
				}
				continue
			}
			if info.IsDir() {
				continue
			}

			if event.Op&(fsnotify.Create|fsnotify.Write) != 0 {
				mu.Lock()
				stableFiles[event.Name] = &fileState{
					lastMod: time.Now(),
					done:    false,
				}
				mu.Unlock()
			}
		case err, ok := <-w.source.Errors():
			if !ok {
				return nil
			}
			log.Printf("InboxWatcher error: %v", err)
		}
	}
}

func (w *InboxWatcher) Close() error {
	return w.source.Close()
}
