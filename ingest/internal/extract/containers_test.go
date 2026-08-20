package extract

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

const (
	opcRelNS     = "http://schemas.openxmlformats.org/package/2006/relationships"
	wordMLNS     = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	smlNS        = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
	officeDocRel = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument"
)

// TestDetect_ZIPContentBeatsExtension: a DOCX package renamed to an unrelated
// extension is detected as KindDOCX from its content.
func TestDetect_ZIPContentBeatsExtension(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name  string
		ext   string
		files map[string]string
		want  Kind
	}{
		{
			"docx-renamed-txt",
			".txt",
			map[string]string{"word/document.xml": `<w:document xmlns:w="` + wordMLNS + `"/>`},
			KindDOCX,
		},
		{
			"xlsx-renamed-bin",
			".bin",
			map[string]string{"xl/workbook.xml": `<workbook xmlns="` + smlNS + `"/>`},
			KindXLSX,
		},
		{
			"pptx-renamed-jpg",
			".jpg",
			map[string]string{"ppt/presentation.xml": `<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"/>`},
			KindPPTX,
		},
		{
			"epub-no-extension",
			"",
			map[string]string{"mimetype": "application/epub+zip"},
			KindEPUB,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name+tc.ext)
			writeZip(t, path, tc.files)
			kind, err := Detect(path)
			if err != nil {
				t.Fatalf("Detect: %v", err)
			}
			if kind != tc.want {
				t.Fatalf("kind = %q, want %q", kind, tc.want)
			}
		})
	}
}

// TestDetect_ODFvsOOXMLDistinguished: an ODT and an OOXML package are told
// apart without consulting the extension.
func TestDetect_ODFvsOOXMLDistinguished(t *testing.T) {
	dir := t.TempDir()

	odtPath := filepath.Join(dir, "document.xlsx") // ODT content, xlsx extension
	writeZip(t, odtPath, map[string]string{"mimetype": "application/vnd.oasis.opendocument.text"})
	kind, err := Detect(odtPath)
	if err != nil {
		t.Fatalf("Detect odt: %v", err)
	}
	if kind != KindODT {
		t.Fatalf("ODT content with .xlsx extension: kind = %q, want %q", kind, KindODT)
	}

	xlsxPath := filepath.Join(dir, "book.odt") // OOXML content, odt extension
	writeZip(t, xlsxPath, map[string]string{"xl/workbook.xml": `<workbook xmlns="` + smlNS + `"/>`})
	kind, err = Detect(xlsxPath)
	if err != nil {
		t.Fatalf("Detect xlsx: %v", err)
	}
	if kind != KindXLSX {
		t.Fatalf("OOXML content with .odt extension: kind = %q, want %q", kind, KindXLSX)
	}
}

// TestDetect_OPCRelationshipResolution: the officeDocument relationship in
// _rels/.rels resolves the kind even without conventional part paths being
// consulted.
func TestDetect_OPCRelationshipResolution(t *testing.T) {
	path := filepath.Join(t.TempDir(), "package.bin")
	writeZip(t, path, map[string]string{
		"_rels/.rels": `<Relationships xmlns="` + opcRelNS + `"><Relationship Id="rId1" Type="` + officeDocRel + `" Target="word/document.xml"/></Relationships>`,
	})
	kind, err := Detect(path)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if kind != KindDOCX {
		t.Fatalf("kind = %q, want %q", kind, KindDOCX)
	}
}

// TestDetect_UnrecognizedContainerNoError: a ZIP whose format cannot be
// resolved yields unknown without an error, even with a misleading extension.
func TestDetect_UnrecognizedContainerNoError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "random.docx")
	writeZip(t, path, map[string]string{"some/random.txt": "hello"})
	kind, err := Detect(path)
	if err != nil {
		t.Fatalf("Detect: unexpected error %v", err)
	}
	if kind != KindUnknown {
		t.Fatalf("kind = %q, want KindUnknown", kind)
	}
}

// TestDetect_OLEStreamName: an OLE compound file is classified by its
// mandated stream name; with no legacy kinds in the surface it is reported
// as unknown rather than guessed, and never as an error.
func TestDetect_OLEStreamName(t *testing.T) {
	dir := t.TempDir()
	for _, stream := range []string{"WordDocument", "PowerPoint Document", "Workbook", "Book"} {
		t.Run(stream, func(t *testing.T) {
			path := filepath.Join(dir, "legacy.doc")
			writeOLEFixture(t, path, stream)
			kind, err := Detect(path)
			if err != nil {
				t.Fatalf("Detect: unexpected error %v", err)
			}
			if kind != KindUnknown {
				t.Fatalf("kind = %q, want KindUnknown (no legacy kind yet)", kind)
			}
		})
	}
}

// TestDetect_OLEWithTxtExtension: an OLE file renamed to .txt must not be
// misdetected as text; content is authoritative.
func TestDetect_OLEWithTxtExtension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.txt")
	writeOLEFixture(t, path, "WordDocument")
	kind, err := Detect(path)
	if err != nil {
		t.Fatalf("Detect: unexpected error %v", err)
	}
	if kind != KindUnknown {
		t.Fatalf("kind = %q, want KindUnknown", kind)
	}
}

// TestDetect_ZIPContentTypesFallback: when the package-level officeDocument
// relationship is absent or unresolvable, the [Content_Types].xml Overrides
// classify the OOXML package.
func TestDetect_ZIPContentTypesFallback(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		rels string // optional _rels/.rels; empty means absent
		ct   string // [Content_Types].xml body
		want Kind
	}{
		{
			"docx-no-rels",
			"",
			`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`,
			KindDOCX,
		},
		{
			"xlsx-rels-without-office-doc",
			`<Relationships xmlns="` + opcRelNS + `"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/extended-properties" Target="docProps/app.xml"/></Relationships>`,
			`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/></Types>`,
			KindXLSX,
		},
		{
			"pptx-no-rels",
			"",
			`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/ppt/presentation.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml"/></Types>`,
			KindPPTX,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{"[Content_Types].xml": tc.ct}
			if tc.rels != "" {
				files["_rels/.rels"] = tc.rels
			}
			path := filepath.Join(dir, tc.name+".bin")
			writeZip(t, path, files)
			kind, err := Detect(path)
			if err != nil {
				t.Fatalf("Detect: %v", err)
			}
			if kind != tc.want {
				t.Fatalf("kind = %q, want %q", kind, tc.want)
			}
		})
	}
}

// TestOpcContentTypeKind: the [Content_Types].xml Override classifier maps
// the three OOXML main-part content types and yields "" otherwise.
func TestOpcContentTypeKind(t *testing.T) {
	cases := []struct {
		name string
		ct   string
		want Kind
	}{
		{
			"docx",
			`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`,
			KindDOCX,
		},
		{
			"xlsx",
			`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/></Types>`,
			KindXLSX,
		},
		{
			"pptx",
			`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/ppt/presentation.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml"/></Types>`,
			KindPPTX,
		},
		{
			"no-match",
			`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/custom/thing.xml" ContentType="application/octet-stream"/></Types>`,
			"",
		},
		{
			"malformed",
			`<Types><Override`,
			"",
		},
		{
			"empty",
			``,
			"",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := opcContentTypeKind([]byte(tc.ct)); got != tc.want {
				t.Fatalf("opcContentTypeKind = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestKindFromOPCMainPart: the conventional OOXML main-part paths map to the
// three office kinds; anything else is unknown.
func TestKindFromOPCMainPart(t *testing.T) {
	cases := []struct {
		mainPart string
		want     Kind
	}{
		{"word/document.xml", KindDOCX},
		{"xl/workbook.xml", KindXLSX},
		{"ppt/presentation.xml", KindPPTX},
		{"custom/part.xml", ""},
	}
	for _, tc := range cases {
		t.Run(tc.mainPart, func(t *testing.T) {
			if got := kindFromOPCMainPart(tc.mainPart); got != tc.want {
				t.Fatalf("kindFromOPCMainPart(%q) = %q, want %q", tc.mainPart, got, tc.want)
			}
		})
	}
}

// TestOleUTF16Name: UTF-16LE directory-entry names decode to the original
// string, with odd trailing bytes truncated.
func TestOleUTF16Name(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want string
	}{
		{"empty", nil, ""},
		{"word", utf16LEBytes("WordDocument")[:len("WordDocument")*2], "WordDocument"},
		{"odd-length-truncated", []byte{'W', 0, 'o', 0, 'r'}, "Wo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := oleUTF16Name(tc.in); got != tc.want {
				t.Fatalf("oleUTF16Name(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestDetectOLE_ContainerEdgeBranches: short headers, unreadable directory
// sectors, invalid name lengths, and unmapped stream names all yield "" from
// detectOLEContainer without an error, and the file stays KindUnknown.
func TestDetectOLE_ContainerEdgeBranches(t *testing.T) {
	dir := t.TempDir()

	t.Run("short-header", func(t *testing.T) {
		path := filepath.Join(dir, "short.bin")
		head := make([]byte, 40)
		copy(head, oleSignature)
		if err := os.WriteFile(path, head, 0o644); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if kind := detectOLEContainer(f, head); kind != "" {
			t.Fatalf("detectOLEContainer = %q, want \"\"", kind)
		}
	})

	t.Run("dir-sector-beyond-eof", func(t *testing.T) {
		path := filepath.Join(dir, "beyond.bin")
		head := make([]byte, 512)
		copy(head, oleSignature)
		head[30] = 9
		binary.LittleEndian.PutUint32(head[48:52], 100) // dirStart far past EOF
		if err := os.WriteFile(path, head, 0o644); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if kind := detectOLEContainer(f, head); kind != "" {
			t.Fatalf("detectOLEContainer = %q, want \"\"", kind)
		}
	})

	t.Run("invalid-name-lengths-then-unmapped", func(t *testing.T) {
		path := filepath.Join(dir, "badlen.bin")
		dirSector := make([]byte, 512)
		// Entry 0: nameLen = 1 (< 2) → skipped.
		binary.LittleEndian.PutUint16(dirSector[64:66], 1)
		// Entry 1: nameLen = 100 (> 64) → skipped.
		binary.LittleEndian.PutUint16(dirSector[128+64:128+66], 100)
		// Entry 2: a valid name not in the mandated map.
		unmapped := "CustomStream"
		copy(dirSector[256:], utf16LEBytes(unmapped))
		binary.LittleEndian.PutUint16(dirSector[256+64:256+66], uint16(len(unmapped)*2+2))
		writeOLERaw(t, path, 0, dirSector)

		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		head := make([]byte, 512)
		if _, err := f.ReadAt(head, 0); err != nil {
			t.Fatal(err)
		}
		if kind := detectOLEContainer(f, head); kind != "" {
			t.Fatalf("detectOLEContainer = %q, want \"\"", kind)
		}
	})

	t.Run("detect-unknown-stream", func(t *testing.T) {
		path := filepath.Join(dir, "unmapped.bin")
		writeOLEFixture(t, path, "CustomStream")
		kind, err := Detect(path)
		if err != nil {
			t.Fatalf("Detect: unexpected error %v", err)
		}
		if kind != KindUnknown {
			t.Fatalf("kind = %q, want KindUnknown", kind)
		}
	})
}

// writeOLERaw writes a minimal OLE compound file: a 512-byte header with the
// given first directory sector index plus one raw directory sector.
func writeOLERaw(t *testing.T, path string, dirStart uint32, dir []byte) {
	t.Helper()
	head := make([]byte, 512)
	copy(head, oleSignature)
	head[30] = 9 // 512-byte sectors: the field value is the exponent (2^9)
	binary.LittleEndian.PutUint32(head[48:52], dirStart)
	data := append(head, dir...)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeOLEFixture writes a minimal OLE compound file: a 512-byte header plus
// one directory sector whose second entry carries the given stream name.
func writeOLEFixture(t *testing.T, path, streamName string) {
	t.Helper()
	head := make([]byte, 512)
	copy(head, oleSignature)
	head[30] = 9 // 512-byte sectors: the field value is the exponent (2^9)
	binary.LittleEndian.PutUint32(head[48:52], 0)

	dir := make([]byte, 512)
	// Entry 0: root storage.
	rootName := "Root Entry"
	copy(dir[0:], utf16LEBytes(rootName))
	binary.LittleEndian.PutUint16(dir[64:66], uint16(len(rootName)*2+2))
	dir[66] = 5 // STGTY_ROOT
	// Entry 1: the mandated stream.
	off := 128
	copy(dir[off:], utf16LEBytes(streamName))
	binary.LittleEndian.PutUint16(dir[off+64:off+66], uint16(len(streamName)*2+2))
	dir[off+66] = 2 // STGTY_STREAM

	data := append(head, dir...)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func utf16LEBytes(s string) []byte {
	out := make([]byte, 0, len(s)*2+2)
	for _, r := range s {
		out = append(out, byte(r), byte(r>>8))
	}
	return append(out, 0, 0) // NUL terminator
}
