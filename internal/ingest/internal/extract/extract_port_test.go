package extract

// Deterministic synthesized corpus. Calls only Detect, ReadTextKind and
// ReadStructuredKind; never instantiates an OCR/barcode or optional provider.
import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/documentformat"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

type extractPortCase struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Input       []byte  `json:"input"`
	Kind        Kind    `json:"kind"`
	ReadKind    Kind    `json:"read_kind"`
	DetectError string  `json:"detect_error"`
	Extract     bool    `json:"extract"`
	Result      *Result `json:"result"`
	Error       string  `json:"error"`
}

func TestPortIngestExtractionContract(t *testing.T) {
	// Ensure Detect's advisory cannot launch Magika on the operator PATH.
	t.Setenv("PATH", t.TempDir())
	ctx := context.Background()
	root := t.TempDir()
	cases := []extractPortCase{}
	add := func(id, name string, data []byte, extract bool, override Kind) {
		p := filepath.Join(root, name)
		if err := os.WriteFile(p, data, 0600); err != nil {
			t.Fatal(err)
		}
		k, e := Detect(p)
		c := extractPortCase{ID: id, Name: name, Input: data, Kind: k, Extract: extract}
		if e != nil {
			c.DetectError = strings.ReplaceAll(e.Error(), p, "$PATH")
		}
		if extract {
			if override != "" {
				k = override
			}
			c.ReadKind = k
			switch k {
			case KindText, KindMarkdown, KindCSV:
				c.Result, e = ReadTextKind(ctx, p, k)
			default:
				c.Result, e = ReadStructuredKind(ctx, p, k)
			}
			if e != nil {
				c.Error = strings.ReplaceAll(e.Error(), p, "$PATH")
			}
		}
		cases = append(cases, c)
	}
	archive := func(parts map[string]string) []byte {
		var b bytes.Buffer
		z := zip.NewWriter(&b)
		names := []string{}
		for n := range parts {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			w, e := z.Create(n)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = w.Write([]byte(parts[n])); e != nil {
				t.Fatal(e)
			}
		}
		if e := z.Close(); e != nil {
			t.Fatal(e)
		}
		return b.Bytes()
	}
	for _, f := range documentformat.SupportedFormats {
		add("extension-"+f.Extension, "file"+strings.ToUpper(f.Extension), []byte("source"), false, "")
	}
	for _, f := range documentformat.UnsupportedFormats {
		add("blocked-"+f.Extension, "file"+f.Extension, []byte("source"), true, "")
	}
	for _, v := range []struct {
		n string
		b []byte
	}{
		{"pdf", []byte("%PDF-1.7")}, {"png", []byte("\x89PNG\r\n\x1a\n")}, {"jpeg", []byte("\xff\xd8\xff")},
		{"tiff-le", []byte("II*\x00")}, {"tiff-be", []byte("MM\x00*")}, {"webp", []byte("RIFF0000WEBP")},
		{"heic", []byte("0000ftypheic")}, {"heif", []byte("0000ftypmif1")},
	} {
		add("magic-"+v.n, "renamed.txt", v.b, false, "")
	}
	add("unknown", "file.zzz", []byte("source"), false, "")
	add("empty-unknown", "file", []byte{}, false, "")
	add("empty-text", "file.txt", []byte{}, true, "")
	add("text-raw", "file.txt", []byte("  raw\r\n\nunchanged	\xff"), true, "")
	add("markdown", "file.md", []byte("# Title\n\n**body**"), true, "")
	add("csv", "file.csv", []byte("a,b\n1,2"), true, "")
	for i, s := range []string{"<h1>Title</h1><p>a &amp; b<br>next</p>", "<script>x</script><style>y</style><p>docu-\nment\u00a0text</p>", "<p>one</p><p>two</p>", "<broken>survives &lt;x&gt; &copy; &#x1f600;", "<table><tr><td>A</td><td>B</td></tr></table>", "<script>unfinished"} {
		add(fmt.Sprintf("html-%d", i), "file.html", []byte(s), true, "")
	}
	for i, s := range []string{`{\rtf1 Hello\par world}`, `{\rtf1{\fonttbl ignored}body	ab x\'41\{end\}}`, `{\rtf1 unclosed`, "trailing\\", "{\\*ignored}visible", `{\rtf1 \'e9}`} {
		add(fmt.Sprintf("rtf-%d", i), "file.rtf", []byte(s), true, "")
	}
	add("zip-corrupt", "file.docx", []byte("PK\x03\x04broken"), false, "")
	add("zip-corrupt-blocked", "file.pages", []byte("PK\x03\x04broken"), true, "")
	add("zip-empty", "file.docx", archive(map[string]string{}), false, "")
	add("zip-unknown", "file.docx", archive(map[string]string{"random": "x"}), false, "")
	for _, ext := range []string{"odt", "ods", "odp", "epub"} {
		k, _ := documentformat.KindForExtension(ext)
		parts := map[string]string{"mimetype": string(k), "content.xml": "<root><p>First</p><p>Second<tab/>tab<line-break/>line &amp; end</p></root>"}
		if ext == "epub" {
			parts["b.xhtml"] = "<p>chapter B</p>"
			parts["a.html"] = "<p>chapter A</p>"
		}
		add("archive-"+ext, "file.bin", archive(parts), true, "")
		add("missing-"+ext, "file.bin", archive(map[string]string{"mimetype": string(k)}), true, "")
	}
	for _, ext := range []string{"docx", "xlsx", "pptx"} {
		main := map[string]string{"docx": "word/document.xml", "xlsx": "xl/workbook.xml", "pptx": "ppt/presentation.xml"}[ext]
		add("detect-main-"+ext, "file.bin", archive(map[string]string{main: "<root/>"}), false, "")
		add("detect-rels-"+ext, "file.bin", archive(map[string]string{"_rels/.rels": `<Relationships><Relationship Type="urn/officeDocument" Target="/` + main + `"/></Relationships>`}), false, "")
		ct := map[string]string{"docx": "wordprocessingml.document.main+xml", "xlsx": "spreadsheetml.sheet.main+xml", "pptx": "presentationml.presentation.main+xml"}[ext]
		add("detect-ct-"+ext, "file.bin", archive(map[string]string{"[Content_Types].xml": `<Types><Override ContentType="` + ct + `"/></Types>`}), false, "")
	}
	add("pptx-sorted", "file.bin", archive(map[string]string{"ppt/presentation.xml": "<root/>", "ppt/slides/slide2.xml": "<root><p><t>B</t></p></root>", "ppt/slides/slide10.xml": "<root><p><t>A</t></p><p><t>line</t></p></root>"}), true, "")
	add("epub-drm", "file.bin", archive(map[string]string{"mimetype": string(KindEPUB), "META-INF/encryption.xml": ""}), true, "")
	add("odt-malformed", "file.bin", archive(map[string]string{"mimetype": string(KindODT), "content.xml": "<root><p>unclosed"}), true, "")
	add("odt-bad-archive", "file.odt", []byte("not zip"), true, KindODT)
	add("odt-entry-limit", "file.odt", archive(map[string]string{"mimetype": string(KindODT), "content.xml": strings.Repeat("a", maxZipEntryBytes+1)}), true, "")
	many := map[string]string{}
	for i := 0; i < maxZipEntries+1; i++ {
		many[fmt.Sprintf("entry-%04d", i)] = ""
	}
	add("archive-count-limit", "file.odt", archive(many), true, KindODT)
	for _, name := range []string{"WordDocument", "Workbook", "Book", "PowerPoint Document", "Unknown"} {
		b := make([]byte, 1024)
		copy(b, oleSignature)
		b[30] = 9
		nameBytes := uint16(2) // Include the terminating UTF-16 NUL.
		for i, r := range []byte(name) {
			binary.LittleEndian.PutUint16(b[512+i*2:], uint16(r))
			nameBytes += 2
		}
		binary.LittleEndian.PutUint16(b[576:], nameBytes)
		add("ole-"+name, "file.txt", b, false, "")
	}
	// Keep large compressed input in fixture; never embed expanded oversized bytes.
	_, file, _, _ := runtime.Caller(0)
	source := filepath.Dir(file)
	hashes := map[string]string{}
	for _, dir := range []string{source, filepath.Join(source, "../filetype")} {
		entries, e := os.ReadDir(dir)
		if e != nil {
			t.Fatal(e)
		}
		for _, f := range entries {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".go") || strings.HasSuffix(f.Name(), "_test.go") && f.Name() != "extract_port_test.go" {
				continue
			}
			//nolint:gosec // path is derived from repository fixtures or the test-owned temporary root, not external input.
			b, e := os.ReadFile(filepath.Join(dir, f.Name()))
			if e != nil {
				t.Fatal(e)
			}
			hashes[filepath.Base(dir)+"/"+f.Name()] = fmt.Sprintf("%x", sha256.Sum256(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))))
		}
	}
	content, e := json.MarshalIndent(map[string]any{"schema_version": 1, "oracle": fixtureoracle.Current(), "source_hashes": hashes, "cases": cases, "normalizations": []string{"source path in diagnostics replaced by $PATH", "invalid UTF-8 in Go result strings encoded using encoding/json replacement semantics"}, "residuals": []string{"OCR/barcode/PDF engines never invoked", "Magika unavailable via isolated PATH", "DOCX/XLSX/EML extraction not replayed"}}, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	content = append(content, '\n')
	out := filepath.Join(source, "../../../../testdata/port/ingest/extraction.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if e = os.WriteFile(out, content, 0600); e != nil {
			t.Fatal(e)
		}
	} else {
		//nolint:gosec // path is derived from repository fixtures or the test-owned temporary root, not external input.
		old, e := os.ReadFile(out)
		if e != nil {
			t.Fatal(e)
		}
		if !bytes.Equal(old, content) {
			t.Fatal("ingest extraction fixture drift; regenerate deliberately")
		}
	}
	t.Logf("replayed %d deterministic cases", len(cases))
}
