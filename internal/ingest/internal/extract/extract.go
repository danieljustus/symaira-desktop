// Package extract detects source-file types and extracts text.
//
// # Recovery policy
//
// Structured readers return an error when a container part is missing,
// malformed, or over the fixed archive limits (see limits.go); the pipeline
// quarantines such documents instead of aborting the batch. Inputs that parse
// successfully but contain nothing extractable yield empty text without an
// error, and readers that can salvage partial content return it. The corpus
// in internal/extract/testdata/malformed encodes this contract in its file
// names (<name>--<outcome>.<ext> with outcomes errors, recovers, skips); the
// fuzz target over ReadStructuredKind keeps the readers panic-free on
// arbitrary bytes.
package extract

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/danieljustus/symaira-desktop/internal/documentformat"
	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/filetype"
)

// Kind is kept as an alias for compatibility with the ingest API. The values
// are owned by the shared document-format contract.
type Kind = documentformat.Kind

const (
	KindPDF           = documentformat.KindPDF
	KindPNG      Kind = "image/png"
	KindJPEG     Kind = "image/jpeg"
	KindTIFF     Kind = "image/tiff"
	KindWebP     Kind = "image/webp"
	KindHEIC     Kind = "image/heic"
	KindText          = documentformat.KindText
	KindCSV           = documentformat.KindCSV
	KindMarkdown      = documentformat.KindMarkdown
	KindHTML          = documentformat.KindHTML
	KindRTF           = documentformat.KindRTF
	KindDOCX          = documentformat.KindDOCX
	KindXLSX          = documentformat.KindXLSX
	KindPPTX          = documentformat.KindPPTX
	KindODT           = documentformat.KindODT
	KindODS           = documentformat.KindODS
	KindODP           = documentformat.KindODP
	KindEPUB          = documentformat.KindEPUB
	KindEML      Kind = "message/rfc822"
	KindMOBI          = documentformat.KindMOBI
	KindAZW3          = documentformat.KindAZW3
	KindPages         = documentformat.KindPages
	KindKeynote       = documentformat.KindKeynote
	KindNumbers       = documentformat.KindNumbers
	KindDOC           = documentformat.KindDOC
	KindXLS           = documentformat.KindXLS
	KindPPT           = documentformat.KindPPT
	KindDjVu          = documentformat.KindDjVu
	KindUnknown  Kind = ""
)

// Result holds extracted text and metadata.
type Result struct {
	Text   string
	MIME   string
	Engine string
}

// Engine extracts text from a file.
type Engine interface {
	Extract(ctx context.Context, path string, kind Kind) (*Result, error)
}

// Detect identifies the kind of file at path using magic bytes and extension fallback.
// Container formats (ZIP-family and OLE) are resolved from the package's own
// identity — the mimetype part, the OPC officeDocument relationship, or the
// mandated OLE stream names — before the extension is consulted, so renamed
// or extension-less packages are classified by content. An unrecognized
// container yields KindUnknown without an error, letting the caller fall
// back. When the optional magika CLI (google/magika) is installed, its result
// is compared against the detected kind and mismatches are logged as warnings
// to stderr. The magika result never overrides the detected kind.
func Detect(path string) (Kind, error) {
	f, err := os.Open(path)
	if err != nil {
		return KindUnknown, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	buf := make([]byte, 512)
	n, err := f.Read(buf)
	if err != nil && err.Error() != "EOF" {
		return KindUnknown, fmt.Errorf("read file: %w", err)
	}
	head := buf[:n]

	var kind Kind

	switch {
	case len(head) >= 4 && bytes.Equal(head[:4], []byte("%PDF")):
		kind = KindPDF
	case len(head) >= 8 && bytes.Equal(head[:8], []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}):
		kind = KindPNG
	case len(head) >= 3 && bytes.Equal(head[:3], []byte{0xFF, 0xD8, 0xFF}):
		kind = KindJPEG
	case len(head) >= 4 && (bytes.Equal(head[:4], []byte("II*\x00")) || bytes.Equal(head[:4], []byte("MM\x00*"))):
		kind = KindTIFF
	case len(head) >= 12 && bytes.Equal(head[:4], []byte("RIFF")) && bytes.Equal(head[8:12], []byte("WEBP")):
		kind = KindWebP
	case len(head) >= 12 && bytes.Equal(head[4:8], []byte("ftyp")) && isHEIFBrand(string(head[8:12])):
		kind = KindHEIC
	}

	if kind == "" {
		// Containers are resolved from the package's own identity; the
		// extension is never trusted for a file whose content claims to be
		// a container.
		switch {
		case bytes.HasPrefix(head, zipSignature):
			kind = detectZIPContainer(path)
		case bytes.HasPrefix(head, oleSignature):
			kind = detectOLEContainer(f, head)
		}
	}

	if kind == "" && !isContainerSignature(head) {
		ext := strings.ToLower(filepath.Ext(path))
		if detected, ok := documentformat.KindForExtension(ext); ok {
			kind = detected
		} else {
			switch ext {
			case ".eml":
				kind = KindEML
			case ".png":
				kind = KindPNG
			case ".jpg", ".jpeg":
				kind = KindJPEG
			case ".tiff", ".tif":
				kind = KindTIFF
			case ".webp":
				kind = KindWebP
			case ".heic", ".heif":
				kind = KindHEIC
			}
		}
	}

	if kind == "" {
		// A signature-carrying container that could not be resolved is
		// reported as unknown unless its extension is a recognized blocked
		// document format. The latter needs a visible reason rather than an
		// opaque "unknown container" result.
		if isContainerSignature(head) {
			if detected, ok := documentformat.KindForExtension(filepath.Ext(path)); ok && documentformat.IsUnsupportedKind(detected) {
				return detected, nil
			}
			return KindUnknown, nil
		}
		return KindUnknown, fmt.Errorf("unsupported file type: %s", path)
	}

	// When magika is installed, verify the extension-based guess and log
	// mismatches as warnings (advisory only, never overrides the detected kind).
	filetype.VerifyAgainstGuess(path, string(kind), func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
	})

	return kind, nil
}

// SupportedExtensions returns the shared format contract in deterministic order.
func SupportedExtensions() []string {
	return documentformat.SupportedExtensions()
}

func IsExplicitlyUnsupported(kind Kind) bool {
	return documentformat.IsUnsupportedKind(kind)
}

func UnsupportedFormatError(kind Kind) error {
	return documentformat.UnsupportedFormatError(kind)
}

func isHEIFBrand(brand string) bool {
	switch brand {
	case "heic", "heix", "hevc", "hevx", "heim", "heis", "mif1", "msf1":
		return true
	default:
		return false
	}
}

// ReadText reads plain text files directly.
func ReadText(ctx context.Context, path string) (*Result, error) {
	return ReadTextKind(ctx, path, KindText)
}

// ReadTextKind reads a text-like file directly while preserving its normalized MIME kind.
func ReadTextKind(ctx context.Context, path string, kind Kind) (*Result, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read text file: %w", err)
	}
	if kind == "" {
		kind = KindText
	}
	return &Result{Text: string(data), MIME: string(kind), Engine: "text"}, nil
}
