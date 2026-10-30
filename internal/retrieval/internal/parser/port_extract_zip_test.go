package parser

import (
	"archive/zip"
	"bytes"
	"fmt"
	"strings"
	"testing"
)

type extractionZipPart struct{ name, text string }

func extractionZip(t *testing.T, parts []extractionZipPart) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, p := range parts {
		f, err := w.Create(p.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write([]byte(p.text)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func archiveExtractionInputs(t *testing.T) []extractionInput {
	var inputs []extractionInput
	add := func(id, ext string, parts ...extractionZipPart) {
		inputs = append(inputs, extractionInput{id + ext, ext, extractionZip(t, parts), false})
	}
	for _, ext := range []string{".docx", ".odt", ".ods", ".odp"} {
		part := "word/document.xml"
		body := `<w:document xmlns:w="urn:w"><w:p><w:r><w:t>Hello &amp; 日本</w:t></w:r><w:tab/><w:r><w:t>cell</w:t></w:r><w:br/><w:t>line</w:t></w:p><w:p/><w:p><w:t>Last</w:t></w:p></w:document>`
		if ext != ".docx" {
			part = "content.xml"
			body = `<office:document xmlns:office="urn:o" xmlns:text="urn:t"><text:h>Title</text:h><text:p>A<text:s text:c="3"/>B<text:tab/>C<text:line-break/>D &amp; 日本</text:p><office:value>42</office:value></office:document>`
		}
		add("blocks", ext, extractionZipPart{part, body})
		add("missing", ext)
		add("malformed", ext, extractionZipPart{part, "<p><t>kept</t><bad"})
		add("truncated", ext, extractionZipPart{part, "<p><t>" + strings.Repeat(" ", MaxIndexFileSize) + "</t></p>"})
	}
	add("sheets-order", ".xlsx", extractionZipPart{"xl/sharedStrings.xml", `<sst><si><t>Hello</t><t> world</t></si><si><t>日本</t></si></sst>`}, extractionZipPart{"xl/worksheets/sheet2.xml", `<worksheet><row><c t="s"><v>1</v></c><c><v>42</v></c><c/></row><row><c t="s"><v>0</v></c><c t="s"><v>99</v></c></row></worksheet>`}, extractionZipPart{"xl/worksheets/sheet1.xml", `<worksheet><row><c t="inlineStr"><is><t>ignored</t></is></c><c><v>3.5</v></c></row></worksheet>`})
	add("no-shared", ".xlsx", extractionZipPart{"xl/worksheets/sheet1.xml", `<worksheet><row><c t="s"><v>0</v></c><c><v>7</v></c><c/></row></worksheet>`})
	add("empty", ".xlsx")
	add("slides-order", ".pptx", extractionZipPart{"ppt/slides/slide2.xml", `<sp><p><t>Second</t><br/><t>line</t></p></sp>`}, extractionZipPart{"ppt/slides/slide1.xml", `<sp><p><t>First</t></p></sp>`})
	add("empty", ".pptx")
	add("chapters-sorted", ".epub", extractionZipPart{"OEBPS/b.xhtml", "<p>Second</p>"}, extractionZipPart{"OEBPS/a.xhtml", "<head><title>hidden</title></head><p>First &amp; 日本</p><script>hidden</script>"}, extractionZipPart{"META-INF/ignored.html", "hidden"}, extractionZipPart{"toc.ncx", "ignored"})
	add("encrypted", ".epub", extractionZipPart{"META-INF/encryption.xml", ""})
	add("rights", ".epub", extractionZipPart{"META-INF/rights.xml", ""})
	add("empty", ".epub")
	for _, ext := range []string{".docx", ".xlsx", ".pptx", ".odt", ".ods", ".odp", ".epub"} {
		inputs = append(inputs, extractionInput{"invalid" + ext, ext, []byte("not a zip"), false})
	}
	var many []extractionZipPart
	for i := 0; i <= MaxArchiveEntries; i++ {
		many = append(many, extractionZipPart{fmt.Sprintf("unused/%05d", i), ""})
	}
	add("too-many-entries", ".docx", many...)
	var bomb []extractionZipPart
	for i := 0; i < 10; i++ {
		bomb = append(bomb, extractionZipPart{fmt.Sprintf("ppt/slides/slide%d.xml", i), strings.Repeat(" ", MaxIndexFileSize)})
	}
	add("budget-exhausted", ".pptx", bomb...)
	return inputs
}
