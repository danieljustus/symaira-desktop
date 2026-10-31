package extract

// Go oracle for the structured readers (DOCX/XLSX/PPTX/ODF/EPUB/EML), the Go
// XML decoder diagnostics they surface, ZIP defence cases (SEC-001) and the
// regexp/html text helpers. Every case calls the production function and
// records the exact result, error text, or recovered panic. Never touches OCR,
// barcode, network or operator state.

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

type structuredCase struct {
	ID        string   `json:"id"`
	Group     string   `json:"group"`
	Reader    string   `json:"reader"`
	Name      string   `json:"name,omitempty"`
	Input     []byte   `json:"input"`
	Shared    []string `json:"shared,omitempty"`
	Kind      Kind     `json:"kind,omitempty"`
	Detect    Kind     `json:"detect,omitempty"`
	DetectErr string   `json:"detect_error,omitempty"`
	Text      string   `json:"text"`
	Result    *Result  `json:"result"`
	Error     string   `json:"error"`
	Panic     string   `json:"panic,omitempty"`
}

func capture(f func() (string, error)) (text, errText, panicText string) {
	defer func() {
		if r := recover(); r != nil {
			panicText = fmt.Sprint(r)
			text, errText = "", ""
		}
	}()
	t, e := f()
	if e != nil {
		return "", e.Error(), ""
	}
	return t, "", ""
}

func portZip(t *testing.T, parts map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	names := make([]string, 0, len(parts))
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

type rawEntry struct {
	name   string
	method uint16
	flags  uint16
	crc    uint32
	data   []byte // already-compressed bytes
	usize  uint64
}

func deflateBytes(t *testing.T, b []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w, err := flate.NewWriter(&out, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(b)
	_ = w.Close()
	return out.Bytes()
}

func rawZip(t *testing.T, entries []rawEntry) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: e.method, Flags: e.flags, CRC32: e.crc, CompressedSize64: uint64(len(e.data)), UncompressedSize64: e.usize}
		w, err := z.CreateRaw(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write(e.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// xmlSnippets stresses the Go XML decoder: entities, names, namespaces,
// comments, directives, CDATA, declarations, attributes and UTF-8 handling.
func xmlSnippets() []string {
	return []string{
		``, `<a/>`, `<a></a>`, `<a>x</a>`, `<a>x`, `<a>`, `<`, `</`, `</a>`, `<a></b>`, `<a><b></a></b>`,
		`<a>&amp;&lt;&gt;&apos;&quot;</a>`, `<a>&#65;&#x42;&#X43;</a>`, `<a>&#x1F600;</a>`, `<a>&#xD800;</a>`, `<a>&#1114112;</a>`, `<a>&#99999999999999999999;</a>`,
		`<a>&foo;</a>`, `<a>&foo</a>`, `<a>&</a>`, `<a>&;</a>`, `<a>&#;</a>`, `<a>&#x;</a>`, `<a>&#xZ;</a>`, `<a>&#65</a>`, `<a>&amp</a>`, `<a>&nbsp;</a>`, `<a>&1a;</a>`, `<a>&a b;</a>`,
		"<a>one\r\ntwo\rthree\nfour</a>", "<a>\x00</a>", "<a>\x01</a>", "<a>\x0b</a>", "<a>\xef\xbf\xbe</a>", "<a>\xff</a>", "<a>\xc3</a>", "<a>\xed\xa0\x80</a>",
		"<a>caf\u00e9 \u4e2d\u6587 \U0001F600</a>", `<a>]]></a>`, `<a>]]]></a>`, `<a><![CDATA[x < y & z]]></a>`, `<a><![CDATA[x]]]]></a>`, `<a><![CDATA[unterminated`, `<a><![CDAT[x]]></a>`, `<a><![x]]></a>`,
		`<a><!-- c --></a>`, `<a><!-- c -- d --></a>`, `<a><!-- c ---></a>`, `<a><!- c --></a>`, `<a><!-- unterminated`, `<a><!--></a>`, `<a><!---></a>`, `<a><!----></a>`,
		`<?xml version="1.0"?><a/>`, `<?xml version="1.0" encoding="UTF-8"?><a/>`, `<?xml version="1.0" encoding="utf-8"?><a/>`, `<?xml version="1.0" encoding="iso-8859-1"?><a/>`, `<?xml version="1.1"?><a/>`, `<?xml version='2.0'?><a/>`, `<?xml encoding="x"?><a/>`, `<?xml version="1.0" encoding="UTF-16"?><a/>`,
		`<?php echo ?><a/>`, `<?xml?><a/>`, `<? ?><a/>`, `<?`, `<?x`, `<?x ?`, `<?xml version="1.0"`,
		`<!DOCTYPE a><a/>`, `<!DOCTYPE a [<!ENTITY e "v">]><a>&e;</a>`, `<!DOCTYPE a [<!-- c --><!ELEMENT a ANY>]><a/>`, `<!DOCTYPE a "x>y"><a/>`, `<!DOCTYPE a 'x>y'><a/>`, `<!DOCTYPE`, `<!DOCTYPE a [`, `<!DOCTYPE a [<!-- unterminated`, `<!DOCTYPE a [<b>]><a/>`, `<!>`, `<!x>`,
		`<a b="1" c='2'/>`, `<a b=1/>`, `<a b/>`, `<a b=/>`, `<a b="1/>`, `<a b="<"/>`, `<a b="&amp;&lt;"/>`, `<a b="&bad;"/>`, `<a b="1"c="2"/>`, `<a b = "1" />`, `<a b="1" b="2"/>`, `<a =1/>`, `<a b="`, `<a b`, `<a `, `<a /`, `<a /x>`, `<a/ >`, `<a b="1">`,
		`<a:b/>`, `<a:b></a:b>`, `<a:b></c:b>`, `<a:b></b>`, `<b></a:b>`, `<a:b:c/>`, `<:a/>`, `<a:/>`, `<a:></a:>`, `<a xmlns:x="u"><x:b/></a>`, `<a xmlns:x="u"><x:b></x:b></a>`, `<a xmlns="d"><b/></a>`, `<a xmlns:x="u"><x:b></y:b></a>`,
		`<x:a xmlns:x="u1"><x:b xmlns:x="u2"></x:b></x:a>`, `<x:a xmlns:x="u1"><x:b xmlns:x="u2"></x:b></x:a></x:a>`, `<xml:a/>`, `<a xml:lang="en"/>`, `<a xmlns:xml="u"/>`,
		`<1a/>`, `<-a/>`, `<.a/>`, `<a-b.c_d/>`, `<\u00e9/>`, "<\u00e9t\u00e9/>", "<a\xc3/>", "<\u4e2d/>", "<a\u00b7/>", "<\u00b7a/>", "<a\u0300/>", "<a\u3007/>",
		`<a b:c="1"/>`, `<a xmlns:b="u" b:c="1"/>`, `<a b:c:d="1"/>`,
		"<a>\n<b>\n</c>\n</a>", "<a>\n\n\n<b", "\n\n<a>x", "<a>x</a>trailing", "<a>x</a><b>y</b>", "<a>x</a>\n<!-- c -->\n", "text before<a/>", "   <a/>", "\ufeff<a/>", "\xef\xbb\xbf<a/>",
		`<w:document xmlns:w="u"><w:body><w:p><w:r><w:t>Hello</w:t></w:r></w:p></w:body></w:document>`,
		`<w:document xmlns:w="u"><w:body><w:p><w:r><w:t xml:space="preserve"> a </w:t><w:tab/><w:t>b</w:t><w:br/><w:cr/></w:r></w:p></w:body></w:document>`,
		`<d><t>x<b>nested</b>y</t></d>`, `<d><t><t>inner</t></t></d>`, `<d><t/></d>`, `<d><t></d>`, `<d><t>a&bad;</t></d>`, `<d><t>a<![CDATA[b]]>c</t></d>`, `<d><t>a<!--c-->b</t></d>`, `<d><t>a<?pi x?>b</t></d>`,
		`<d><tbl><tr><tc><p><t>A</t></p></tc><tc><t>B</t></tc></tr><tr><tc><t>C</t></tc></tr></tbl></d>`,
		`<d><tbl><tr><tc><t>a|b</t></tc><tc><t>l1</t><br/><t>l2</t></tc></tr></tbl></d>`,
		`<d><tbl></tbl></d>`, `<d><tbl><tr></tr></tbl></d>`, `<d><tbl><tr/><tr/></tbl></d>`, `<d><tbl><tc><t>x</t></tc></tbl></d>`, `<d><tr><tc><t>x</t></tc></tr></d>`, `<d><tbl><tr><tc><t>x</t></tc></tr></tbl>after<t>y</t></d>`,
		`<d><tbl><tr><tc><tbl><tr><tc><t>in</t></tc></tr></tbl><t>out</t></tc></tr></tbl><t>end</t></d>`, `<d><tbl><tr><tc><t>x</t><tab/><br/></tc></tr></tbl></d>`, `<d><tbl><tr><tc></tc></tr></tbl></d>`, `<d><tbl><tr><tc><t>x</t></tc></tr>`,
		`<d><p>a</p><p>b</p><tab/>c<br/>d<cr/>e</d>`, `<d>text outside<t>in</t></d>`,
		`<o:p xmlns:o="u"><o:h>H</o:h><o:p>P1<o:tab/>t<o:line-break/>l</o:p>tail</o:p>`, `<o>  <p>a</p>  <p>b</p>  </o>`, `<o><h>x</h><p/></o>`,
		`<s><p><t>A</t></p><p><t>B</t></p></s>`, `<s><p><r><t>A</t></r></p></s>`, `<s><t>x</t><t>y</t></s>`, `<s><p><t>a</t>text<t>b</t></p></s>`,
		`<sh><sheetData><row><c><v>1</v></c><c t="s"><v>0</v></c></row><row><c t="inlineStr"><is><t>inline</t></is></c></row></sheetData></sh>`,
		`<sh><row><c t="s"><v>5</v></c><c t="s"><v>-1</v></c><c t="s"><v> 1 </v></c><c t="s"><v>+1</v></c><c t="s"><v>1x</v></c><c t="s"><v></v></c><c t="s"><v>99999999999999999999</v></c></row></sh>`,
		`<sh><row><c><v>a</v><v>b</v></c></row></sh>`, `<sh><row><c><is><t>x</t></is><v>y</v></c></row></sh>`, `<sh><row><c/></row></sh>`, `<sh><row></row></sh>`, `<sh><row/><row/></sh>`, `<sh><c><v>1</v></c></sh>`, `<sh><row><c><v>1</v></c></sh>`, `<sh><row><c>`, `<sh><row><c><v>x`,
		`<sh><row><c><c><v>inner</v></c></c></row></sh>`, `<sh><row><c><x:c xmlns:x="u"><v>q</v></x:c></c></row></sh>`, `<sh><row><c t="s"><v>0</v></c><c t="s"><v>1</v></c></row><row><c><v>3</v></c></row></sh>`, `<sh><row><c><v>a|b</v></c></row><row><c><v>l1&#10;l2</v></c></row></sh>`,
		`<sh><row><c><v>1</v></c><c><v>2</v></c></row><row><c><v>3</v></c></row></sh>`,
	}
}

func xmlSeeds() []string {
	return []string{
		`<?xml version="1.0" encoding="UTF-8"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Hi &amp; bye</w:t></w:r></w:p><w:tbl><w:tr><w:tc><w:p><w:t>A|B</w:t></w:p></w:tc><w:tc><w:t>C</w:t></w:tc></w:tr></w:tbl></w:body></w:document>`,
		`<office:document-content xmlns:office="u" xmlns:text="v"><office:body><text:p>One<text:tab/>two<text:line-break/>three &#x263a;</text:p><text:h>Head</text:h><!-- c --><![CDATA[cd]]></office:body></office:document-content>`,
		`<p:sld xmlns:a="u" xmlns:p="v"><p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>Slide A</a:t></a:r></a:p><a:p><a:r><a:t>Line &lt;2&gt;</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:sld>`,
		`<worksheet xmlns="u"><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1"><v>2.5</v></c><c r="C1" t="inlineStr"><is><t>in&amp;line</t></is></c></row><row r="2"><c r="A2" t="s"><v>1</v></c></row></sheetData></worksheet>`,
	}
}

func mutateBytes(seed string, n int) []string {
	interesting := []byte{'<', '>', '&', ';', '"', '\'', '/', '=', '!', '?', 0, 0xff, '-', ']', ' ', ':', '#', 'x', '[', '\n', '\r'}
	out := make([]string, 0, n)
	state := uint32(0x9e3779b9)
	next := func() uint32 {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		return state
	}
	for i := 0; i < n; i++ {
		b := []byte(seed)
		switch next() % 3 {
		case 0:
			b[next()%uint32(len(b))] = interesting[next()%uint32(len(interesting))]
		case 1:
			p := int(next() % uint32(len(b)))
			b = append(b[:p], append([]byte{interesting[next()%uint32(len(interesting))]}, b[p:]...)...)
		default:
			p := int(next() % uint32(len(b)))
			q := p + int(next()%4)
			if q > len(b) {
				q = len(b)
			}
			b = append(b[:p], b[q:]...)
		}
		out = append(out, string(b))
	}
	return out
}

func emlMessages() map[string]string {
	crlf := func(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }
	m := map[string]string{}
	add := func(id, s string) { m[id] = s }
	add("plain", crlf("From: a@b\nSubject: Hello\nContent-Type: text/plain\n\nBody text\n"))
	add("plain-lf", "From: a@b\nSubject: Hello\n\nBody text\n")
	add("plain-no-subject", crlf("From: a@b\n\nBody\n"))
	add("plain-no-body", crlf("Subject: S\n\n"))
	add("headers-only-no-blank", "Subject: S\r\nFrom: a@b")
	add("headers-trailing-eof", "Subject: S\r\n")
	add("empty", "")
	add("blank-only", "\r\n")
	add("leading-space", " Subject: x\r\n\r\nbody")
	add("leading-tab", "\tSubject: x\r\n\r\nbody")
	add("no-colon", "Subject S\r\n\r\nbody")
	add("no-colon-after-good", "Subject: S\r\nbad line\r\n\r\nbody")
	add("empty-key", ": value\r\nSubject: S\r\n\r\nb")
	add("space-key", "Sub ject: v\r\nSubject: S\r\n\r\nb")
	add("folded-subject", crlf("Subject: first\n second\n\tthird\n\nbody"))
	add("fold-trim", crlf("Subject:   spaced   \n   more   \n\nbody"))
	add("duplicate-subject", crlf("Subject: one\nSubject: two\n\nb"))
	add("lower-key", crlf("subject: lower\nCONTENT-TYPE: text/plain\n\nb"))
	add("subject-only-empty", crlf("Subject:\n\nbody"))
	add("subject-utf8", crlf("Subject: caf\u00e9 \u4e2d\u6587\n\nbody \u00fc"))
	add("subject-encoded-word", crlf("Subject: =?UTF-8?B?w7w=?=\n\nbody"))
	add("html-only", crlf("Subject: H\nContent-Type: text/html; charset=utf-8\n\n<p>Hello <b>world</b></p><br>x"))
	add("ct-image", crlf("Subject: I\nContent-Type: image/png\nContent-Transfer-Encoding: base64\n\niVBORw0K"))
	add("ct-weird", crlf("Subject: I\nContent-Type: text\n\nbody"))
	add("ct-slash-only", crlf("Subject: I\nContent-Type: /plain\n\nbody"))
	add("ct-param-error", crlf("Subject: I\nContent-Type: text/plain; =bad\n\nbody"))
	add("ct-param-trailing-semi", crlf("Subject: I\nContent-Type: text/plain;\n\nbody"))
	add("ct-multi-param-dup", crlf("Subject: I\nContent-Type: text/plain; charset=a; charset=b\n\nbody"))
	add("ct-upper", crlf("Subject: I\nContent-Type: TEXT/HTML\n\n<i>x</i>"))
	add("ct-text-xml", crlf("Subject: I\nContent-Type: text/xml\n\n<x/>"))
	add("ct-text-plain-extra", crlf("Subject: I\nContent-Type: text/plainish\n\nbody"))
	add("cte-base64", crlf("Subject: B\nContent-Transfer-Encoding: base64\n\naGVsbG8gd29ybGQ=\n"))
	add("cte-base64-multiline", crlf("Subject: B\nContent-Transfer-Encoding: BASE64\n\naGVs\nbG8g\nd29y\nbGQ=\n"))
	add("cte-base64-bad-char", crlf("Subject: B\nContent-Transfer-Encoding: base64\n\naGVs$G8gd29ybGQ=\n"))
	add("cte-base64-bad-padding", crlf("Subject: B\nContent-Transfer-Encoding: base64\n\naGVsbG8gd29ybGQ\n"))
	add("cte-base64-short", crlf("Subject: B\nContent-Transfer-Encoding: base64\n\naGV\n"))
	add("cte-base64-garbage-after-pad", crlf("Subject: B\nContent-Transfer-Encoding: base64\n\naGVsbG8=Zm9v\n"))
	add("cte-base64-pad-only", crlf("Subject: B\nContent-Transfer-Encoding: base64\n\n====\n"))
	add("cte-base64-empty", crlf("Subject: B\nContent-Transfer-Encoding: base64\n\n"))
	add("cte-base64-spaces", crlf("Subject: B\nContent-Transfer-Encoding: base64\n\naGVs bG8=\n"))
	add("cte-base64-html", crlf("Subject: B\nContent-Type: text/html\nContent-Transfer-Encoding: base64\n\nPHA+SGk8L3A+\n"))
	long := strings.Repeat("QUJD", 400)
	add("cte-base64-long", crlf("Subject: B\nContent-Transfer-Encoding: base64\n\n"+long+"\n"))
	add("cte-base64-long-corrupt-700", crlf("Subject: B\nContent-Transfer-Encoding: base64\n\n"+long[:700]+"!"+long[701:]+"\n"))
	add("cte-base64-long-corrupt-1500", crlf("Subject: B\nContent-Transfer-Encoding: base64\n\n"+long[:1500]+"!"+long[1501:]+"\n"))
	add("cte-qp-basic", crlf("Subject: Q\nContent-Transfer-Encoding: quoted-printable\n\nHello =C3=BCber=\n world=21\n"))
	add("cte-qp-lf", "Subject: Q\nContent-Transfer-Encoding: quoted-printable\n\nline one\nline=20two  \nend")
	add("cte-qp-bad-hex", crlf("Subject: Q\nContent-Transfer-Encoding: quoted-printable\n\nbad =ZZ here\n"))
	add("cte-qp-lone-equals", crlf("Subject: Q\nContent-Transfer-Encoding: quoted-printable\n\nx = y\n"))
	add("cte-qp-equals-eof", "Subject: Q\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\nabc=")
	add("cte-qp-equals-garbage", crlf("Subject: Q\nContent-Transfer-Encoding: quoted-printable\n\nabc= x\ndef\n"))
	add("cte-qp-8bit", "Subject: Q\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\nna\xefve caf\xe9\r\n")
	add("cte-qp-control", "Subject: Q\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\nbell\x07 here\r\n")
	add("cte-qp-tab-trailing", "Subject: Q\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\nx\t \r\ny\r\n")
	add("cte-qp-hex-lower", crlf("Subject: Q\nContent-Transfer-Encoding: quoted-printable\n\n=c3=bc =3d\n"))
	add("cte-qp-long-line", "Subject: Q\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n"+strings.Repeat("a", 5000)+"\r\n")
	add("cte-qp-html", crlf("Subject: Q\nContent-Type: text/html\nContent-Transfer-Encoding: quoted-printable\n\n<p class=3D\"x\">Hi</p>\n"))
	add("cte-7bit", crlf("Subject: Q\nContent-Transfer-Encoding: 7bit\n\nplain\n"))
	add("cte-unknown", crlf("Subject: Q\nContent-Transfer-Encoding: x-weird\n\nplain\n"))
	add("cte-padded", crlf("Subject: Q\nContent-Transfer-Encoding:  Base64 \n\naGk=\n"))
	b := "BOUND"
	mp := func(ct string, parts ...string) string {
		s := "Subject: M\nContent-Type: " + ct + "; boundary=" + b + "\n\n"
		for _, p := range parts {
			s += "--" + b + "\n" + p + "\n"
		}
		return crlf(s + "--" + b + "--\n")
	}
	add("mp-alt", mp("multipart/alternative", "Content-Type: text/plain\n\nplain part", "Content-Type: text/html\n\n<p>html part</p>"))
	add("mp-html-only", mp("multipart/alternative", "Content-Type: text/html\n\n<p>only html</p>"))
	add("mp-plain-html-order", mp("multipart/alternative", "Content-Type: text/html\n\n<p>h</p>", "Content-Type: text/plain\n\np"))
	add("mp-two-plain", mp("multipart/mixed", "Content-Type: text/plain\n\none", "Content-Type: text/plain\n\ntwo"))
	add("mp-blank-plain", mp("multipart/mixed", "Content-Type: text/plain\n\n   ", "Content-Type: text/html\n\n<b>x</b>"))
	add("mp-attachment", mp("multipart/mixed", "Content-Type: text/plain\n\nbody", "Content-Type: text/plain\nContent-Disposition: attachment; filename=a.txt\n\nattached"))
	add("mp-attachment-upper", mp("multipart/mixed", "Content-Type: text/plain\nContent-Disposition: ATTACHMENT\n\nsecret", "Content-Type: text/plain\n\nbody"))
	add("mp-inline", mp("multipart/mixed", "Content-Type: text/plain\nContent-Disposition: inline\n\nin"))
	add("mp-no-ct", mp("multipart/mixed", "X-Other: 1\n\nraw part"))
	add("mp-image", mp("multipart/mixed", "Content-Type: image/png\nContent-Transfer-Encoding: base64\n\niVBO", "Content-Type: text/plain\n\ntext"))
	add("mp-part-base64", mp("multipart/mixed", "Content-Type: text/plain\nContent-Transfer-Encoding: base64\n\naGVsbG8=\n"))
	add("mp-part-base64-bad", mp("multipart/mixed", "Content-Type: text/plain\nContent-Transfer-Encoding: base64\n\n!!!!\n"))
	add("mp-part-qp", mp("multipart/mixed", "Content-Type: text/plain\nContent-Transfer-Encoding: quoted-printable\n\nsoft=\nbreak =C3=BC\n"))
	add("mp-part-qp-bad", mp("multipart/mixed", "Content-Type: text/plain\nContent-Transfer-Encoding: quoted-printable\n\nbad =ZZ\n"))
	add("mp-part-qp-html", mp("multipart/alternative", "Content-Type: text/html\nContent-Transfer-Encoding: Quoted-Printable\n\n<p>=3Cb=3E</p>\n"))
	add("mp-nested", mp("multipart/mixed", "Content-Type: multipart/alternative; boundary=INNER\n\n--INNER\nContent-Type: text/plain\n\ninner plain\n--INNER--\n"))
	add("mp-nested-html", mp("multipart/mixed", "Content-Type: multipart/alternative; boundary=INNER\n\n--INNER\nContent-Type: text/html\n\n<p>ih</p>\n--INNER--\n", "Content-Type: text/plain\n\nouter"))
	add("mp-nested-same-boundary", mp("multipart/mixed", "Content-Type: multipart/mixed; boundary=BOUND\n\n--BOUND\nContent-Type: text/plain\n\nx\n--BOUND--\n"))
	add("mp-nested-unterminated", mp("multipart/mixed", "Content-Type: multipart/alternative; boundary=INNER\n\n--INNER\nContent-Type: text/plain\n\ninner"))
	add("mp-missing-boundary-param", crlf("Subject: M\nContent-Type: multipart/mixed\n\n--B\nContent-Type: text/plain\n\nx\n--B--\n"))
	add("mp-empty-boundary", crlf("Subject: M\nContent-Type: multipart/mixed; boundary=\"\"\n\n--\n\nx\n"))
	add("mp-quoted-boundary", crlf("Subject: M\nContent-Type: multipart/mixed; boundary=\"a b'c\"\n\n--a b'c\nContent-Type: text/plain\n\nq\n--a b'c--\n"))
	add("mp-rfc2231-boundary", crlf("Subject: M\nContent-Type: multipart/mixed; boundary*0=BO; boundary*1=UND\n\n--BOUND\nContent-Type: text/plain\n\nc\n--BOUND--\n"))
	add("mp-preamble", crlf("Subject: M\nContent-Type: multipart/mixed; boundary=BOUND\n\npreamble text\nmore\n--BOUND\nContent-Type: text/plain\n\nx\n--BOUND--\nepilogue\n"))
	add("mp-epilogue-garbage-line", "Subject: M\r\nContent-Type: multipart/mixed; boundary=BOUND\r\n\r\n--BOUND\r\nContent-Type: text/plain\r\n\r\nx\r\n--BOUND\r\nContent-Type: text/plain\r\n\r\ny\r\nstray\r\n--BOUND--\r\n")
	add("mp-no-final-boundary", "Subject: M\r\nContent-Type: multipart/mixed; boundary=BOUND\r\n\r\n--BOUND\r\nContent-Type: text/plain\r\n\r\nx\r\n")
	add("mp-no-final-boundary-nl", "Subject: M\r\nContent-Type: multipart/mixed; boundary=BOUND\r\n\r\n--BOUND\r\nContent-Type: text/plain\r\n\r\nx\r\n--BOUND")
	add("mp-final-no-newline", "Subject: M\r\nContent-Type: multipart/mixed; boundary=BOUND\r\n\r\n--BOUND\r\nContent-Type: text/plain\r\n\r\nx\r\n--BOUND--")
	add("mp-lf-only", "Subject: M\nContent-Type: multipart/mixed; boundary=BOUND\n\n--BOUND\nContent-Type: text/plain\n\nlf part\n--BOUND--\n")
	add("mp-lf-two-parts", "Subject: M\nContent-Type: multipart/mixed; boundary=BOUND\n\n--BOUND\nContent-Type: text/plain\n\none\n--BOUND\nContent-Type: text/plain\n\ntwo\n--BOUND--\n")
	add("mp-boundary-trailing-ws", "Subject: M\r\nContent-Type: multipart/mixed; boundary=BOUND\r\n\r\n--BOUND  \t\r\nContent-Type: text/plain\r\n\r\nx\r\n--BOUND--  \r\n")
	add("mp-false-boundary-prefix", "Subject: M\r\nContent-Type: multipart/mixed; boundary=BOUND\r\n\r\n--BOUND\r\nContent-Type: text/plain\r\n\r\nline\r\n--BOUNDARY not a boundary\r\nmore\r\n--BOUND--\r\n")
	add("mp-dash-in-body", "Subject: M\r\nContent-Type: multipart/mixed; boundary=BOUND\r\n\r\n--BOUND\r\nContent-Type: text/plain\r\n\r\n--BOUND-x\r\n--BOUND-\r\n--\r\n--BOUND--\r\n")
	add("mp-empty-part", "Subject: M\r\nContent-Type: multipart/mixed; boundary=BOUND\r\n\r\n--BOUND\r\nContent-Type: text/plain\r\n\r\n\r\n--BOUND--\r\n")
	add("mp-part-immediate-boundary", "Subject: M\r\nContent-Type: multipart/mixed; boundary=BOUND\r\n\r\n--BOUND\r\nContent-Type: text/plain\r\n\r\n--BOUND--\r\n")
	add("mp-no-parts", "Subject: M\r\nContent-Type: multipart/mixed; boundary=BOUND\r\n\r\n--BOUND--\r\n")
	add("mp-empty-body", "Subject: M\r\nContent-Type: multipart/mixed; boundary=BOUND\r\n\r\n")
	add("mp-bad-part-header", "Subject: M\r\nContent-Type: multipart/mixed; boundary=BOUND\r\n\r\n--BOUND\r\nbad header line\r\n\r\nx\r\n--BOUND--\r\n")
	add("mp-part-header-truncated", "Subject: M\r\nContent-Type: multipart/mixed; boundary=BOUND\r\n\r\n--BOUND\r\nContent-Type: text/plain")
	add("mp-part-header-leading-space", "Subject: M\r\nContent-Type: multipart/mixed; boundary=BOUND\r\n\r\n--BOUND\r\n Content-Type: text/plain\r\n\r\nx\r\n--BOUND--\r\n")
	add("mp-part-header-bad-key", "Subject: M\r\nContent-Type: multipart/mixed; boundary=BOUND\r\n\r\n--BOUND\r\nBad Key: v\r\n\r\nx\r\n--BOUND--\r\n")
	add("mp-part-header-bad-value", "Subject: M\r\nContent-Type: multipart/mixed; boundary=BOUND\r\n\r\n--BOUND\r\nX-A: v\x01w\r\n\r\nx\r\n--BOUND--\r\n")
	add("mp-part-header-fold", "Subject: M\r\nContent-Type: multipart/mixed; boundary=BOUND\r\n\r\n--BOUND\r\nContent-Type: text/plain;\r\n charset=utf-8\r\n\r\nfolded\r\n--BOUND--\r\n")
	add("mp-long-preamble-line", "Subject: M\r\nContent-Type: multipart/mixed; boundary=BOUND\r\n\r\n"+strings.Repeat("p", 5000)+"\r\n--BOUND\r\nContent-Type: text/plain\r\n\r\nx\r\n--BOUND--\r\n")
	add("mp-long-epilogue-line", "Subject: M\r\nContent-Type: multipart/mixed; boundary=BOUND\r\n\r\n--BOUND\r\nContent-Type: text/plain\r\n\r\nx\r\n--BOUND--\r\n"+strings.Repeat("e", 5000))
	add("mp-long-body-line", "Subject: M\r\nContent-Type: multipart/mixed; boundary=BOUND\r\n\r\n--BOUND\r\nContent-Type: text/plain\r\n\r\n"+strings.Repeat("b", 9000)+"\r\n--BOUND--\r\n")
	add("mp-large-body", "Subject: M\r\nContent-Type: multipart/mixed; boundary=BOUND\r\n\r\n--BOUND\r\nContent-Type: text/plain\r\n\r\n"+strings.Repeat("line of text\r\n", 3000)+"\r\n--BOUND--\r\n")
	add("mp-nl-before-boundary-only", "Subject: M\r\nContent-Type: multipart/mixed; boundary=BOUND\r\n\r\n\r\n--BOUND\r\nContent-Type: text/plain\r\n\r\nx\r\n--BOUND--\r\n")
	add("mp-expecting-new-part", "Subject: M\r\nContent-Type: multipart/mixed; boundary=BOUND\r\n\r\n--BOUND\r\nContent-Type: text/plain\r\n\r\nx\r\n\r\njunk\r\n--BOUND--\r\n")
	add("mp-three-parts-mixed", mp("multipart/mixed", "Content-Type: text/plain\n\na", "Content-Type: image/gif\n\nGIF", "Content-Type: text/html\n\n<i>h</i>"))
	add("mp-content-disposition-bad", mp("multipart/mixed", "Content-Type: text/plain\nContent-Disposition: ;;;\n\nbody"))
	add("mp-boundary-case", crlf("Subject: M\nContent-Type: Multipart/Mixed; BOUNDARY=BOUND\n\n--BOUND\nContent-Type: text/plain\n\ncase\n--BOUND--\n"))
	add("mp-dup-boundary-params", crlf("Subject: M\nContent-Type: multipart/mixed; boundary=A; boundary=B\n\n--A\n\nx\n--A--\n"))
	add("mp-binary-body", "Subject: S\r\nContent-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\n\r\n\x00\x01\xff\r\n--B--\r\n")
	return m
}

func TestPortIngestStructuredContract(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	ctx := context.Background()
	root := t.TempDir()
	cases := []structuredCase{}

	record := func(c structuredCase, f func() (string, error)) {
		c.Text, c.Error, c.Panic = capture(f)
		cases = append(cases, c)
	}
	// Direct XML text functions over raw bytes.
	direct := func(id, reader string, in string, shared []string) {
		c := structuredCase{ID: id, Group: "xml-direct", Reader: reader, Input: []byte(in), Shared: shared}
		switch reader {
		case "word":
			record(c, func() (string, error) { return wordXMLToText(strings.NewReader(in)) })
		case "odf":
			record(c, func() (string, error) { return odfXMLToText(strings.NewReader(in)) })
		case "pptx":
			record(c, func() (string, error) { return pptxXMLToText(strings.NewReader(in)) })
		case "sheet":
			record(c, func() (string, error) { return sheetXMLToText(strings.NewReader(in), shared) })
		}
	}
	readers := []string{"word", "odf", "pptx", "sheet"}
	for i, s := range xmlSnippets() {
		for _, r := range readers {
			direct(fmt.Sprintf("snippet-%03d-%s", i, r), r, s, []string{"shared-zero", "pipe|one"})
		}
	}
	seeds := xmlSeeds()
	for si, s := range seeds {
		r := readers[si]
		for n := 0; n <= len(s); n++ {
			direct(fmt.Sprintf("trunc-%d-%03d-%s", si, n, r), r, s[:n], []string{"A", "B"})
		}
		for mi, m := range mutateBytes(s, 90) {
			direct(fmt.Sprintf("mut-%d-%03d-%s", si, mi, r), r, m, []string{"A", "B"})
		}
	}
	// Cross-reader mutations on the first seed only keep file size bounded.
	for _, r := range readers {
		if r == "word" {
			continue
		}
		for mi, m := range mutateBytes(seeds[0], 30) {
			direct(fmt.Sprintf("xmut-%03d-%s", mi, r), r, m, []string{"A"})
		}
	}

	// htmlToText and NormalizeText.
	htmls := []string{
		"", "plain", "<p>a</p><p>b</p>", "a<br>b<BR/>c<br  />d", "<div>x</div><li>y</li><h3>z</h3><tr>r</tr>", "<td>a</td><th>b</th>", "<script>x</script>y", "<SCRIPT type=t>x\n</SCRIPT>y", "<style>s</style>t", "<script>unterminated",
		"&amp;&lt;&gt;&quot;&apos;", "&copy; &reg; &nbsp;|&euro;", "&copy &amp &lt", "&ampx &notit; &notin;", "&#65;&#x41;&#X41;", "&#0;&#x0;&#128;&#150;&#159;", "&#xD800;&#xDFFF;&#x110000;", "&#1114111;", "&#99999999999;", "&#;", "&#x;", "&#65", "&#65x", "&unknown;", "&", "&&amp;", "&Aacute;&aacute;&Auml;&auml;&szlig;", "&hellip;&mdash;&ndash;&bull;", "&ThickSpace;&NotEqualTilde;", "&fjlig;&acE;",
		"<a href=\"x\">link</a>", "<a title='>'>t</a>", "<!-- c -->t", "<!DOCTYPE html><p>d</p>", "<p>unclosed", "<<p>>", "a < b > c", "1<2", "<", ">", "<>", "<p", "x<y",
		"<p>one</p>\n<p>two</p>", "<td>1</td>\t<td>2</td>", "line1\r\nline2\rline3", "tab\there", "multi\n\n\n\nblank", "  lead and trail  ", "dehyph-\nenated word", "docu-\r\nment", "a\u00a0b", "caf\u00e9 \u4e2d\u6587",
		"<p>&lt;script&gt;alert(1)&lt;/script&gt;</p>", "<![CDATA[x]]>", "<?php x ?>y", "<br\t/>x", "<BR>y", "<p>a</P>b",
		"\xff\xfe invalid", "bad \xc3 utf8",
	}
	for i, h := range htmls {
		c := structuredCase{ID: fmt.Sprintf("html-%03d", i), Group: "html", Reader: "htmlToText", Input: []byte(h)}
		record(c, func() (string, error) { return htmlToText(h), nil })
		n := structuredCase{ID: fmt.Sprintf("normalize-%03d", i), Group: "normalize", Reader: "NormalizeText", Input: []byte(h)}
		record(n, func() (string, error) { return NormalizeText(h), nil })
	}

	writeKind := func(id, group, name string, data []byte, kind Kind) {
		p := filepath.Join(root, fmt.Sprintf("case-%d", len(cases)))
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		p = filepath.Join(p, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		c := structuredCase{ID: id, Group: group, Reader: "ReadStructuredKind", Name: name, Input: data, Kind: kind}
		k, de := Detect(p)
		c.Detect = k
		if de != nil {
			c.DetectErr = strings.ReplaceAll(de.Error(), p, "$PATH")
		}
		var res *Result
		text, errText, pan := capture(func() (string, error) {
			r, e := ReadStructuredKind(ctx, p, kind)
			res = r
			if e != nil {
				return "", e
			}
			return "", nil
		})
		_ = text
		c.Result = res
		c.Error = strings.ReplaceAll(errText, p, "$PATH")
		c.Panic = pan
		cases = append(cases, c)
	}

	// EML.
	emls := emlMessages()
	ids := make([]string, 0, len(emls))
	for id := range emls {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		writeKind("eml-"+id, "eml", "message.eml", []byte(emls[id]), KindEML)
	}

	// Archives with realistic part layouts.
	wordDoc := func(body string) string {
		return `<?xml version="1.0" encoding="UTF-8"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` + body + `</w:body></w:document>`
	}
	docx := func(parts map[string]string) []byte { return portZip(t, parts) }
	writeKind("docx-basic", "docx", "file.docx", docx(map[string]string{"word/document.xml": wordDoc(`<w:p><w:r><w:t>Hello</w:t></w:r></w:p><w:p><w:r><w:t>World</w:t></w:r></w:p>`)}), KindDOCX)
	writeKind("docx-table", "docx", "file.docx", docx(map[string]string{"word/document.xml": wordDoc(`<w:tbl><w:tr><w:tc><w:p><w:r><w:t>H1</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>H2</w:t></w:r></w:p></w:tc></w:tr><w:tr><w:tc><w:p><w:r><w:t>a|b</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>c</w:t></w:r></w:p></w:tc></w:tr></w:tbl><w:p><w:r><w:t>after</w:t></w:r></w:p>`)}), KindDOCX)
	writeKind("docx-empty-row-table", "docx", "file.docx", docx(map[string]string{"word/document.xml": wordDoc(`<w:tbl><w:tr></w:tr></w:tbl>`)}), KindDOCX)
	writeKind("docx-missing", "docx", "file.docx", docx(map[string]string{"word/other.xml": "<x/>", "[Content_Types].xml": `<Types><Override ContentType="wordprocessingml.document.main+xml"/></Types>`}), KindDOCX)
	writeKind("docx-malformed", "docx", "file.docx", docx(map[string]string{"word/document.xml": wordDoc(`<w:p><w:r><w:t>x`)}), KindDOCX)
	writeKind("docx-not-zip", "docx", "file.docx", []byte("not a zip"), KindDOCX)
	writeKind("docx-entity-error", "docx", "file.docx", docx(map[string]string{"word/document.xml": wordDoc(`<w:p><w:r><w:t>a &bad; b</w:t></w:r></w:p>`)}), KindDOCX)

	shared := `<sst><si><t>Alpha</t></si><si><r><t>Be</t></r><r><t>ta</t></r></si><si><t>pipe|x</t></si></sst>`
	wb := `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Data" sheetId="1" r:id="rId1"/><sheet name="Other" sheetId="2" r:id="rId2"/></sheets></workbook>`
	rels := `<Relationships><Relationship Id="rId1" Type="t" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Type="t" Target="/xl/worksheets/sheet2.xml"/></Relationships>`
	sheet1 := `<worksheet><sheetData><row><c t="s"><v>0</v></c><c t="s"><v>1</v></c></row><row><c><v>1</v></c><c t="s"><v>2</v></c></row></sheetData></worksheet>`
	sheet2 := `<worksheet><sheetData><row><c><v>x</v></c></row></sheetData></worksheet>`
	writeKind("xlsx-basic", "xlsx", "file.xlsx", portZip(t, map[string]string{"xl/sharedStrings.xml": shared, "xl/workbook.xml": wb, "xl/_rels/workbook.xml.rels": rels, "xl/worksheets/sheet1.xml": sheet1, "xl/worksheets/sheet2.xml": sheet2}), KindXLSX)
	writeKind("xlsx-no-workbook", "xlsx", "file.xlsx", portZip(t, map[string]string{"xl/sharedStrings.xml": shared, "xl/worksheets/sheet1.xml": sheet1, "xl/worksheets/sheet10.xml": sheet2, "xl/worksheets/sheet2.xml": sheet2}), KindXLSX)
	writeKind("xlsx-no-shared", "xlsx", "file.xlsx", portZip(t, map[string]string{"xl/worksheets/sheet1.xml": `<worksheet><sheetData><row><c t="s"><v>0</v></c><c t="inlineStr"><is><t>in</t></is></c></row></sheetData></worksheet>`}), KindXLSX)
	writeKind("xlsx-bad-shared", "xlsx", "file.xlsx", portZip(t, map[string]string{"xl/sharedStrings.xml": `<sst><si><t>x`, "xl/worksheets/sheet1.xml": sheet2}), KindXLSX)
	writeKind("xlsx-bad-workbook", "xlsx", "file.xlsx", portZip(t, map[string]string{"xl/workbook.xml": `<workbook><sheets>`, "xl/worksheets/sheet1.xml": sheet2}), KindXLSX)
	writeKind("xlsx-bad-rels", "xlsx", "file.xlsx", portZip(t, map[string]string{"xl/workbook.xml": wb, "xl/_rels/workbook.xml.rels": `<Relationships><Relationship`, "xl/worksheets/sheet1.xml": sheet2}), KindXLSX)
	writeKind("xlsx-bad-sheet", "xlsx", "file.xlsx", portZip(t, map[string]string{"xl/worksheets/sheet1.xml": `<worksheet><sheetData><row><c><v>1</v></c>`}), KindXLSX)
	writeKind("xlsx-empty-rows", "xlsx", "file.xlsx", portZip(t, map[string]string{"xl/worksheets/sheet1.xml": `<worksheet><sheetData><row/></sheetData></worksheet>`}), KindXLSX)
	writeKind("xlsx-blank-sheet", "xlsx", "file.xlsx", portZip(t, map[string]string{"xl/worksheets/sheet1.xml": `<worksheet><sheetData/></worksheet>`, "xl/worksheets/sheet2.xml": sheet2}), KindXLSX)
	writeKind("xlsx-no-sheets", "xlsx", "file.xlsx", portZip(t, map[string]string{"xl/workbook.xml": wb}), KindXLSX)
	writeKind("xlsx-sheet-title-dup", "xlsx", "file.xlsx", portZip(t, map[string]string{"xl/workbook.xml": `<workbook xmlns:r="u"><sheets><sheet name="A" r:id="r1"/><sheet name="B" r:id="r1"/></sheets></workbook>`, "xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="r1" Target="sheets/sheet1.xml/"/></Relationships>`, "xl/worksheets/sheet1.xml": sheet2}), KindXLSX)

	// PPTX / ODF / EPUB with Go's own readers.
	slide := func(text string) string {
		return `<p:sld xmlns:a="u" xmlns:p="v"><p:cSld><a:p><a:r><a:t>` + text + `</a:t></a:r></a:p></p:cSld></p:sld>`
	}
	writeKind("pptx-basic", "pptx", "file.pptx", portZip(t, map[string]string{"ppt/slides/slide1.xml": slide("One"), "ppt/slides/slide2.xml": slide("Two"), "ppt/slides/slide10.xml": slide("Ten"), "ppt/slides/_rels/slide1.xml.rels": "<x/>", "ppt/slides/other.txt": "ignored"}), KindPPTX)
	writeKind("pptx-blank-slide", "pptx", "file.pptx", portZip(t, map[string]string{"ppt/slides/slide1.xml": slide("   "), "ppt/slides/slide2.xml": slide("Two")}), KindPPTX)
	writeKind("pptx-bad-slide", "pptx", "file.pptx", portZip(t, map[string]string{"ppt/slides/slide1.xml": slide("One") + "<x", "ppt/slides/slide2.xml": slide("Two")}), KindPPTX)
	writeKind("odt-content", "odt", "file.odt", portZip(t, map[string]string{"mimetype": string(KindODT), "content.xml": `<office:document-content xmlns:office="u" xmlns:text="v"><office:body><text:p>A</text:p><text:p>B<text:tab/>C</text:p></office:body></office:document-content>`}), KindODT)
	writeKind("ods-content", "ods", "file.ods", portZip(t, map[string]string{"content.xml": `<c><p>cell</p><p>two</p></c>`}), KindODS)
	writeKind("epub-chapters", "epub", "file.epub", portZip(t, map[string]string{"mimetype": string(KindEPUB), "OEBPS/ch2.xhtml": "<p>two &amp; more</p>", "OEBPS/ch1.html": "<h1>one</h1><p>text</p>", "OEBPS/ch3.HTM": "<p>caps</p>", "OEBPS/toc.ncx": "<p>skip</p>", "OEBPS/styles.css": "p{}", "META-INF/container.xml": "<c/>", "OEBPS/blank.html": "   "}), KindEPUB)
	writeKind("epub-rights", "epub", "file.epub", portZip(t, map[string]string{"mimetype": string(KindEPUB), "META-INF/rights.xml": "<r/>", "OEBPS/a.html": "x"}), KindEPUB)

	// ZIP defence (SEC-001).
	body := wordDoc(`<w:p><w:r><w:t>zip ok</w:t></w:r></w:p>`)
	good := deflateBytes(t, []byte(body))
	crc := crc32.ChecksumIEEE([]byte(body))
	writeKind("zip-good-raw", "zip", "file.docx", rawZip(t, []rawEntry{{name: "word/document.xml", method: zip.Deflate, crc: crc, data: good, usize: uint64(len(body))}}), KindDOCX)
	writeKind("zip-bad-crc", "zip", "file.docx", rawZip(t, []rawEntry{{name: "word/document.xml", method: zip.Deflate, crc: crc ^ 1, data: good, usize: uint64(len(body))}}), KindDOCX)
	writeKind("zip-store-bad-crc", "zip", "file.docx", rawZip(t, []rawEntry{{name: "word/document.xml", method: zip.Store, crc: crc ^ 1, data: []byte(body), usize: uint64(len(body))}}), KindDOCX)
	writeKind("zip-store-ok", "zip", "file.docx", rawZip(t, []rawEntry{{name: "word/document.xml", method: zip.Store, crc: crc, data: []byte(body), usize: uint64(len(body))}}), KindDOCX)
	writeKind("zip-truncated-deflate", "zip", "file.docx", rawZip(t, []rawEntry{{name: "word/document.xml", method: zip.Deflate, crc: crc, data: good[:len(good)/2], usize: uint64(len(body))}}), KindDOCX)
	writeKind("zip-garbage-deflate", "zip", "file.docx", rawZip(t, []rawEntry{{name: "word/document.xml", method: zip.Deflate, crc: crc, data: bytes.Repeat([]byte{0xff}, 40), usize: uint64(len(body))}}), KindDOCX)
	writeKind("zip-method-99", "zip", "file.docx", rawZip(t, []rawEntry{{name: "word/document.xml", method: 99, crc: crc, data: good, usize: uint64(len(body))}}), KindDOCX)
	writeKind("zip-method-bzip2", "zip", "file.docx", rawZip(t, []rawEntry{{name: "word/document.xml", method: 12, crc: crc, data: good, usize: uint64(len(body))}}), KindDOCX)
	writeKind("zip-encrypted-flag", "zip", "file.docx", rawZip(t, []rawEntry{{name: "word/document.xml", method: zip.Deflate, flags: 1, crc: crc, data: good, usize: uint64(len(body))}}), KindDOCX)
	writeKind("zip-size-lie-small", "zip", "file.docx", rawZip(t, []rawEntry{{name: "word/document.xml", method: zip.Deflate, crc: crc, data: good, usize: 3}}), KindDOCX)
	writeKind("zip-size-lie-huge", "zip", "file.docx", rawZip(t, []rawEntry{{name: "word/document.xml", method: zip.Deflate, crc: crc, data: good, usize: 1 << 40}}), KindDOCX)
	writeKind("zip-traversal-name", "zip", "file.docx", portZip(t, map[string]string{"../word/document.xml": body, "word/document.xml": body}), KindDOCX)
	writeKind("zip-absolute-name", "zip", "file.docx", portZip(t, map[string]string{"/word/document.xml": body}), KindDOCX)
	writeKind("zip-backslash-name", "zip", "file.docx", portZip(t, map[string]string{"word\\document.xml": body}), KindDOCX)
	writeKind("zip-dir-entry", "zip", "file.docx", portZip(t, map[string]string{"word/": "", "word/document.xml": body}), KindDOCX)
	writeKind("zip-duplicate-name", "zip", "file.docx", rawZip(t, []rawEntry{
		{name: "word/document.xml", method: zip.Store, crc: crc32.ChecksumIEEE([]byte("<d><t>first</t></d>")), data: []byte("<d><t>first</t></d>"), usize: 19},
		{name: "word/document.xml", method: zip.Store, crc: crc32.ChecksumIEEE([]byte("<d><t>second</t></d>")), data: []byte("<d><t>second</t></d>"), usize: 20}}), KindDOCX)
	writeKind("zip-empty-archive", "zip", "file.docx", portZip(t, map[string]string{}), KindDOCX)
	writeKind("zip-trailing-garbage", "zip", "file.docx", append(portZip(t, map[string]string{"word/document.xml": body}), []byte("TRAILING GARBAGE")...), KindDOCX)
	writeKind("zip-leading-garbage", "zip", "file.docx", append([]byte("LEADING"), portZip(t, map[string]string{"word/document.xml": body})...), KindDOCX)
	truncZip := portZip(t, map[string]string{"word/document.xml": body})
	writeKind("zip-truncated-archive", "zip", "file.docx", truncZip[:len(truncZip)-10], KindDOCX)
	writeKind("zip-truncated-half", "zip", "file.docx", truncZip[:len(truncZip)/2], KindDOCX)
	corrupt := append([]byte(nil), truncZip...)
	corrupt[len(corrupt)/3] ^= 0xff
	writeKind("zip-corrupt-byte", "zip", "file.docx", corrupt, KindDOCX)
	eocdOnly := []byte("PK\x05\x06\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00")
	writeKind("zip-eocd-only", "zip", "file.docx", eocdOnly, KindDOCX)
	writeKind("zip-eocd-comment", "zip", "file.docx", append(append([]byte(nil), truncZip...), 0), KindDOCX)
	// Entry-size boundaries and amplification.
	for _, n := range []int{maxZipEntryBytes - 1, maxZipEntryBytes, maxZipEntryBytes + 1} {
		pad := strings.Repeat(" ", n-len(`<d></d>`))
		writeKind(fmt.Sprintf("zip-entry-%d", n), "zip", "file.odt", portZip(t, map[string]string{"mimetype": string(KindODT), "content.xml": "<d>" + pad + "</d>"}), KindODT)
	}
	// Aggregate: five 30 MiB sheets cross the 128 MiB total cap; four do not.
	sheetPad := func(n int) string {
		return "<worksheet><sheetData>" + strings.Repeat(" ", n) + "</sheetData></worksheet>"
	}
	agg := func(n int) map[string]string {
		m := map[string]string{}
		for i := 1; i <= n; i++ {
			m[fmt.Sprintf("xl/worksheets/sheet%d.xml", i)] = sheetPad(30 << 20)
		}
		return m
	}
	writeKind("zip-aggregate-4x30MiB", "zip", "file.xlsx", portZip(t, agg(4)), KindXLSX)
	writeKind("zip-aggregate-5x30MiB", "zip", "file.xlsx", portZip(t, agg(5)), KindXLSX)
	exact := map[string]string{}
	for i := 1; i <= 4; i++ {
		exact[fmt.Sprintf("xl/worksheets/sheet%d.xml", i)] = sheetPad(32<<20 - len("<worksheet><sheetData></sheetData></worksheet>"))
	}
	writeKind("zip-aggregate-4x32MiB-exact", "zip", "file.xlsx", portZip(t, exact), KindXLSX)
	exact["xl/worksheets/sheet5.xml"] = "<w/>"
	writeKind("zip-aggregate-over-by-4", "zip", "file.xlsx", portZip(t, exact), KindXLSX)
	// Entry count caps.
	for _, n := range []int{maxZipEntries - 1, maxZipEntries, maxZipEntries + 1} {
		m := map[string]string{"word/document.xml": body}
		for i := 0; len(m) < n; i++ {
			m[fmt.Sprintf("pad/%05d", i)] = ""
		}
		writeKind(fmt.Sprintf("zip-entries-%d", n), "zip", "file.docx", portZip(t, m), KindDOCX)
	}
	// Nested archive inside an archive is just bytes: never recursed.
	writeKind("zip-nested-bomb-ignored", "zip", "file.docx", portZip(t, map[string]string{"word/document.xml": body, "inner.zip": string(truncZip)}), KindDOCX)

	// Malformed corpus shipped with the Go tests.
	corpus := filepath.Join(filepath.Dir(sourceFile()), "testdata", "malformed")
	_ = filepath.Walk(corpus, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		data, e := os.ReadFile(p) //nolint:gosec // repository corpus path
		if e != nil {
			t.Fatal(e)
		}
		kind, _ := Detect(p)
		if kind == KindUnknown {
			ext := strings.TrimPrefix(filepath.Ext(p), ".")
			switch ext {
			case "docx":
				kind = KindDOCX
			case "xlsx":
				kind = KindXLSX
			case "eml":
				kind = KindEML
			case "html":
				kind = KindHTML
			case "rtf":
				kind = KindRTF
			case "epub":
				kind = KindEPUB
			case "odt":
				kind = KindODT
			case "ods":
				kind = KindODS
			case "odp":
				kind = KindODP
			case "pptx":
				kind = KindPPTX
			}
		}
		rel, _ := filepath.Rel(corpus, p)
		writeKind("corpus-"+strings.ReplaceAll(rel, "/", "-"), "corpus", filepath.Base(p), data, kind)
		return nil
	})
	sort.SliceStable(cases, func(i, j int) bool { return cases[i].ID < cases[j].ID })

	hashes := map[string]string{}
	src := filepath.Dir(sourceFile())
	for _, f := range []string{"structured.go", "containers.go", "limits.go", "extract.go", "structured_port_test.go"} {
		b, e := os.ReadFile(filepath.Join(src, f)) //nolint:gosec // repository source
		if e != nil {
			t.Fatal(e)
		}
		hashes[f] = fmt.Sprintf("%x", sha256.Sum256(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))))
	}
	counts := map[string]int{}
	for _, c := range cases {
		counts[c.Group]++
	}
	content, e := json.MarshalIndent(map[string]any{
		"schema_version": 1,
		"oracle":         fixtureoracle.Current(),
		"source_hashes":  hashes,
		"counts":         counts,
		"cases":          cases,
		"normalizations": []string{"source path in diagnostics replaced by $PATH", "invalid UTF-8 in Go result strings encoded using encoding/json replacement semantics"},
	}, "", " ")
	if e != nil {
		t.Fatal(e)
	}
	content = append(content, '\n')
	out := filepath.Join(src, "../../../../testdata/port/ingest/structured.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if e = os.WriteFile(out, content, 0o600); e != nil {
			t.Fatal(e)
		}
	} else {
		old, e := os.ReadFile(out) //nolint:gosec // repository fixture
		if e != nil {
			t.Fatal(e)
		}
		if !bytes.Equal(old, content) {
			t.Fatal("ingest structured fixture drift; regenerate deliberately")
		}
	}
	t.Logf("replayed %d structured cases %v", len(cases), counts)
}

func sourceFile() string {
	_, file, _, _ := runtime.Caller(0)
	return file
}
