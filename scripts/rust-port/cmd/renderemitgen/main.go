// Command renderemitgen captures real BuildSceneFromIR and EmitSVG output.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/danieljustus/symaira-desktop/internal/draw"
	"github.com/danieljustus/symaira-desktop/internal/draw/emit"
	"github.com/danieljustus/symaira-desktop/internal/draw/ir"
	parsepkg "github.com/danieljustus/symaira-desktop/internal/draw/parse"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

type testCase struct {
	ID       string `json:"id"`
	Mode     string `json:"mode"`
	Input    string `json:"input"`
	Expected string `json:"expected"`
	Error    string `json:"error"`
}
type corpus struct {
	SchemaVersion int              `json:"schema_version"`
	Oracle        inventory.Oracle `json:"oracle"`
	FMAWithheld   int              `json:"fma_withheld"`
	ChartResidual int              `json:"chart_residual"`
	Cases         []testCase       `json:"cases"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	output := flag.String("output", "testdata/port/render/emit.json", "fixture path")
	check := flag.Bool("check", false, "compare without writing")
	flag.Parse()
	root, err := fixtureoracle.FindRepositoryRoot(".")
	if err != nil {
		return err
	}
	oracle := fixtureoracle.Defaults()
	if err = fixtureoracle.ValidateSource(root, inventory.Oracle{Commit: oracle.Commit, Release: oracle.Release}); err != nil {
		return err
	}
	f := corpus{SchemaVersion: 1, Oracle: inventory.Oracle{Commit: oracle.Commit, Release: oracle.Release}}
	seen := map[string]bool{}
	add := func(id, mode, input string) error {
		key := mode + "\x00" + input
		if seen[key] {
			return nil
		}
		seen[key] = true
		var d *ir.Diagram
		var err error
		if mode == "source" {
			d, err = parsepkg.Source(input)
		} else {
			d = &ir.Diagram{}
			err = json.Unmarshal([]byte(input), d)
		}
		if err != nil {
			return nil
		} // Parser errors belong to the DSL fixture.

		if chartFMAAmbiguous(d) {
			f.FMAWithheld++
			return nil
		}
		c := testCase{ID: id, Mode: mode, Input: input}
		sc, err := draw.BuildSceneFromIR(d)
		if err != nil {
			c.Error = err.Error()
		} else {
			b, e := emit.EmitSVG(sc, emit.SVGEmitOptions{})
			if e != nil {
				c.Error = e.Error()
			} else {
				c.Expected = string(b)
			}
		}
		f.Cases = append(f.Cases, c)
		return nil
	}
	for _, name := range []string{"dsl-parse", "layout"} {
		b, e := os.ReadFile(filepath.Join(root, "testdata/port/render", name+".json"))
		if e != nil {
			return e
		}
		var src struct {
			Cases []struct{ ID, Mode, Input string }
		}
		if e = json.Unmarshal(b, &src); e != nil {
			return e
		}
		for _, c := range src.Cases {
			mode := c.Mode
			if mode != "ir" {
				mode = "source"
			}
			if e = add(name+"-"+c.ID, mode, c.Input); e != nil {
				return e
			}
		}
	}
	for i, input := range []string{`{}`, `{"kind":"graph","nodes":[{"id":"a","shape":"invalid"}]}`, `{"kind":"sequence"}`, `{"kind":"graph","nodes":[{"id":"a","label":"<&\"'\nUnicode 日本語","x":10,"y":20,"note":"https://example.invalid/?a=1&b=2"}]}`} {
		if err = add(fmt.Sprintf("hand-%d", i), "ir", input); err != nil {
			return err
		}
	}
	for i, input := range chartInputs() {
		if err = add(fmt.Sprintf("chart-%d", i), "ir", input); err != nil {
			return err
		}
	}
	extra, err := primitiveCases()
	if err != nil {
		return err
	}
	f.Cases = append(f.Cases, extra...)
	outlines, err := outlineCases()
	if err != nil {
		return err
	}
	f.Cases = append(f.Cases, outlines...)
	if err = parserSeeds(root, *check); err != nil {
		return err
	}
	if len(f.Cases) == 0 {
		return fmt.Errorf("empty emit corpus")
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if *check {
		old, e := os.ReadFile(*output)
		if e != nil {
			return e
		}
		if !bytes.Equal(old, data) {
			return fmt.Errorf("render emit fixture is stale; regenerate deliberately")
		}
	} else {
		if err = os.MkdirAll(filepath.Dir(*output), 0750); err != nil {
			return err
		}
		if err = os.WriteFile(*output, data, 0600); err != nil {
			return err
		}
	}
	fmt.Printf("PASS render emit fixture (%d cases, %d chart residuals)\n", len(f.Cases), f.ChartResidual)
	return nil
}
