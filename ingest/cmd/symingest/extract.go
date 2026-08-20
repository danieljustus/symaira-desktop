package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"path/filepath"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-ingest/internal/annotate"
	"github.com/danieljustus/symaira-ingest/internal/extract"
	"github.com/danieljustus/symaira-ingest/internal/ocr"
)

func runExtract(args []string) error {
	fs := flag.NewFlagSet("extract", flag.ContinueOnError)
	profile := fs.String("profile", "generic", "Extraction profile (generic, invoice, contract, jobcenter)")
	jsonFlag := fs.Bool("json", false, "Output extractions as JSONL")
	configureUsage(fs, "extract [flags] <file>", "Extract structured data from a file using a deterministic regex/rule-based profile.")
	help, err := parseFlags(fs, args, "invalid extract flags")
	if help || err != nil {
		return err
	}
	remaining := fs.Args()
	if len(remaining) == 0 {
		fs.Usage()
		return nil
	}

	source, err := filepath.Abs(remaining[0])
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitData, exitcodes.KindValidation,
			"invalid source path")
	}

	profileObj, err := annotate.GetProfile(*profile)
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitData, exitcodes.KindValidation,
			fmt.Sprintf("unknown profile %q", *profile))
	}

	kind, err := extract.Detect(source)
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitData, exitcodes.KindValidation,
			fmt.Sprintf("cannot detect file type: %v", err))
	}

	var extractRes *extract.Result
	switch kind {
	case extract.KindText, extract.KindMarkdown, extract.KindCSV:
		extractRes, err = extract.ReadTextKind(context.Background(), source, kind)
	case extract.KindHTML, extract.KindRTF, extract.KindDOCX, extract.KindXLSX, extract.KindODT, extract.KindEML:
		extractRes, err = extract.ReadStructuredKind(context.Background(), source, kind)
	default:
		engine := ocr.DefaultRunner("eng")
		extractRes, err = engine.Extract(context.Background(), source, kind)
	}
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal,
			"extraction failed")
	}

	extractions := annotate.Extract(profileObj, extractRes.Text)

	if *jsonFlag {
		enc := json.NewEncoder(stdout)
		for _, e := range extractions {
			if err := enc.Encode(e); err != nil {
				return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal,
					"failed to encode extraction")
			}
		}
		if len(extractions) == 0 {
			fmt.Fprintln(stdout, "[]")
		}
		return nil
	}

	fmt.Fprintf(stdout, "Profile: %s\nFile: %s\nText length: %d\nExtractions: %d\n\n",
		*profile, source, len(extractRes.Text), len(extractions))
	for _, e := range extractions {
		spanInfo := ""
		if e.Span != nil {
			spanInfo = fmt.Sprintf(" [%d:%d]", e.Span.Start, e.Span.End)
		}
		fmt.Fprintf(stdout, "  %-20s %s%s\n", e.Field+":", e.Value, spanInfo)
	}
	return nil
}
