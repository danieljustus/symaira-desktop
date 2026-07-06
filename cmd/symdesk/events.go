package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/vault"
)

func newEventsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "events",
		Short: "Watch vault for changes and emit events (NDJSON)",
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()

			watcher, err := fsnotify.NewWatcher()
			if err != nil {
				return err
			}
			defer watcher.Close()

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

			stdinClosed := make(chan struct{})
			go func() {
				scanner := bufio.NewScanner(os.Stdin)
				for scanner.Scan() {
				}
				close(stdinClosed)
			}()

			type debounceEvent struct {
				Op fsnotify.Op
				Ts time.Time
			}
			debounceMap := make(map[string]*debounceEvent)
			var mu sync.Mutex

			go func() {
				for {
					time.Sleep(500 * time.Millisecond)
					mu.Lock()
					now := time.Now()
					for path, ev := range debounceMap {
						if now.Sub(ev.Ts) >= 500*time.Millisecond {
							// Process event
							delete(debounceMap, path)
							
							opName := ""
							if ev.Op&fsnotify.Create == fsnotify.Create {
								opName = "file_added"
								if info, err := os.Stat(path); err == nil && info.IsDir() {
									watcher.Add(path)
								}
							} else if ev.Op&fsnotify.Write == fsnotify.Write {
								opName = "file_changed"
							} else if ev.Op&fsnotify.Remove == fsnotify.Remove || ev.Op&fsnotify.Rename == fsnotify.Rename {
								opName = "file_removed"
								// TODO: remove from sidecar DB if file removed
							}

							if opName != "" && filepath.Ext(path) == ".md" {
								// Index it
								if opName != "file_removed" {
									doc, err := vault.ParseFile(path)
									if err == nil {
										_ = db.IndexDocument(doc)
										opName = "index_updated" // As requested by plan: index_updated upon re-indexing
									}
								}

								evt := map[string]interface{}{
									"event": opName,
									"path":  path,
									"ts":    now.UTC().Format(time.RFC3339),
								}
								b, _ := json.Marshal(evt)
								fmt.Println(string(b))
							}
						}
					}
					mu.Unlock()
				}
			}()

			for {
				select {
				case <-sigs:
					return nil
				case <-stdinClosed:
					return nil
				case event, ok := <-watcher.Events:
					if !ok {
						return nil
					}
					mu.Lock()
					debounceMap[event.Name] = &debounceEvent{
						Op: event.Op,
						Ts: time.Now(),
					}
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
