package export

import (
	"strconv"
	"strings"
)

func vaultFile(path, content string) exportVaultFile {
	return exportVaultFile{Path: path, Content: content}
}

func noteCase(id, note, format string, files ...exportVaultFile) exportVaultCase {
	return exportVaultCase{ID: id, Files: files, Note: strPtr(note), Format: format}
}

func viewCase(id, name string, columns []string, rows []map[string]string, format string, files ...exportVaultFile) exportVaultCase {
	return exportVaultCase{ID: id, Files: files, View: &exportVaultView{Name: name, Columns: columns}, Rows: rows, Format: format}
}

func row(pairs ...string) map[string]string {
	out := map[string]string{}
	for i := 0; i+1 < len(pairs); i += 2 {
		out[pairs[i]] = pairs[i+1]
	}
	return out
}

// exportVaultCorpus never contains an embed cycle: Go's resolveTransclusions
// does not terminate on one.
func exportVaultCorpus() []exportVaultCase {
	const (
		plain    = "---\ntitle: Plain Note\ntags: [a, b]\n---\nFirst paragraph.\n\n- item <one>\n* item two & three\n"
		headed   = "# Heading Title\n\nBody under heading.\n"
		untitled = "Just text without any title.\n"
	)
	var cases []exportVaultCase

	// Formats of a plain note.
	for _, format := range []string{"", "markdown", "html", "pdf", "docx"} {
		name := format
		if name == "" {
			name = "default"
		}
		cases = append(cases, noteCase("note-frontmatter-"+name, "a.md", format, vaultFile("a.md", plain)))
	}
	cases = append(cases,
		noteCase("note-heading-title-markdown", "a.md", "", vaultFile("a.md", headed)),
		noteCase("note-heading-title-pdf", "a.md", "pdf", vaultFile("a.md", headed)),
		noteCase("note-untitled-markdown", "sub/dir/untitled.md", "", vaultFile("sub/dir/untitled.md", untitled)),
		noteCase("note-untitled-pdf", "sub/dir/untitled.md", "pdf", vaultFile("sub/dir/untitled.md", untitled)),
		noteCase("note-untitled-html", "sub/dir/untitled.md", "html", vaultFile("sub/dir/untitled.md", untitled)),
		noteCase("note-empty-file-markdown", "empty.md", "", vaultFile("empty.md", "")),
		noteCase("note-empty-file-pdf", "empty.md", "pdf", vaultFile("empty.md", "")),
		noteCase("note-pdf-profile-fields", "letter.md", "pdf", vaultFile("letter.md", "---\ntitle: Antrag\nlang: de\nrecipient:\n  name: Amt\n  street: Hauptstraße 1\n---\nSehr geehrte Damen und Herren,\n")),
		noteCase("note-pdf-explicit-empty-title", "t.md", "pdf", vaultFile("t.md", "---\ntitle: \"\"\ntags: [x]\n---\nbody\n")),
		noteCase("note-crlf-html", "crlf.md", "html", vaultFile("crlf.md", "---\r\ntitle: CRLF\r\n---\r\n# Head\r\n\r\n- a\r\n- b\r\n\r\ntext\r\n")),
		noteCase("note-unicode-html", "u.md", "html", vaultFile("u.md", "---\ntitle: Übersicht <&>\n---\nGrüße — „Zitat“ 😀 & <b>fett</b>\n")),
		noteCase("note-html-lists-and-headings", "h.md", "html", vaultFile("h.md", "---\ntitle: Lists\n---\n## Two\n### Three\n#### Four\n- a\n- b\n\n* c\ntext\n- d\n")),
		noteCase("note-leading-slash-path", "/a.md", "", vaultFile("a.md", plain)),
		noteCase("note-dot-segments-inside", "sub/../a.md", "", vaultFile("a.md", plain), vaultFile("sub/x.md", "x")),
		noteCase("note-body-without-trailing-newline", "n.md", "", vaultFile("n.md", "---\ntitle: N\n---\nno newline at end")),
		noteCase("note-frontmatter-only", "f.md", "", vaultFile("f.md", "---\ntitle: Only Frontmatter\n---\n")),
		noteCase("note-unterminated-frontmatter", "u.md", "", vaultFile("u.md", "---\ntitle: Open\nbody never closes\n")),
	)

	// Errors of the top-level note.
	cases = append(cases,
		noteCase("note-missing", "missing.md", ""),
		noteCase("note-root-itself", ".", ""),
		noteCase("note-empty-path", "", "pdf"),
		noteCase("note-through-file-component", "a.md/b.md", "", vaultFile("a.md", "plain")),
		noteCase("note-missing-in-missing-dir", "no/such/dir/x.md", "pdf"),
		noteCase("note-traversal", "../outside/secret.md", ""),
		noteCase("note-traversal-deep", "a/../../outside/secret.md", "html"),
		noteCase("note-traversal-from-subdir", "sub/../../outside/secret.md", ""),
		exportVaultCase{ID: "note-directory", Dirs: []string{"folder.md"}, Note: strPtr("folder.md")},
		noteCase("note-duplicate-frontmatter-key", "d.md", "", vaultFile("d.md", "---\ntitle: One\ntitle: Two\n---\nbody\n")),
		exportVaultCase{ID: "note-over-read-limit", Files: []exportVaultFile{{Path: "big.md", Sparse: 64<<20 + 1}}, Note: strPtr("big.md")},
		exportVaultCase{ID: "note-at-read-limit-is-not-rejected-by-size", Files: []exportVaultFile{{Path: "limit.md", Sparse: 1 << 20}}, Note: strPtr("limit.md"), Format: "html"},
		exportVaultCase{
			ID: "note-symlink-escape-file", Note: strPtr("link.md"),
			Symlinks: []exportVaultLink{{Path: "link.md", Target: exportVaultOutsideToken + "/secret.md"}},
		},
		exportVaultCase{
			ID: "note-symlink-escape-dir", Note: strPtr("linkdir/secret.md"),
			Symlinks: []exportVaultLink{{Path: "linkdir", Target: exportVaultOutsideToken}},
		},
		exportVaultCase{
			ID: "note-symlink-contained", Note: strPtr("alias.md"), Files: []exportVaultFile{vaultFile("real/target.md", plain)},
			Symlinks: []exportVaultLink{{Path: "alias.md", Target: "real/target.md"}},
		},
		exportVaultCase{
			ID: "note-symlink-contained-dir", Note: strPtr("alias/target.md"), Format: "pdf", Files: []exportVaultFile{vaultFile("real/target.md", headed)},
			Symlinks: []exportVaultLink{{Path: "alias", Target: "real"}},
		},
		exportVaultCase{
			ID: "note-symlink-dangling", Note: strPtr("dangling.md"),
			Symlinks: []exportVaultLink{{Path: "dangling.md", Target: "nowhere.md"}},
		},
	)

	// Transclusion expansion.
	cases = append(cases,
		noteCase("embed-simple", "host.md", "", vaultFile("host.md", "---\ntitle: Host\n---\nBefore\n![[child]]\nAfter\n"), vaultFile("child.md", "---\ntitle: Child\n---\nChild body.\n")),
		noteCase("embed-with-md-suffix", "host.md", "", vaultFile("host.md", "![[child.md]]\n"), vaultFile("child.md", "Child text\n")),
		noteCase("embed-heading-fragment-ignored", "host.md", "", vaultFile("host.md", "![[child#Section]]\n"), vaultFile("child.md", "# Whole\n\nAll of it\n")),
		noteCase("embed-block-fragment-ignored", "host.md", "", vaultFile("host.md", "![[child#^abc123]]\n"), vaultFile("child.md", "para ^abc123\n")),
		noteCase("embed-trimmed-reference", "host.md", "", vaultFile("host.md", "![[  child  ]]\n"), vaultFile("child.md", "trimmed\n")),
		noteCase("embed-in-subfolder", "host.md", "", vaultFile("host.md", "![[sub/deep/child]]\n"), vaultFile("sub/deep/child.md", "deep child\n")),
		noteCase("embed-relative-to-vault-not-host", "sub/host.md", "", vaultFile("sub/host.md", "![[sibling]]\n"), vaultFile("sub/sibling.md", "sibling\n"), vaultFile("sibling.md", "ROOT sibling\n")),
		noteCase("embed-nested-three-levels", "a.md", "", vaultFile("a.md", "A\n![[b]]\n"), vaultFile("b.md", "B\n![[c]]\n"), vaultFile("c.md", "C\n![[d]]\n"), vaultFile("d.md", "D end\n")),
		noteCase("embed-same-note-twice", "host.md", "", vaultFile("host.md", "![[c]] and ![[c]]\n"), vaultFile("c.md", "once")),
		noteCase("embed-diamond", "top.md", "", vaultFile("top.md", "![[l]]\n![[r]]\n"), vaultFile("l.md", "L ![[leaf]]\n"), vaultFile("r.md", "R ![[leaf]]\n"), vaultFile("leaf.md", "leaf\n")),
		noteCase("embed-adjacent", "host.md", "", vaultFile("host.md", "![[a]]![[b]]"), vaultFile("a.md", "A"), vaultFile("b.md", "B")),
		noteCase("embed-empty-body", "host.md", "", vaultFile("host.md", "x![[e]]y\n"), vaultFile("e.md", "")),
		noteCase("embed-strips-child-frontmatter", "host.md", "", vaultFile("host.md", "![[c]]\n"), vaultFile("c.md", "---\ntitle: Hidden\nstatus: draft\n---\nVisible\n")),
		noteCase("embed-in-code-fence-still-expands", "host.md", "", vaultFile("host.md", "```\n![[c]]\n```\n"), vaultFile("c.md", "expanded")),
		noteCase("embed-multiline-reference-no-match", "host.md", "", vaultFile("host.md", "![[a\nb]]\n"), vaultFile("a\nb.md", "newline name")),
		noteCase("embed-empty-reference-no-match", "host.md", "", vaultFile("host.md", "![[]] text\n")),
		noteCase("embed-bracket-in-reference-no-match", "host.md", "", vaultFile("host.md", "![[a]b]] and ![[c]]\n"), vaultFile("c.md", "C")),
		noteCase("embed-joins-with-preceding-bang", "host.md", "", vaultFile("host.md", "!![[c]]"), vaultFile("c.md", "[[d]]"), vaultFile("d.md", "D")),
		noteCase("embed-joins-with-preceding-bracket-pair", "host.md", "", vaultFile("host.md", "![![[c]]"), vaultFile("c.md", "[d]]"), vaultFile("d.md", "D")),
		noteCase("embed-joins-with-preceding-bracket", "host.md", "", vaultFile("host.md", "!?![[c]]"), vaultFile("c.md", "[[d]]"), vaultFile("d.md", "D")),
		noteCase("embed-replacement-opens-later-embed", "host.md", "", vaultFile("host.md", "![[c]]d]]"), vaultFile("c.md", "![[x"), vaultFile("x]]d.md", "joined")),
		// A replacement that starts with (or is empty before) "]" completes a
		// "![[x]" candidate that began earlier: Go rescans from 0 every iteration.
		noteCase("embed-replacement-closes-earlier-candidate", "host.md", "", vaultFile("host.md", "![[a]![[c]]"), vaultFile("a.md", "A"), vaultFile("c.md", "]x")),
		noteCase("embed-empty-replacement-closes-earlier-candidate", "host.md", "", vaultFile("host.md", "![[a]![[c]]]"), vaultFile("a.md", "A"), vaultFile("c.md", "")),
		noteCase("embed-bracket-only-replacement-closes-earlier-candidate", "host.md", "", vaultFile("host.md", "![[a]![[c]]"), vaultFile("a.md", "A"), vaultFile("c.md", "]")),
		noteCase("embed-closing-replacement-with-later-embed", "host.md", "", vaultFile("host.md", "![[a]![[c]]![[d]]"), vaultFile("a.md", "A"), vaultFile("c.md", "]"), vaultFile("d.md", "D")),
		noteCase("embed-closing-replacement-after-prefix-text", "host.md", "", vaultFile("host.md", "pre ![[a]mid ![[c]] post"), vaultFile("a.md", "A"), vaultFile("c.md", "]")),
		noteCase("embed-closing-replacement-without-earlier-candidate", "host.md", "", vaultFile("host.md", "a]![[c]]"), vaultFile("c.md", "]x")),
		noteCase("embed-unterminated-before-real-embed", "host.md", "", vaultFile("host.md", "![[open ![[c]]"), vaultFile("c.md", "C")),
		noteCase("embed-single-bracket-before-real-embed", "host.md", "", vaultFile("host.md", "![[a]b ![[c]]"), vaultFile("c.md", "C")),
		noteCase("embed-through-file-component", "host.md", "", vaultFile("host.md", "![[a/b]]"), vaultFile("a", "plain file")),
		noteCase("embed-wikilink-not-embed", "host.md", "", vaultFile("host.md", "[[c]] stays\n"), vaultFile("c.md", "C")),
		noteCase("embed-triple-bracket", "host.md", "", vaultFile("host.md", "![[[c]]]\n"), vaultFile("c.md", "C"), vaultFile("[c.md", "bracket")),
		noteCase("embed-html-format", "host.md", "html", vaultFile("host.md", "---\ntitle: Host\n---\n![[c]]\n"), vaultFile("c.md", "- from <child>\n- second\n")),
		noteCase("embed-pdf-format", "host.md", "pdf", vaultFile("host.md", "---\ntitle: Host\nlang: de\n---\n![[c]]\n"), vaultFile("c.md", "---\ntitle: Child\n---\nchild body\n")),
		noteCase("embed-pdf-child-frontmatter-not-merged", "host.md", "pdf", vaultFile("host.md", "![[c]]\n"), vaultFile("c.md", "---\nrecipient: X\ntitle: Child\n---\nbody\n")),
	)

	// Embed failures are rendered into the output, not returned.
	cases = append(cases,
		noteCase("embed-not-found", "host.md", "", vaultFile("host.md", "![[ghost]]\n")),
		noteCase("embed-not-found-with-fragment", "host.md", "", vaultFile("host.md", "![[ghost#Heading]]\n")),
		noteCase("embed-alias-pipe-not-found", "host.md", "", vaultFile("host.md", "![[c|Alias]]\n"), vaultFile("c.md", "C")),
		noteCase("embed-image-gets-md-suffix", "host.md", "", vaultFile("host.md", "![[pic.png]]\n"), vaultFile("pic.png", "PNG")),
		noteCase("embed-empty-name-after-trim", "host.md", "", vaultFile("host.md", "![[ ]]\n![[#only-fragment]]\n")),
		noteCase("embed-traversal", "host.md", "", vaultFile("host.md", "![[../outside/secret]]\n")),
		noteCase("embed-traversal-nested", "host.md", "", vaultFile("host.md", "![[a/../../outside/secret]]\n")),
		noteCase("embed-absolute-path-is-vault-relative", "host.md", "", vaultFile("host.md", "![[/c]]\n"), vaultFile("c.md", "vault C")),
		noteCase("embed-absolute-outside-path", "host.md", "", vaultFile("host.md", "![[/etc/passwd]]\n")),
		exportVaultCase{
			ID: "embed-symlink-escape-file", Note: strPtr("host.md"), Files: []exportVaultFile{vaultFile("host.md", "![[link]]\n")},
			Symlinks: []exportVaultLink{{Path: "link.md", Target: exportVaultOutsideToken + "/secret.md"}},
		},
		exportVaultCase{
			ID: "embed-symlink-escape-dir", Note: strPtr("host.md"), Files: []exportVaultFile{vaultFile("host.md", "![[linkdir/secret]]\n")},
			Symlinks: []exportVaultLink{{Path: "linkdir", Target: exportVaultOutsideToken}},
		},
		exportVaultCase{
			ID: "embed-symlink-contained", Note: strPtr("host.md"), Files: []exportVaultFile{vaultFile("host.md", "![[alias]]\n"), vaultFile("real.md", "via contained link\n")},
			Symlinks: []exportVaultLink{{Path: "alias.md", Target: "real.md"}},
		},
		exportVaultCase{
			ID: "embed-symlink-dangling", Note: strPtr("host.md"), Files: []exportVaultFile{vaultFile("host.md", "![[dangling]]\n")},
			Symlinks: []exportVaultLink{{Path: "dangling.md", Target: "nowhere.md"}},
		},
		exportVaultCase{
			ID: "embed-directory", Note: strPtr("host.md"), Files: []exportVaultFile{vaultFile("host.md", "![[folder]]\n")}, Dirs: []string{"folder.md"},
		},
		noteCase("embed-invalid-frontmatter", "host.md", "", vaultFile("host.md", "![[bad]]\n"), vaultFile("bad.md", "---\ntitle: One\ntitle: Two\n---\nbody\n")),
		noteCase("embed-error-then-success", "host.md", "", vaultFile("host.md", "![[ghost]]\n![[ok]]\n"), vaultFile("ok.md", "fine\n")),
		exportVaultCase{
			ID: "embed-over-read-limit", Note: strPtr("host.md"),
			Files: []exportVaultFile{vaultFile("host.md", "![[big]]\n"), {Path: "big.md", Sparse: 64<<20 + 1}},
		},
		noteCase("embed-error-text-is-not-reexpanded", "host.md", "", vaultFile("host.md", "![[a]]\n"), vaultFile("a.md", "![[ghost]] kept\n")),
	)

	// Views.
	people := []map[string]string{
		row("path", "people/ann.md", "name", "Ann", "role", "dev|ops"),
		row("path", "people/bob.md", "name", "Bob", "city", "Köln"),
	}
	peopleFiles := []exportVaultFile{
		vaultFile("people/ann.md", "---\ntitle: Ann\n---\nAnn body ![[note]]\n"),
		vaultFile("people/bob.md", "# Bob\n\nBob body\n"),
		vaultFile("note.md", "shared note\n"),
	}
	for _, format := range []string{"", "html", "pdf"} {
		name := format
		if name == "" {
			name = "default"
		}
		cases = append(cases, viewCase("view-people-"+name, "People", []string{"name", "role", "path"}, people, format, peopleFiles...))
	}
	cases = append(cases,
		viewCase("view-no-rows-markdown", "Empty", []string{"name"}, nil, "", peopleFiles...),
		viewCase("view-no-rows-html", "Empty", []string{"name"}, nil, "html"),
		viewCase("view-no-rows-pdf", "Empty", []string{"name"}, nil, "pdf"),
		viewCase("view-no-rows-pdf-unnamed", "", []string{"name"}, nil, "pdf"),
		viewCase("view-no-rows-markdown-unnamed", "", nil, nil, ""),
		viewCase("view-unnamed-with-rows", "", []string{"path"}, []map[string]string{row("path", "a.md")}, "", vaultFile("a.md", "A body\n")),
		viewCase("view-unnamed-with-rows-pdf", "", []string{"path"}, []map[string]string{row("path", "a.md")}, "pdf", vaultFile("a.md", "---\ntitle: A\n---\nA body\n")),
		viewCase("view-rows-without-path", "NoPath", []string{"name"}, []map[string]string{row("name", "x"), row("name", "y", "extra", "z")}, ""),
		viewCase("view-row-with-empty-path-skipped", "EmptyPath", []string{"name", "path"}, []map[string]string{row("name", "x", "path", ""), row("name", "y", "path", "b.md")}, "", vaultFile("b.md", "B\n")),
		viewCase("view-absolute-paths-in-root", "Abs", []string{"path"}, []map[string]string{row("path", exportVaultRootToken+"/a.md"), row("path", exportVaultRootToken+"/sub/../b.md")}, "", vaultFile("a.md", "A\n"), vaultFile("b.md", "B\n")),
		viewCase("view-absolute-path-outside-vault", "Out", []string{"path"}, []map[string]string{row("path", exportVaultOutsideToken+"/secret.md")}, ""),
		viewCase("view-relative-traversal-path", "Trav", []string{"path"}, []map[string]string{row("path", "../outside/secret.md")}, "html"),
		viewCase("view-missing-note-error-line", "Missing", []string{"path"}, []map[string]string{row("path", "ghost.md"), row("path", "a.md")}, "", vaultFile("a.md", "A\n")),
		viewCase("view-missing-note-pdf", "Missing", []string{"path"}, []map[string]string{row("path", "ghost.md")}, "pdf"),
		viewCase("view-invalid-frontmatter-error-line", "Bad", []string{"path"}, []map[string]string{row("path", "bad.md")}, "", vaultFile("bad.md", "---\na: 1\na: 2\n---\nx\n")),
		viewCase("view-embed-error-inside-row", "EmbedErr", []string{"path"}, []map[string]string{row("path", "a.md")}, "", vaultFile("a.md", "![[ghost]]\n")),
		viewCase("view-duplicate-and-extra-columns", "Cols", []string{"b", "a", "b", "z"}, []map[string]string{row("a", "1", "q", "x|y"), row("c", "3")}, ""),
		viewCase("view-name-markdown-specials", "A # B | C", []string{"x"}, []map[string]string{row("x", "v")}, ""),
		viewCase("view-pdf-name-yaml-quoting", "Q: R #1 — \"x\"", []string{"x"}, []map[string]string{row("x", "v")}, "pdf"),
		viewCase("view-html-escapes-table", "Tbl <x>", []string{"x"}, []map[string]string{row("x", "<b>&\"'</b>")}, "html"),
		exportVaultCase{
			ID: "view-symlink-escape-row", View: &exportVaultView{Name: "Link", Columns: []string{"path"}}, Rows: []map[string]string{row("path", "link.md")},
			Symlinks: []exportVaultLink{{Path: "link.md", Target: exportVaultOutsideToken + "/secret.md"}},
		},
		exportVaultCase{
			ID: "view-symlink-contained-row", View: &exportVaultView{Name: "Link", Columns: []string{"path"}}, Rows: []map[string]string{row("path", "alias.md")}, Files: []exportVaultFile{vaultFile("real.md", "real\n")},
			Symlinks: []exportVaultLink{{Path: "alias.md", Target: "real.md"}},
		},
		viewCase("view-many-rows", "Many", []string{"path"}, manyRows(12), "", manyFiles(12)...),
	)
	return cases
}

func manyRows(n int) []map[string]string {
	rows := make([]map[string]string, n)
	for i := range rows {
		rows[i] = row("path", "n/"+strings.Repeat("x", i%3)+strconv.Itoa(i)+".md")
	}
	return rows
}

func manyFiles(n int) []exportVaultFile {
	files := make([]exportVaultFile, n)
	for i := range files {
		files[i] = vaultFile("n/"+strings.Repeat("x", i%3)+strconv.Itoa(i)+".md", "note "+strconv.Itoa(i)+"\n")
	}
	return files
}
