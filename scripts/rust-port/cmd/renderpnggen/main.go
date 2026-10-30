// Command renderpnggen records PNG output from the production Go draw engine.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"runtime"

	"github.com/danieljustus/symaira-desktop/internal/draw"
	"github.com/danieljustus/symaira-desktop/internal/draw/emit"
	"github.com/danieljustus/symaira-desktop/internal/draw/ir"
	parsepkg "github.com/danieljustus/symaira-desktop/internal/draw/parse"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

type sourceCase struct{ ID, Mode, Input string }
type pngCase struct {
	ID       string `json:"id"`
	Mode     string `json:"mode"`
	Input    string `json:"input"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	SHA256   string `json:"sha256"`
	Error    string `json:"error"`
	Withheld string `json:"withheld"`
}
type corpus struct {
	SchemaVersion int              `json:"schema_version"`
	Oracle        inventory.Oracle `json:"oracle"`
	Cases         []pngCase        `json:"cases"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	output := flag.String("output", "testdata/port/render/png.json", "fixture path")
	check := flag.Bool("check", false, "compare without writing")
	flag.Parse()
	// The recorded bytes depend on the host architecture: the Go compiler
	// fuses float32 multiply-adds into FMA instructions on arm64 (x/image's
	// lerp/devSquared and png.go carry no float32 conversions), and the amd64
	// rasterizer sums coverage in a different order. The corpus is recorded
	// on arm64 and the Rust port reproduces exactly that arithmetic.
	if runtime.GOARCH != "arm64" {
		if *check {
			fmt.Printf("SKIP render PNG oracle check: fixture is recorded on arm64, host is %s\n", runtime.GOARCH)
			return nil
		}
		return fmt.Errorf("render PNG fixture must be generated on arm64, host is %s", runtime.GOARCH)
	}
	root, err := fixtureoracle.FindRepositoryRoot(".")
	if err != nil {
		return err
	}
	oracle := fixtureoracle.Defaults()
	pin := inventory.Oracle{Commit: oracle.Commit, Release: oracle.Release}
	if err := fixtureoracle.ValidateSource(root, pin); err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(root, "testdata/port/render/emit.json"))
	if err != nil {
		return err
	}
	var src struct {
		Cases []sourceCase `json:"cases"`
	}
	if err := json.Unmarshal(data, &src); err != nil {
		return err
	}
	result := corpus{SchemaVersion: 1, Oracle: pin}
	for _, c := range src.Cases {
		if c.Mode != "source" && c.Mode != "ir" {
			continue
		}
		var d *ir.Diagram
		if c.Mode == "source" {
			d, err = parsepkg.Source(c.Input)
		} else {
			d = &ir.Diagram{}
			err = json.Unmarshal([]byte(c.Input), d)
		}
		if err != nil {
			return fmt.Errorf("%s parse: %w", c.ID, err)
		}
		item := pngCase{ID: c.ID, Mode: c.Mode, Input: c.Input}
		sc, e := draw.BuildSceneFromIR(d)
		if e == nil {
			// The SVG corpus contains enormous scenes that panic or exhaust memory
			// when rasterized. Keep them visible as untested, never as errors.
			w, h := math.Ceil(sc.Width), math.Ceil(sc.Height)
			if w <= 0 {
				w = 100
			}
			if h <= 0 {
				h = 100
			}
			if math.IsNaN(w) || math.IsNaN(h) || w > 4096 || h > 4096 || w*h > 4_000_000 {
				item.Withheld = "raster dimensions exceed capture budget"
				result.Cases = append(result.Cases, item)
				continue
			}
			var b []byte
			b, e = emit.EmitPNG(sc, emit.RasterOptions{})
			if e == nil {
				config, decodeErr := png.DecodeConfig(bytes.NewReader(b))
				if decodeErr != nil {
					return decodeErr
				}
				item.Width, item.Height = config.Width, config.Height
				item.SHA256 = fmt.Sprintf("%x", sha256.Sum256(b))
			}
		}
		if e != nil {
			item.Error = e.Error()
		}
		result.Cases = append(result.Cases, item)
	}
	if len(result.Cases) == 0 {
		return fmt.Errorf("empty PNG corpus")
	}
	data, err = json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if *check {
		old, err := os.ReadFile(*output)
		if err != nil {
			return err
		}
		if !bytes.Equal(old, data) {
			return fmt.Errorf("render PNG fixture is stale")
		}
	} else if err := os.WriteFile(*output, data, 0600); err != nil {
		return err
	}
	fmt.Printf("PASS render PNG fixture (%d cases)\n", len(result.Cases))
	return nil
}
