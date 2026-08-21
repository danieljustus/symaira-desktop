package press

import "sort"

// Stability marks how production-ready a profile is.
const (
	StabilityStable   = "stable"
	StabilityBeta     = "beta"
	StabilityScaffold = "scaffold" // template present, geometry/compliance not yet validated
)

// Profile is a named use-case bundle: it fixes the template, engine options,
// page geometry defaults, and output guarantees. Selecting a profile is the one
// knob a document needs (`profile:` in frontmatter). Presentation lives here,
// never in the document.
type Profile struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`

	// KeyGuarantees summarizes the key capabilities/guarantees for help texts.
	KeyGuarantees string `json:"key_guarantees,omitempty"`

	// Template is the embedded Typst file used to render this profile.
	Template string `json:"template"`
	// Engine selects the rendering engine. Only "typst" exists today; the
	// field exists so future profiles can target weasyprint/maroto explicitly.
	Engine string `json:"engine"`

	// Form is the DIN 5008 default ("A", "B", or "" for non-letters).
	Form string `json:"form,omitempty"`
	// PDFStandard is the default typst --pdf-standard for this profile.
	PDFStandard []string `json:"pdf_standard,omitempty"`
	// Reproducible default for this profile.
	Reproducible bool `json:"reproducible"`

	// RequiredFields are frontmatter fields that must be present.
	RequiredFields []string `json:"required_fields,omitempty"`

	Stability string `json:"stability"`
}

// Capability summarizes the guarantees a profile provides, so the CLI/MCP layer
// and AI agents can reason about output without re-deriving it.
func (p Profile) Capability() Capability {
	return Capability{
		Markdown:     true,
		PDFA:         hasPrefix(p.PDFStandard, "a-"),
		PDFUA:        contains(p.PDFStandard, "ua-1") || contains(p.PDFStandard, "ua-2"),
		DINWindow:    p.Form != "",
		Reproducible: p.Reproducible,
	}
}

// RequiresAccessibility reports whether the profile emits a PDF/UA standard and
// therefore needs lang + title metadata to validate.
func (p Profile) RequiresAccessibility() bool {
	return contains(p.PDFStandard, "ua-1") || contains(p.PDFStandard, "ua-2")
}

// builtins is the registry of shipped profiles. Letter/invoice geometry is
// seeded from the DIN 5008 research (typst-letter-pro, briefs, classy-german-
// invoice) and is marked "scaffold" until validated with typst + veraPDF.
var builtins = map[string]Profile{
	"brief": {
		Name:           "brief",
		Title:          "Brief (DIN 5008)",
		Description:    "General German business letter. Defaults to DIN 5008 Form B (45 mm Briefkopf). Tagged PDF, no archival requirement.",
		KeyGuarantees:  "DIN 5008 letter (Form B)",
		Template:       "brief.typ",
		Engine:         "typst",
		Form:           "B",
		PDFStandard:    nil,
		Reproducible:   false,
		RequiredFields: []string{"recipient"},
		Stability:      StabilityScaffold,
	},
	"behoerde": {
		Name:           "behoerde",
		Title:          "Behörde (DIN 5008 + PDF/A + PDF/UA)",
		Description:    "Authority letter: DIN 5008 Form A plus archival (PDF/A-2a) and accessible (PDF/UA-1) output for E-Government / BITV 2.0 compliance.",
		KeyGuarantees:  "DIN 5008 authority letter (Form A) + PDF/A-2a + PDF/UA-1",
		Template:       "behoerde.typ",
		Engine:         "typst",
		Form:           "A",
		PDFStandard:    []string{"a-2a", "ua-1"},
		Reproducible:   false,
		RequiredFields: []string{"recipient", "title", "lang"},
		Stability:      StabilityScaffold,
	},
	"report": {
		Name:           "report",
		Title:          "Report / Bericht",
		Description:    "Multi-page report with cover page, automatic table of contents, running header/footer, page numbers, and themed headings.",
		KeyGuarantees:  "cover page, table of contents, headers/footers, page numbers",
		Template:       "report.typ",
		Engine:         "typst",
		Form:           "",
		PDFStandard:    nil,
		Reproducible:   false,
		RequiredFields: []string{"title"},
		Stability:      StabilityBeta,
	},
	"rechnung": {
		Name:           "rechnung",
		Title:          "Rechnung (DIN 5008)",
		Description:    "German invoice with windowed-envelope recipient, line items and VAT. Driven by a `data:` block. (Scaffold — VAT/GiroCode in Phase 4.)",
		KeyGuarantees:  "data-driven German invoice (scaffold)",
		Template:       "rechnung.typ",
		Engine:         "typst",
		Form:           "B",
		PDFStandard:    nil,
		Reproducible:   false,
		RequiredFields: []string{"recipient", "data"},
		Stability:      StabilityScaffold,
	},
	"meeting": {
		Name:           "meeting",
		Title:          "Meeting Minutes",
		Description:    "Accessible and archivable meeting minutes profile. Automatically formats metadata, participants, duration, location, and structured sections (Summary, Decisions, Action Items, Notes, Transcript).",
		KeyGuarantees:  "meeting minutes + PDF/A-2a + PDF/UA-1",
		Template:       "meeting.typ",
		Engine:         "typst",
		Form:           "",
		PDFStandard:    []string{"a-2a", "ua-1"},
		Reproducible:   false,
		RequiredFields: []string{"title", "lang", "date"},
		Stability:      StabilityBeta,
	},
}

// Lookup returns the built-in profile with the given name.
func Lookup(name string) (Profile, bool) {
	p, ok := builtins[name]
	return p, ok
}

// All returns the built-in profiles sorted by name.
func All() []Profile {
	out := make([]Profile, 0, len(builtins))
	for _, p := range builtins {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func hasPrefix(ss []string, prefix string) bool {
	for _, s := range ss {
		if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}
