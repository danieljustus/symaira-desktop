package cli

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/danieljustus/symaira-relate/internal/app"
	importerdomain "github.com/danieljustus/symaira-relate/internal/domain/importer"
)

func init() {
	Register(&Command{
		Name:  "import",
		Short: "Import contacts from vCard or CSV, with a reviewed dedup workflow",
		Long: `Import contacts from vCard (3.0/4.0) or CSV files into the local database.

A dry run never writes; duplicate candidates (matched on contact points, names,
or a prior import of the same source) require an explicit --resolve flag.
Re-importing an unchanged source is idempotent — nothing is duplicated.`,
		Examples: `  symrelate import vcard --dry-run testdata/import/sample.vcf
  symrelate import vcard testdata/import/sample.vcf
  symrelate import csv --map name=Full Name,email=Email sample.csv --dry-run
  symrelate import runs`,
		Run: runImport,
	})
}

func runImport(ctx context.Context, iostreams IO, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: symrelate import <vcard|csv|runs> ...")
	}
	verb, rest := args[0], args[1:]

	switch verb {
	case "vcard":
		return importVCard(ctx, iostreams, rest)
	case "csv":
		return importCSV(ctx, iostreams, rest)
	case "runs":
		return importRuns(ctx, iostreams, rest)
	default:
		return fmt.Errorf("symrelate import: unknown subcommand %q", verb)
	}
}

// resolveFlags collects repeatable --resolve ROW=create|skip|merge:PERSONID
// flags into RowResolutions.
type resolveFlags []importerdomain.RowResolution

func (r *resolveFlags) String() string { return "" }

func (r *resolveFlags) Set(v string) error {
	rowStr, action, ok := strings.Cut(v, "=")
	if !ok {
		return fmt.Errorf("--resolve must be ROW=create|skip|merge:PERSONID, got %q", v)
	}
	row, err := strconv.Atoi(strings.TrimSpace(rowStr))
	if err != nil {
		return fmt.Errorf("--resolve: invalid row number %q: %w", rowStr, err)
	}

	res := importerdomain.RowResolution{RowNumber: row}
	switch {
	case action == string(importerdomain.ResolutionCreate):
		res.Resolution = importerdomain.ResolutionCreate
	case action == string(importerdomain.ResolutionSkip):
		res.Resolution = importerdomain.ResolutionSkip
	case strings.HasPrefix(action, "merge:"):
		res.Resolution = importerdomain.ResolutionMerge
		res.MergePersonID = strings.TrimPrefix(action, "merge:")
	default:
		return fmt.Errorf("--resolve: unknown action %q (want create, skip, or merge:PERSONID)", action)
	}
	*r = append(*r, res)
	return nil
}

func importVCard(ctx context.Context, iostreams IO, args []string) error {
	fs := flag.NewFlagSet("import vcard", flag.ContinueOnError)
	fs.SetOutput(iostreams.Stderr)
	dryRun := fs.Bool("dry-run", false, "compute and print the plan, write nothing")
	var resolves resolveFlags
	fs.Var(&resolves, "resolve", "ROW=create|skip|merge:PERSONID (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: symrelate import vcard [--dry-run] [--resolve ROW=...] <file>")
	}

	f, err := os.Open(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("failed to open vCard file: %w", err)
	}
	defer f.Close()

	rows, issues, err := importerdomain.ParseVCard(f)
	if err != nil {
		return err
	}
	return runImportPlan(ctx, iostreams, importerdomain.SourceVCard, rows, issues, *dryRun, resolves)
}

func importCSV(ctx context.Context, iostreams IO, args []string) error {
	fs := flag.NewFlagSet("import csv", flag.ContinueOnError)
	fs.SetOutput(iostreams.Stderr)
	dryRun := fs.Bool("dry-run", false, "compute and print the plan, write nothing")
	mapFlag := fs.String("map", "", "explicit column mapping: field=header,field=header,... (fields: name,given,family,org,title,email,phone,url); default: auto-detect from the header row")
	var resolves resolveFlags
	fs.Var(&resolves, "resolve", "ROW=create|skip|merge:PERSONID (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: symrelate import csv [--map field=header,...] [--dry-run] [--resolve ROW=...] <file>")
	}

	f, err := os.Open(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("failed to open CSV file: %w", err)
	}
	defer f.Close()

	mapping, header, err := csvMapping(f, *mapFlag)
	if err != nil {
		return err
	}
	fmt.Fprintf(iostreams.Stderr, "column mapping: %+v (header: %v)\n", mapping, header)

	rows, issues, err := importerdomain.ParseCSV(f, mapping)
	if err != nil {
		return err
	}
	return runImportPlan(ctx, iostreams, importerdomain.SourceCSV, rows, issues, *dryRun, resolves)
}

// csvMapping resolves the column mapping: an explicit --map flag if given,
// otherwise auto-detected from the file's header row. Detection opens its
// own handle on f's path to read just the header, so f itself is left at
// offset 0 for ParseCSV to do the real, quote-aware parse afterward.
func csvMapping(f *os.File, mapFlag string) (importerdomain.ColumnMapping, []string, error) {
	if mapFlag != "" {
		m, err := parseColumnMapping(mapFlag)
		return m, nil, err
	}

	header, err := peekCSVHeader(f.Name())
	if err != nil {
		return importerdomain.ColumnMapping{}, nil, err
	}
	return importerdomain.DetectColumnMapping(header), header, nil
}

func peekCSVHeader(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	header, err := csv.NewReader(f).Read()
	if err == io.EOF {
		return nil, fmt.Errorf("csv file is empty")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read CSV header: %w", err)
	}
	for i, h := range header {
		header[i] = strings.TrimSpace(h)
	}
	return header, nil
}

func parseColumnMapping(spec string) (importerdomain.ColumnMapping, error) {
	var m importerdomain.ColumnMapping
	for _, pair := range strings.Split(spec, ",") {
		field, header, ok := strings.Cut(pair, "=")
		if !ok {
			return m, fmt.Errorf("--map: invalid entry %q, want field=header", pair)
		}
		switch strings.TrimSpace(field) {
		case "name":
			m.DisplayName = header
		case "given":
			m.GivenName = header
		case "family":
			m.FamilyName = header
		case "org":
			m.Organization = header
		case "title":
			m.Title = header
		case "email":
			m.Email = header
		case "phone":
			m.Phone = header
		case "url":
			m.URL = header
		default:
			return m, fmt.Errorf("--map: unknown field %q", field)
		}
	}
	return m, nil
}

func runImportPlan(ctx context.Context, iostreams IO, source importerdomain.SourceKind, rows []importerdomain.ImportRow, issues []importerdomain.ValidationIssue, dryRun bool, resolves resolveFlags) error {
	a, err := app.Open(ctx)
	if err != nil {
		return err
	}
	defer a.Close()

	plan, err := a.Import.Plan(ctx, source, rows, issues)
	if err != nil {
		return err
	}

	if dryRun {
		return printJSON(iostreams, plan)
	}

	result, err := a.Import.Apply(ctx, plan, []importerdomain.RowResolution(resolves))
	if err != nil {
		return err
	}
	return printJSON(iostreams, struct {
		Plan   *importerdomain.ImportPlan `json:"plan"`
		Result any                        `json:"result"`
	}{Plan: plan, Result: result})
}

func importRuns(ctx context.Context, iostreams IO, args []string) error {
	a, err := app.Open(ctx)
	if err != nil {
		return err
	}
	defer a.Close()

	runs, err := a.Import.ListRuns(ctx)
	if err != nil {
		return err
	}
	return printJSON(iostreams, runs)
}
