package press

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// StringList unmarshals a YAML scalar OR sequence into a []string, so frontmatter
// may write `author: Jane` or `author: [Jane, John]` interchangeably.
type StringList []string

// UnmarshalYAML accepts a scalar or a sequence of scalars.
func (s *StringList) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		if value.Value == "" {
			*s = nil
			return nil
		}
		*s = StringList{value.Value}
		return nil
	case yaml.SequenceNode:
		var out []string
		if err := value.Decode(&out); err != nil {
			return err
		}
		*s = out
		return nil
	default:
		return fmt.Errorf("expected a string or list of strings, got %s", value.Tag)
	}
}

// Address is a DIN 5008 sender/recipient block.
type Address struct {
	Name    string     `yaml:"name" json:"name"`
	Address StringList `yaml:"address" json:"address,omitempty"`
}

// PDFOptions controls archival / accessibility output.
type PDFOptions struct {
	// Standard holds typst --pdf-standard identifiers, e.g. ["a-2a","ua-1"].
	Standard StringList `yaml:"standard" json:"standard,omitempty"`
	// Reproducible overrides the profile/config reproducibility default.
	Reproducible *bool `yaml:"reproducible" json:"reproducible,omitempty"`
}

// Frontmatter is the semantic metadata contract parsed from a document's leading
// YAML block. It holds meaning, never layout — presentation lives in the Profile.
// Unknown keys are rejected at parse time (KnownFields), so typos fail loudly.
type Frontmatter struct {
	// Selection
	Profile string `yaml:"profile" json:"profile"`

	// Universal metadata
	Lang     string     `yaml:"lang" json:"lang"`
	Title    string     `yaml:"title" json:"title"`
	Subtitle string     `yaml:"subtitle" json:"subtitle"`
	Author   StringList `yaml:"author" json:"author,omitempty"`
	Date     string     `yaml:"date" json:"date"`
	Keywords StringList `yaml:"keywords" json:"keywords,omitempty"`

	// Letter fields (brief / behoerde)
	Form      string            `yaml:"form" json:"form"`
	Sender    *Address          `yaml:"sender" json:"sender,omitempty"`
	Recipient *Address          `yaml:"recipient" json:"recipient,omitempty"`
	Betreff   string            `yaml:"betreff" json:"betreff"`
	Infoblock map[string]string `yaml:"infoblock" json:"infoblock,omitempty"`

	// Report fields (report)
	Titlepage   *bool  `yaml:"titlepage" json:"titlepage,omitempty"`
	TOC         *bool  `yaml:"toc" json:"toc,omitempty"`
	TOCDepth    int    `yaml:"toc-depth" json:"toc_depth,omitempty"`
	HeaderLeft  string `yaml:"header-left" json:"header_left"`
	HeaderRight string `yaml:"header-right" json:"header_right"`
	FooterLeft  string `yaml:"footer-left" json:"footer_left"`
	FooterRight string `yaml:"footer-right" json:"footer_right"`

	// Data-driven fields (rechnung and future data profiles)
	Data map[string]any `yaml:"data" json:"data,omitempty"`

	// Meeting fields (meeting)
	MeetingID    string     `yaml:"meeting_id" json:"meeting_id,omitempty"`
	Participants StringList `yaml:"participants" json:"participants,omitempty"`
	Duration     string     `yaml:"duration" json:"duration,omitempty"`
	Location     string     `yaml:"location" json:"location,omitempty"`

	// Output
	PDF PDFOptions `yaml:"pdf" json:"pdf"`
}

// Document is a parsed source: its frontmatter and its Markdown body.
type Document struct {
	Front Frontmatter `json:"front"`
	Body  []byte      `json:"-"`
}

var fmOpen = []byte("---\n")

// Split separates a leading YAML frontmatter block (delimited by `---` lines)
// from the Markdown body. A document without an opening delimiter yields empty
// fm and the whole input as body. CRLF is normalized to LF.
func Split(src []byte) (fm, body []byte) {
	norm := bytes.ReplaceAll(src, []byte("\r\n"), []byte("\n"))
	if !bytes.HasPrefix(norm, fmOpen) {
		return nil, src
	}
	lines := strings.Split(string(norm[len(fmOpen):]), "\n")
	for i, ln := range lines {
		if ln == "---" || ln == "..." {
			fmStr := strings.Join(lines[:i], "\n")
			bodyStr := strings.Join(lines[i+1:], "\n")
			bodyStr = strings.TrimPrefix(bodyStr, "\n")
			return []byte(fmStr), []byte(bodyStr)
		}
	}
	// No closing delimiter — treat the whole input as body (malformed header).
	return nil, src
}

// Parse splits and decodes a source document. Unknown frontmatter keys are an
// error (the contract is strict so silent typos can't change output).
func Parse(src []byte) (*Document, error) {
	fmBytes, body := Split(src)
	doc := &Document{Body: body}
	if len(bytes.TrimSpace(fmBytes)) == 0 {
		return doc, nil
	}
	dec := yaml.NewDecoder(bytes.NewReader(fmBytes))
	dec.KnownFields(true)
	if err := dec.Decode(&doc.Front); err != nil {
		return nil, &ContractError{Reason: "invalid frontmatter", Detail: cleanYAMLError(err)}
	}
	return doc, nil
}

var dinDate = regexp.MustCompile(`^(\d{2}\.\d{2}\.\d{4}|\d{4}-\d{2}-\d{2})$`)

// Validate checks a parsed document against the rules of a profile and returns
// every issue found (errors and warnings). It never renders.
func (d *Document) Validate(p Profile) []Issue {
	var issues []Issue
	add := func(sev, field, msg string) { issues = append(issues, Issue{sev, field, msg}) }

	for _, f := range p.RequiredFields {
		if d.fieldEmpty(f) {
			add("error", f, fmt.Sprintf("profile %q requires %q", p.Name, f))
		}
	}

	if d.Front.Date != "" && !dinDate.MatchString(d.Front.Date) {
		add("error", "date", "must be TT.MM.JJJJ or JJJJ-MM-TT with leading zeros (DIN 5008)")
	}

	if d.Front.Form != "" && d.Front.Form != "A" && d.Front.Form != "B" {
		add("error", "form", `DIN 5008 form must be "A" or "B"`)
	}

	if p.RequiresAccessibility() {
		if d.Front.Lang == "" {
			add("error", "lang", "PDF/UA requires a document language (e.g. lang: de)")
		}
		if d.Front.Title == "" {
			add("error", "title", "PDF/UA requires a document title in metadata")
		}
	}

	return issues
}

func (d *Document) fieldEmpty(field string) bool {
	switch field {
	case "title":
		return d.Front.Title == ""
	case "lang":
		return d.Front.Lang == ""
	case "betreff":
		return d.Front.Betreff == ""
	case "date":
		return d.Front.Date == ""
	case "author":
		return len(d.Front.Author) == 0
	case "recipient":
		return d.Front.Recipient == nil || d.Front.Recipient.Name == ""
	case "sender":
		return d.Front.Sender == nil || d.Front.Sender.Name == ""
	case "data":
		return len(d.Front.Data) == 0
	default:
		return false
	}
}

// cleanYAMLError trims yaml.v3's boilerplate into a single readable line.
func cleanYAMLError(err error) string {
	msg := err.Error()
	msg = strings.TrimPrefix(msg, "yaml: unmarshal errors:\n")
	msg = strings.ReplaceAll(msg, "\n  ", "; ")
	msg = strings.TrimPrefix(msg, "yaml: ")
	return strings.TrimSpace(msg)
}
