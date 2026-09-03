package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/danieljustus/symaira-desktop/internal/export"
	"github.com/danieljustus/symaira-desktop/internal/pdf"
)

// ExportResult is the service return value for an export operation.
type ExportResult struct {
	Format   string `json:"format"`
	Path     string `json:"path"`
	Profile  string `json:"profile,omitempty"`
	Rendered bool   `json:"rendered"`
	Message  string `json:"message,omitempty"`
}

// ExportProfile is one selectable symprint output profile.
type ExportProfile struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Stability   string `json:"stability"`
}

// ExportProfiles returns the PDF profiles a caller may pass to Export, in
// presentation order. The list comes from print/ itself, so a picker built on
// it cannot drift from the renderer. It is a package function rather than a
// method because the profile set is a property of the renderer, not of a
// vault — listing it must not require an opened vault.
func ExportProfiles() []ExportProfile {
	all := pdf.Profiles()
	out := make([]ExportProfile, 0, len(all))
	for _, p := range all {
		out = append(out, ExportProfile{
			Name:        p.Name,
			Title:       p.Title,
			Description: p.Description,
			Stability:   p.Stability,
		})
	}
	return out
}

// Export renders a note or view to PDF or HTML.
// For notes, relPath is the vault-relative path. For views, viewID is the view id.
// Exactly one of relPath or viewID should be set.
func (s *Service) Export(relPath, viewID, outputPath, format, profile string) (*ExportResult, error) {
	format = strings.ToLower(format)
	if format != "pdf" && format != "html" && format != "csv" {
		return nil, fmt.Errorf("unsupported export format %q: use pdf, html, or csv", format)
	}

	if format == "csv" {
		if viewID == "" {
			return nil, fmt.Errorf("csv export is only supported for views (use --view)")
		}
		csvData, err := s.ViewsExportCSV(viewID)
		if err != nil {
			return nil, err
		}
		if outputPath == "" {
			outputPath = defaultOutputPath(relPath, viewID, format)
		}
		outputPath = filepath.Clean(outputPath)
		if !filepath.IsAbs(outputPath) {
			cwd, err := os.Getwd()
			if err != nil {
				return nil, fmt.Errorf("cannot resolve cwd: %w", err)
			}
			outputPath = filepath.Join(cwd, outputPath)
		}
		if err := os.WriteFile(outputPath, csvData, 0o600); err != nil {
			return nil, fmt.Errorf("failed to write CSV: %w", err)
		}
		return &ExportResult{
			Format:   "csv",
			Path:     outputPath,
			Rendered: true,
			Message:  fmt.Sprintf("Exported view %s to CSV", viewID),
		}, nil
	}

	opts := export.Options{Format: format, Profile: profile}
	var rendered []byte
	var err error
	var sourceDesc string

	if viewID != "" {
		view, err := s.ViewsGet(viewID)
		if err != nil {
			return nil, fmt.Errorf("view not found: %w", err)
		}
		rows, err := s.ViewsExec(viewID)
		if err != nil {
			return nil, fmt.Errorf("failed to evaluate view: %w", err)
		}
		stringRows := make([]map[string]string, len(rows))
		for i, row := range rows {
			stringRow := make(map[string]string, len(row))
			for k, v := range row {
				stringRow[k] = fmt.Sprintf("%v", v)
			}
			stringRows[i] = stringRow
		}
		rendered, err = export.View(s.VaultRoot, view, stringRows, opts)
		if err != nil {
			return nil, err
		}
		sourceDesc = "view " + view.Name
	} else if relPath != "" {
		rendered, err = export.Note(s.VaultRoot, relPath, opts)
		if err != nil {
			return nil, err
		}
		sourceDesc = "note " + relPath
	} else {
		return nil, fmt.Errorf("export requires --note or --view")
	}

	if outputPath == "" {
		outputPath = defaultOutputPath(relPath, viewID, format)
	}
	outputPath = filepath.Clean(outputPath)
	if !filepath.IsAbs(outputPath) {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("cannot resolve cwd: %w", err)
		}
		outputPath = filepath.Join(cwd, outputPath)
	}

	if format == "pdf" {
		// The renderer is in-process; only the typesetting engine is an
		// external requirement, so that is the one thing worth probing.
		if ok, hint := pdf.EngineAvailable(); !ok {
			return nil, fmt.Errorf("PDF export requires a typesetting engine: %s", hint)
		}
		var sourceDir string
		if relPath != "" {
			sourceDir = filepath.Dir(filepath.Join(s.VaultRoot, relPath))
		} else if viewID != "" {
			sourceDir = s.VaultRoot
		}
		res, err := pdf.Render(rendered, outputPath, profile, sourceDir)
		if err != nil {
			return nil, err
		}
		return &ExportResult{
			Format:   "pdf",
			Path:     res.OutputPath,
			Profile:  res.Profile,
			Rendered: true,
			Message:  fmt.Sprintf("Exported %s to PDF (profile %s, typst %s)", sourceDesc, res.Profile, res.EngineVersion),
		}, nil
	}

	if err := os.WriteFile(outputPath, rendered, 0644); err != nil { //nolint:gosec // exported HTML preserves the established user-readable mode
		return nil, fmt.Errorf("failed to write HTML: %w", err)
	}
	return &ExportResult{
		Format:   "html",
		Path:     outputPath,
		Rendered: true,
		Message:  fmt.Sprintf("Exported %s to HTML", sourceDesc),
	}, nil
}

func defaultOutputPath(relPath, viewID, format string) string {
	base := "export"
	if relPath != "" {
		base = strings.TrimSuffix(filepath.Base(relPath), filepath.Ext(relPath))
	} else if viewID != "" {
		base = strings.TrimSuffix(filepath.Base(viewID), filepath.Ext(viewID))
	}
	if format == "pdf" {
		return base + ".pdf"
	}
	if format == "csv" {
		return base + ".csv"
	}
	return base + ".html"
}
