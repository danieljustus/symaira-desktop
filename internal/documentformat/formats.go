// Package documentformat defines the document-format contract shared by ingest
// and retrieval. Keep this registry limited to formats both stacks can extract
// without optional runtimes or platform-specific tools.
package documentformat

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Kind is the normalized MIME-like identity used by both extraction stacks.
type Kind string

const (
	KindPDF      Kind = "application/pdf"
	KindText     Kind = "text/plain"
	KindCSV      Kind = "text/csv"
	KindMarkdown Kind = "text/markdown"
	KindHTML     Kind = "text/html"
	KindRTF      Kind = "application/rtf"
	KindDOCX     Kind = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	KindXLSX     Kind = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	KindPPTX     Kind = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	KindODT      Kind = "application/vnd.oasis.opendocument.text"
	KindODS      Kind = "application/vnd.oasis.opendocument.spreadsheet"
	KindODP      Kind = "application/vnd.oasis.opendocument.presentation"
	KindEPUB     Kind = "application/epub+zip"

	// These kinds are recognized so callers can report an actionable reason;
	// they are deliberately not in SupportedFormats.
	KindMOBI    Kind = "application/x-mobipocket-ebook"
	KindAZW3    Kind = "application/vnd.amazon.mobi8-ebook"
	KindPages   Kind = "application/vnd.apple.pages"
	KindKeynote Kind = "application/vnd.apple.keynote"
	KindNumbers Kind = "application/vnd.apple.numbers"
	KindDOC     Kind = "application/msword"
	KindXLS     Kind = "application/vnd.ms-excel"
	KindPPT     Kind = "application/vnd.ms-powerpoint"
	KindDjVu    Kind = "image/vnd.djvu"
	KindODG     Kind = "application/vnd.oasis.opendocument.graphics"
)

// Format describes one extension in the shared extraction contract.
type Format struct {
	Extension string
	Kind      Kind
	Name      string
}

// SupportedFormats is the single source of truth for formats extractable by
// both ingest and retrieval. Multi-extension aliases are separate rows so
// callers can compare the registry directly with filesystem extensions.
var SupportedFormats = []Format{
	{Extension: ".pdf", Kind: KindPDF, Name: "PDF"},
	{Extension: ".txt", Kind: KindText, Name: "plain text"},
	{Extension: ".text", Kind: KindText, Name: "plain text"},
	{Extension: ".csv", Kind: KindCSV, Name: "CSV"},
	{Extension: ".md", Kind: KindMarkdown, Name: "Markdown"},
	{Extension: ".markdown", Kind: KindMarkdown, Name: "Markdown"},
	{Extension: ".html", Kind: KindHTML, Name: "HTML"},
	{Extension: ".htm", Kind: KindHTML, Name: "HTML"},
	{Extension: ".rtf", Kind: KindRTF, Name: "RTF"},
	{Extension: ".docx", Kind: KindDOCX, Name: "Office Open XML document"},
	{Extension: ".xlsx", Kind: KindXLSX, Name: "Office Open XML spreadsheet"},
	{Extension: ".pptx", Kind: KindPPTX, Name: "Office Open XML presentation"},
	{Extension: ".odt", Kind: KindODT, Name: "OpenDocument text"},
	{Extension: ".ods", Kind: KindODS, Name: "OpenDocument spreadsheet"},
	{Extension: ".odp", Kind: KindODP, Name: "OpenDocument presentation"},
	{Extension: ".epub", Kind: KindEPUB, Name: "EPUB"},
}

// UnsupportedFormats lists recognized document extensions for which this
// repository has no genuine parser. The reason is part of the user-visible
// contract; callers must not silently fall back to binary-as-text handling.
var UnsupportedFormats = []struct {
	Format
	Reason string
}{
	{Format: Format{Extension: ".mobi", Kind: KindMOBI, Name: "MOBI"}, Reason: "no bundled MOBI parser; DRM status cannot be determined"},
	{Format: Format{Extension: ".azw3", Kind: KindAZW3, Name: "AZW3"}, Reason: "no bundled AZW3 parser; DRM status cannot be determined"},
	{Format: Format{Extension: ".pages", Kind: KindPages, Name: "iWork Pages"}, Reason: "iWork bundle parser is not available"},
	{Format: Format{Extension: ".key", Kind: KindKeynote, Name: "iWork Keynote"}, Reason: "iWork bundle parser is not available"},
	{Format: Format{Extension: ".numbers", Kind: KindNumbers, Name: "iWork Numbers"}, Reason: "iWork bundle parser is not available"},
	{Format: Format{Extension: ".doc", Kind: KindDOC, Name: "legacy Word"}, Reason: "legacy binary Office parser is not available"},
	{Format: Format{Extension: ".xls", Kind: KindXLS, Name: "legacy Excel"}, Reason: "legacy binary Office parser is not available"},
	{Format: Format{Extension: ".ppt", Kind: KindPPT, Name: "legacy PowerPoint"}, Reason: "legacy binary Office parser is not available"},
	{Format: Format{Extension: ".djvu", Kind: KindDjVu, Name: "DjVu"}, Reason: "DjVu parser is not available"},
	{Format: Format{Extension: ".odg", Kind: KindODG, Name: "OpenDocument drawing"}, Reason: "OpenDocument drawing parser is not available"},
}

var supportedByExtension = makeSupportedIndex()
var unsupportedByExtension = makeUnsupportedIndex()

func makeSupportedIndex() map[string]Format {
	result := make(map[string]Format, len(SupportedFormats))
	for _, format := range SupportedFormats {
		result[format.Extension] = format
	}
	return result
}

func makeUnsupportedIndex() map[string]struct {
	Format
	Reason string
} {
	result := make(map[string]struct {
		Format
		Reason string
	}, len(UnsupportedFormats))
	for _, format := range UnsupportedFormats {
		result[format.Extension] = format
	}
	return result
}

// NormalizeExtension lowercases and ensures an extension starts with a dot.
func NormalizeExtension(ext string) string {
	ext = strings.ToLower(strings.TrimSpace(ext))
	if ext != "" && !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	return ext
}

// Lookup returns the shared supported format for ext.
func Lookup(ext string) (Format, bool) {
	format, ok := supportedByExtension[NormalizeExtension(ext)]
	return format, ok
}

// KindForExtension returns the shared kind for supported or explicitly
// unsupported document formats. The bool reports whether ext is recognized.
func KindForExtension(ext string) (Kind, bool) {
	ext = NormalizeExtension(ext)
	if format, ok := supportedByExtension[ext]; ok {
		return format.Kind, true
	}
	if format, ok := unsupportedByExtension[ext]; ok {
		return format.Kind, true
	}
	return "", false
}

// IsSupported reports whether ext has a real parser in both stacks.
func IsSupported(ext string) bool {
	_, ok := Lookup(ext)
	return ok
}

// UnsupportedReason returns the explicit reason for a recognized but blocked
// format. The second return value distinguishes it from an unknown extension.
func UnsupportedReason(ext string) (string, bool) {
	format, ok := unsupportedByExtension[NormalizeExtension(ext)]
	if !ok {
		return "", false
	}
	return format.Reason, true
}

// IsRecognizedDocument reports whether ext belongs to either side of the
// shared format contract.
func IsRecognizedDocument(ext string) bool {
	return IsSupported(ext) || IsUnsupported(ext)
}

// IsUnsupported reports whether ext is recognized but intentionally blocked.
func IsUnsupported(ext string) bool {
	_, ok := unsupportedByExtension[NormalizeExtension(ext)]
	return ok
}

// SupportedExtensions returns a sorted copy suitable for diagnostics and
// deterministic tests.
func SupportedExtensions() []string {
	result := make([]string, 0, len(SupportedFormats))
	for _, format := range SupportedFormats {
		result = append(result, format.Extension)
	}
	sort.Strings(result)
	return result
}

// ErrDRMProtected identifies content that must not be decrypted by these
// local extractors. It is shared so both stacks can classify it consistently.
var ErrDRMProtected = errors.New("document is DRM-protected")

// IsUnsupportedKind reports whether kind identifies a format that is
// recognized but intentionally blocked.
func IsUnsupportedKind(kind Kind) bool {
	for _, format := range UnsupportedFormats {
		if format.Kind == kind {
			return true
		}
	}
	return false
}

// UnsupportedFormatError is the shared error shape used by ingest callers.
func UnsupportedFormatError(kind Kind) error {
	for _, format := range UnsupportedFormats {
		if format.Kind == kind {
			return fmt.Errorf("unsupported document format %s: %s", format.Name, format.Reason)
		}
	}
	return fmt.Errorf("unsupported document format %q", kind)
}
