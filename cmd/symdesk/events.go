package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"context"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/mail"
	"github.com/danieljustus/symaira-desktop/internal/service"
	"github.com/danieljustus/symaira-desktop/internal/vault"
	deskwatcher "github.com/danieljustus/symaira-desktop/internal/watcher"
)

// DebouncedEvent tracks a pending filesystem event for debouncing.
type DebouncedEvent struct {
	Op fsnotify.Op
	Ts time.Time
}

// accumulateDebounce merges a new filesystem event into the debounce map,
// keeping the earliest timestamp and OR-ing the operation bits so a Create
// followed by Write/Chmod still resolves to a create, not a plain change.
// Caller must hold the debounce mutex.
func accumulateDebounce(debounceMap map[string]*DebouncedEvent, path string, op fsnotify.Op) {
	if ev, ok := debounceMap[path]; ok {
		ev.Op |= op
		return
	}
	debounceMap[path] = &DebouncedEvent{Op: op, Ts: time.Now()}
}

// processEvent handles a single debounced filesystem event. It determines the
// operation name, performs side effects (indexing, deletion for .md files), and
// writes the NDJSON event line to out when the file is markdown.
func processEvent(path string, ev *DebouncedEvent, w *fsnotify.Watcher, svc *service.Service, out io.Writer) {
	opName := ""
	if ev.Op&fsnotify.Create == fsnotify.Create {
		opName = "file_added"
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			if err := w.Add(path); err != nil {
				fmt.Fprintf(os.Stderr, "failed to watch created directory %s: %v\n", path, err)
			}
		}
	} else if ev.Op&fsnotify.Write == fsnotify.Write {
		opName = "file_changed"
	} else if ev.Op&fsnotify.Remove == fsnotify.Remove || ev.Op&fsnotify.Rename == fsnotify.Rename {
		opName = "file_removed"
		_ = svc.DeleteDocument(path)
	}

	if opName != "" && filepath.Ext(path) == ".md" {
		// Index it
		if opName != "file_removed" {
			doc, err := vault.ParseFile(path)
			if err == nil {
				_ = svc.IndexDocument(doc)
				opName = "index_updated" // As requested by plan: index_updated upon re-indexing
			}
		}

		evt := map[string]interface{}{
			"event": opName,
			"path":  path,
			"ts":    ev.Ts.UTC().Format(time.RFC3339),
		}
		b, _ := json.Marshal(evt)
		if _, err := fmt.Fprintln(out, string(b)); err != nil {
			fmt.Fprintf(os.Stderr, "failed to emit filesystem event for %s: %v\n", path, err)
		}
	}
}

// flushDebounce processes all matured events in the debounce map (those older
// than 500ms) and removes them. Caller must hold or provide mu for synchronisation.
func flushDebounce(debounceMap map[string]*DebouncedEvent, mu *sync.Mutex, w *fsnotify.Watcher, svc *service.Service, out io.Writer) {
	mu.Lock()
	defer mu.Unlock()
	now := time.Now()
	for path, ev := range debounceMap {
		if now.Sub(ev.Ts) >= 500*time.Millisecond {
			delete(debounceMap, path)
			processEvent(path, ev, w, svc, out)
		}
	}
}

func newEventsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "events",
		Short: "Watch vault for changes and emit events (NDJSON)",
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer closeWithWarning("sidecar database", db.Close)

			svc := service.New(vRoot, db)

			watcher, err := fsnotify.NewWatcher()
			if err != nil {
				return err
			}
			defer closeWithWarning("filesystem watcher", watcher.Close)

			// Start Inbox Watcher in the background
			inboxDir := cfg.Inbox
			if inboxDir == "" {
				inboxDir = filepath.Join(vRoot, "inbox_watch")
			}
			inboxWatcher, err := deskwatcher.NewInboxWatcher(inboxDir, svc)
			if err == nil {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				go func() {
					if err := inboxWatcher.Start(ctx); err != nil {
						fmt.Fprintf(os.Stderr, "inbox watcher error: %v\n", err)
					}
				}()
				defer closeWithWarning("inbox watcher", inboxWatcher.Close)
			} else {
				fmt.Fprintf(os.Stderr, "failed to start inbox watcher: %v\n", err)
			}

			// Start Mail Watcher in the background for IMAP email ingestion.
			// It polls symingest mail accounts periodically and routes
			// fetched messages through the same ingest pipeline. An empty
			// path resolves through config.MailConfigPath (XDG-aware,
			// issue #755).
			mailWatcher, err := mail.New("", svc)
			if err == nil {
				mailCtx, mailCancel := context.WithCancel(context.Background())
				defer mailCancel()
				go func() {
					if err := mailWatcher.Start(mailCtx); err != nil {
						fmt.Fprintf(os.Stderr, "mail watcher error: %v\n", err)
					}
				}()
			} else {
				fmt.Fprintf(os.Stderr, "mail watcher not started (mail config not available): %v\n", err)
			}

			// Watch all subdirectories
			err = filepath.Walk(vRoot, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if info.IsDir() {
					if strings.HasPrefix(filepath.Base(path), ".") && path != vRoot {
						return filepath.SkipDir
					}
					return watcher.Add(path)
				}
				return nil
			})
			if err != nil {
				return err
			}

			sigs := make(chan os.Signal, 1)
			signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
			defer signal.Stop(sigs)

			stdinClosed := make(chan struct{})
			go func() {
				scanner := bufio.NewScanner(os.Stdin)
				for scanner.Scan() {
				}
				close(stdinClosed)
			}()

			debounceMap := make(map[string]*DebouncedEvent)
			var mu sync.Mutex
			debounceTicker := time.NewTicker(500 * time.Millisecond)
			defer debounceTicker.Stop()

			for {
				select {
				case <-sigs:
					return nil
				case <-stdinClosed:
					return nil
				case <-debounceTicker.C:
					flushDebounce(debounceMap, &mu, watcher, svc, os.Stdout)
				case event, ok := <-watcher.Events:
					if !ok {
						return nil
					}
					accumulateDebounce(debounceMap, event.Name, event.Op)
					mu.Unlock()
				case err, ok := <-watcher.Errors:
					if !ok {
						return nil
					}
					fmt.Fprintf(os.Stderr, "watcher error: %v\n", err)
				}
			}
		},
	}
}
