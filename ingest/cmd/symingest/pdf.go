package main

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-ingest/internal/ingest"
	"github.com/danieljustus/symaira-ingest/internal/ocr"
	"github.com/danieljustus/symaira-ingest/internal/pdfops"
	"github.com/danieljustus/symaira-ingest/internal/store"
	"github.com/danieljustus/symaira-ingest/internal/writer"
)

func ingestDerivedPDFs(cfg *resolvedConfig, paths []string) ([]string, error) {
	if cfg.vault == "" {
		return nil, fmt.Errorf("cannot ingest generated PDFs: no vault configured; pass --vault or configure a vault")
	}
	st, err := store.Open(cfg.db)
	if err != nil {
		return nil, fmt.Errorf("open document store: %w", err)
	}
	defer st.Close()
	pipeline := &ingest.Pipeline{
		Engine:     ocr.NewEngine(cfg.ocrLang, cfg.ollamaBaseURL, cfg.ollamaModel),
		Store:      st,
		Writer:     &writer.NoteWriter{Vault: cfg.vault},
		ArchiveDir: cfg.archive,
	}
	configurePostIndex(pipeline, cfg)
	var notes []string
	for _, path := range paths {
		result, err := pipeline.Ingest(context.Background(), path, nil)
		if err != nil {
			return nil, fmt.Errorf("ingest generated PDF %s: %w", path, err)
		}
		notes = append(notes, result.VaultPath)
	}
	return notes, nil
}

func runPDFSplit(args []string) error {
	fs := flag.NewFlagSet("split", flag.ContinueOnError)
	at := fs.String("at", "", "Split after these pages, e.g. 2,4 or 2-3,6")
	outputDir := fs.String("output-dir", "", "Directory for generated PDF parts (default: <input>.parts)")
	ingestFlag := fs.Bool("ingest", false, "Run generated PDFs through the normal ingest pipeline")
	ocrLang, vault, archive, db := registerSharedFlags(fs)
	configureUsage(fs, "split [flags] <file>", "Split a PDF after selected pages. Requires Poppler pdfinfo, pdfseparate and pdfunite.")
	help, err := parseFlags(fs, args, "invalid split flags")
	if help || err != nil {
		return err
	}
	if *at == "" || len(fs.Args()) != 1 {
		fs.Usage()
		return exitcodes.Wrapf(nil, exitcodes.ExitNoInput, exitcodes.KindValidation, "split requires one PDF and --at")
	}
	cfg, err := resolveConfig(fs, ocrLang, vault, archive, db)
	if err != nil {
		return err
	}
	input, err := filepath.Abs(fs.Args()[0])
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitData, exitcodes.KindValidation, "invalid split input")
	}
	outDir := *outputDir
	if outDir == "" {
		outDir = strings.TrimSuffix(input, filepath.Ext(input)) + ".parts"
	}
	outDir, err = filepath.Abs(outDir)
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitData, exitcodes.KindValidation, "invalid split output directory")
	}
	outputs, err := pdfops.DefaultTools().Split(context.Background(), input, *at, outDir)
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal, "PDF split failed")
	}
	for _, path := range outputs {
		fmt.Fprintf(stdout, "created: %s\n", path)
	}
	if *ingestFlag {
		notes, err := ingestDerivedPDFs(cfg, outputs)
		if err != nil {
			return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal, "generated PDF ingest failed")
		}
		for _, note := range notes {
			fmt.Fprintf(stdout, "ingested note: %s\n", note)
		}
	}
	return nil
}

func runPDFMerge(args []string) error {
	fs := flag.NewFlagSet("merge", flag.ContinueOnError)
	output := fs.String("output", "", "Output PDF path")
	ingestFlag := fs.Bool("ingest", false, "Run the generated PDF through the normal ingest pipeline")
	ocrLang, vault, archive, db := registerSharedFlags(fs)
	configureUsage(fs, "merge [flags] <file>...", "Merge two or more PDFs. Requires Poppler pdfunite.")
	help, err := parseFlags(fs, args, "invalid merge flags")
	if help || err != nil {
		return err
	}
	if *output == "" || len(fs.Args()) < 2 {
		fs.Usage()
		return exitcodes.Wrapf(nil, exitcodes.ExitNoInput, exitcodes.KindValidation, "merge requires --output and at least two input PDFs")
	}
	cfg, err := resolveConfig(fs, ocrLang, vault, archive, db)
	if err != nil {
		return err
	}
	inputs := make([]string, len(fs.Args()))
	for i, input := range fs.Args() {
		inputs[i], err = filepath.Abs(input)
		if err != nil {
			return exitcodes.Wrap(err, exitcodes.ExitData, exitcodes.KindValidation, "invalid merge input")
		}
	}
	outputPath, err := filepath.Abs(*output)
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitData, exitcodes.KindValidation, "invalid merge output")
	}
	if err := pdfops.DefaultTools().Merge(context.Background(), inputs, outputPath); err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal, "PDF merge failed")
	}
	fmt.Fprintf(stdout, "created: %s\n", outputPath)
	if *ingestFlag {
		notes, err := ingestDerivedPDFs(cfg, []string{outputPath})
		if err != nil {
			return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal, "generated PDF ingest failed")
		}
		fmt.Fprintf(stdout, "ingested note: %s\n", notes[0])
	}
	return nil
}

func runPDFRotate(args []string) error {
	fs := flag.NewFlagSet("rotate", flag.ContinueOnError)
	degrees := fs.Int("degrees", 0, "Rotation in degrees: -270, -180, -90, 90, 180 or 270")
	pages := fs.String("pages", "", "Optional pages to rotate, e.g. 1,3-4 (default: all pages)")
	output := fs.String("output", "", "Output PDF path")
	ingestFlag := fs.Bool("ingest", false, "Run the generated PDF through the normal ingest pipeline")
	ocrLang, vault, archive, db := registerSharedFlags(fs)
	configureUsage(fs, "rotate [flags] <file>", "Rotate selected PDF pages. Requires qpdf; the original is never modified.")
	help, err := parseFlags(fs, args, "invalid rotate flags")
	if help || err != nil {
		return err
	}
	if *output == "" || len(fs.Args()) != 1 {
		fs.Usage()
		return exitcodes.Wrapf(nil, exitcodes.ExitNoInput, exitcodes.KindValidation, "rotate requires one input PDF and --output")
	}
	cfg, err := resolveConfig(fs, ocrLang, vault, archive, db)
	if err != nil {
		return err
	}
	input, err := filepath.Abs(fs.Args()[0])
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitData, exitcodes.KindValidation, "invalid rotate input")
	}
	outputPath, err := filepath.Abs(*output)
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitData, exitcodes.KindValidation, "invalid rotate output")
	}
	if err := pdfops.DefaultTools().Rotate(context.Background(), input, outputPath, *degrees, *pages); err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal, "PDF rotation failed")
	}
	fmt.Fprintf(stdout, "created: %s\n", outputPath)
	if *ingestFlag {
		notes, err := ingestDerivedPDFs(cfg, []string{outputPath})
		if err != nil {
			return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal, "generated PDF ingest failed")
		}
		fmt.Fprintf(stdout, "ingested note: %s\n", notes[0])
	}
	return nil
}
