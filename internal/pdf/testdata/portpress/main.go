// Command portpress exercises the shipped profile and engine-discovery APIs.
package main

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/pdf"
	"github.com/danieljustus/symaira-desktop/internal/pdf/internal/press"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/pdfops/reference"
)

type step struct {
	Operation       string `json:"operation"`
	Name            string `json:"name"`
	Path            string `json:"path"`
	Source          string `json:"source"`
	Cancel          bool   `json:"cancel"`
	TimeoutMS       int64  `json:"timeout_ms"`
	ExpiredDeadline bool   `json:"expired_deadline"`
	CancelMarker    string `json:"cancel_marker"`
}

func main() {
	if runtime.Version() != "go1.26.10" {
		panic("press oracle requires go1.26.10")
	}
	if len(os.Args) == 2 && os.Args[1] == "--identity" {
		emit(map[string]any{"oracle": reference.Current(), "compiler": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH})
		return
	}
	var steps []step
	if err := json.NewDecoder(os.Stdin).Decode(&steps); err != nil {
		panic(err)
	}
	results := make([]any, 0, len(steps))
	for _, s := range steps {
		var result any
		switch s.Operation {
		case "profiles":
			capabilities := make([]any, 0)
			for _, p := range press.All() {
				capabilities = append(capabilities, map[string]any{"name": p.Name, "capability": p.Capability(), "accessibility": p.RequiresAccessibility()})
			}
			result = map[string]any{"full": press.All(), "facade": pdf.Profiles(), "capabilities": capabilities}
		case "lookup":
			p, ok := press.Lookup(s.Name)
			if ok {
				result = p
			}
		case "detect":
			ctx, cancel := context.WithCancel(context.Background())
			if s.TimeoutMS > 0 {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), time.Duration(s.TimeoutMS)*time.Millisecond)
			}
			if s.Cancel {
				cancel()
			}
			if s.ExpiredDeadline {
				cancel()
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			}
			var done chan struct{}
			if s.CancelMarker != "" {
				done = make(chan struct{})
				go func() {
					defer close(done)
					deadline := time.Now().Add(10 * time.Second)
					for time.Now().Before(deadline) {
						if _, err := os.Stat(s.CancelMarker); err == nil {
							cancel()
							return
						}
						time.Sleep(time.Millisecond)
					}
					panic("tool did not signal readiness")
				}()
			}
			result = press.DetectTypst(ctx, s.Name)
			if done != nil {
				<-done
			}
			cancel()
		case "reset":
			press.ResetProbeCache()
		case "remove":
			if err := os.Remove(s.Path); err != nil {
				panic(err)
			}
		case "install":
			b, err := os.ReadFile(s.Source)
			if err != nil {
				panic(err)
			}
			if err = os.WriteFile(s.Path, b, 0o700); err != nil {
				panic(err)
			}
		default:
			panic("unknown operation")
		}
		results = append(results, result)
	}
	emit(results)
}
func emit(value any) {
	if err := json.NewEncoder(os.Stdout).Encode(value); err != nil {
		panic(err)
	}
}
