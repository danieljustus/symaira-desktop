// Command portpdfops is a test-only driver of the real PDF adapter and facade.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/ingest"
	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/pdfops"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

type request struct {
	Operation    string       `json:"operation"`
	Layer        string       `json:"layer"`
	Input        string       `json:"input"`
	Inputs       []string     `json:"inputs"`
	Output       string       `json:"output"`
	Pages        string       `json:"pages"`
	Degrees      int          `json:"degrees"`
	Tools        pdfops.Tools `json:"tools"`
	Cancel       bool         `json:"cancel"`
	CancelMarker string       `json:"cancel_marker"`
}

func main() {
	if runtime.Version() != "go1.26.9" {
		panic("PDF oracle requires go1.26.9")
	}
	if len(os.Args) == 2 && os.Args[1] == "--identity" {
		emit(map[string]any{"oracle": fixtureoracle.Current(), "compiler": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH})
		return
	}
	var r request
	if err := json.NewDecoder(os.Stdin).Decode(&r); err != nil {
		panic(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if r.Cancel {
		cancel()
	}
	if r.CancelMarker != "" {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				if _, err := os.Stat(r.CancelMarker); err == nil {
					cancel()
					return
				}
				time.Sleep(time.Millisecond)
			}
		}()
	}
	var result any
	var err error
	switch r.Operation {
	case "parse":
		result, err = pdfops.ParsePages(r.Pages)
	case "split":
		if r.Layer == "api" {
			pdfops.WithToolsFactory(func() pdfops.Tools { return r.Tools }, func() { result, err = ingest.SplitPDFAtSpec(ctx, r.Input, r.Pages, r.Output) })
		} else {
			result, err = r.Tools.Split(ctx, r.Input, r.Pages, r.Output)
		}
	case "merge":
		if r.Layer == "api" {
			pdfops.WithToolsFactory(func() pdfops.Tools { return r.Tools }, func() { err = ingest.MergePDFs(ctx, r.Inputs, r.Output) })
		} else {
			err = r.Tools.Merge(ctx, r.Inputs, r.Output)
		}
	case "rotate":
		if r.Layer == "api" {
			pdfops.WithToolsFactory(func() pdfops.Tools { return r.Tools }, func() { err = ingest.RotatePDF(ctx, r.Input, r.Output, r.Degrees, r.Pages) })
		} else {
			err = r.Tools.Rotate(ctx, r.Input, r.Output, r.Degrees, r.Pages)
		}
	default:
		panic("unknown operation")
	}
	message := ""
	if err != nil {
		message = err.Error()
		result = nil
	}
	emit(map[string]any{"result": result, "error": message})
}
func emit(value any) {
	if err := json.NewEncoder(os.Stdout).Encode(value); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
