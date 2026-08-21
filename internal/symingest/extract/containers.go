package extract

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"os"
	"strings"
)

// Container signature prefixes inspected by Detect before the extension
// fallback. A file carrying one of these signatures whose format cannot be
// resolved is reported as unknown (no error) so callers can fall back,
// instead of being misdetected by a misleading extension.
var (
	zipSignature = []byte("PK\x03\x04")
	zipEmptySig  = []byte("PK\x05\x06")
	oleSignature = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}
)

// isContainerSignature reports whether head starts with a known container
// signature, making content authoritative over the file extension.
func isContainerSignature(head []byte) bool {
	return bytes.HasPrefix(head, zipSignature) ||
		bytes.HasPrefix(head, zipEmptySig) ||
		bytes.HasPrefix(head, oleSignature)
}

// detectZIPContainer resolves the format of a ZIP package from its own
// identity: the mandatory mimetype part for ODF/EPUB, the OPC package-level
// officeDocument relationship and its content type for OOXML, and finally the
// conventional main-part paths. An unresolvable archive yields "".
func detectZIPContainer(path string) Kind {
	a, err := openZip(path)
	if err != nil {
		return ""
	}
	defer closeZip(a)

	// ODF and EPUB carry a mandatory, authoritative mimetype part.
	if data, err := a.readPart("mimetype"); err == nil && data != nil {
		switch string(bytes.TrimSpace(data)) {
		case "application/vnd.oasis.opendocument.text":
			return KindODT
		case "application/vnd.oasis.opendocument.spreadsheet":
			return KindODS
		case "application/vnd.oasis.opendocument.presentation":
			return KindODP
		case "application/epub+zip":
			return KindEPUB
		}
	}

	// OPC packages resolve via the package-level officeDocument relationship.
	if data, err := a.readPart("_rels/.rels"); err == nil && data != nil {
		if mainPart := opcOfficeDocumentPart(data); mainPart != "" {
			if kind := kindFromOPCMainPart(mainPart); kind != "" {
				return kind
			}
		}
	}

	// Fall back to the [Content_Types].xml overrides for the mandated main
	// parts, then to the conventional part paths themselves.
	if data, err := a.readPart("[Content_Types].xml"); err == nil && data != nil {
		if kind := opcContentTypeKind(data); kind != "" {
			return kind
		}
	}
	switch {
	case a.find("word/document.xml") != nil:
		return KindDOCX
	case a.find("xl/workbook.xml") != nil:
		return KindXLSX
	case a.find("ppt/presentation.xml") != nil:
		return KindPPTX
	}
	return ""
}

// opcOfficeDocumentPart extracts the Target of the package-level
// officeDocument relationship from _rels/.rels.
func opcOfficeDocumentPart(relsData []byte) string {
	dec := xml.NewDecoder(bytes.NewReader(relsData))
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "Relationship" {
			continue
		}
		var relType, target string
		for _, attr := range start.Attr {
			switch attr.Name.Local {
			case "Type":
				relType = attr.Value
			case "Target":
				target = attr.Value
			}
		}
		if strings.HasSuffix(relType, "/officeDocument") && target != "" {
			return strings.TrimPrefix(target, "/")
		}
	}
}

// opcContentTypeKind resolves the kind from the [Content_Types].xml Override
// content types of the three OOXML main parts.
func opcContentTypeKind(ctData []byte) Kind {
	dec := xml.NewDecoder(bytes.NewReader(ctData))
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "Override" {
			continue
		}
		var contentType string
		for _, attr := range start.Attr {
			if attr.Name.Local == "ContentType" {
				contentType = attr.Value
				break
			}
		}
		switch {
		case strings.Contains(contentType, "wordprocessingml.document.main+xml"):
			return KindDOCX
		case strings.Contains(contentType, "spreadsheetml.sheet.main+xml"):
			return KindXLSX
		case strings.Contains(contentType, "presentationml.presentation.main+xml"):
			return KindPPTX
		}
	}
}

// kindFromOPCMainPart maps the conventional OOXML main-part path to a kind.
func kindFromOPCMainPart(mainPart string) Kind {
	switch mainPart {
	case "word/document.xml":
		return KindDOCX
	case "xl/workbook.xml":
		return KindXLSX
	case "ppt/presentation.xml":
		return KindPPTX
	}
	return ""
}

// oleMainStreamKinds maps the mandated OLE root-storage stream names to
// kinds. No legacy binary formats are part of the extraction surface yet, so
// every entry currently resolves to KindUnknown; this table is the single
// place to extend when .doc/.xls/.ppt land, and until then renamed OLE files
// are reported as unknown rather than guessed.
var oleMainStreamKinds = map[string]Kind{
	"WordDocument":        KindUnknown,
	"PowerPoint Document": KindUnknown,
	"Workbook":            KindUnknown,
	"Book":                KindUnknown,
}

// detectOLEContainer classifies an OLE compound file by the mandated stream
// names in its root storage. It reads the first directory sector through the
// already-open file and scans the 128-byte directory entries for the
// mandated names; anything else yields "".
func detectOLEContainer(f *os.File, head []byte) Kind {
	// Header layout: byte 30 holds the sector-shift exponent — the field
	// value IS the power of two (9 for 512-byte sectors, 12 for 4096-byte
	// sectors) — and bytes 48-51 the first directory sector index.
	if len(head) < 52 {
		return ""
	}
	sectorSize := 1 << head[30]
	dirStart := binary.LittleEndian.Uint32(head[48:52])
	dir := make([]byte, sectorSize)
	// Sector n of the file starts at (n+1) * sectorSize (sector 0 follows
	// the 512-byte header).
	if _, err := f.ReadAt(dir, int64(dirStart+1)*int64(sectorSize)); err != nil {
		return ""
	}
	for i := 0; i+127 < len(dir); i += 128 {
		nameLen := int(binary.LittleEndian.Uint16(dir[i+64 : i+66]))
		if nameLen < 2 || nameLen > 64 {
			continue
		}
		name := oleUTF16Name(dir[i : i+nameLen-2])
		if kind, ok := oleMainStreamKinds[name]; ok {
			return kind
		}
	}
	return ""
}

// oleUTF16Name decodes a UTF-16LE directory entry name.
func oleUTF16Name(b []byte) string {
	if len(b)%2 != 0 {
		b = b[:len(b)-1]
	}
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, binary.LittleEndian.Uint16(b[i:i+2]))
	}
	var sb strings.Builder
	for _, r := range u {
		sb.WriteRune(rune(r))
	}
	return sb.String()
}
