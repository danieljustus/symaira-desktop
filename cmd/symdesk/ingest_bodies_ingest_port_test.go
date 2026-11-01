package main

// `ingest <file>`, `ingest jobs|retry|reocr|split|merge|rotate`, recorded for
// RUST-013 against the Go binary. Sources are plain text, Markdown, HTML and
// PDF stand-ins so no OCR engine or sibling tool is needed; the PDF
// operations run against fake `qpdf`/`pdfinfo` scripts in the case HOME.

func init() {
	cliIngestCaseBuilders = append(cliIngestCaseBuilders, cliIngestFileCases, cliIngestPDFCases)
}

const fakeQPDF = `#!/bin/sh
# Stand-in for qpdf: validates anything, copies the first input to the output.
if [ "$1" = "--check" ]; then exit 0; fi
case "$*" in
*" -- "*) eval "out=\${$#}" ;;
*) out="$2" ;;
esac
cp "$1" "$out"
`

const fakePDFInfo = `#!/bin/sh
# Stand-in for pdfinfo: the page count is the "pages=N" line of the file.
n=$(sed -n 's/^pages=//p' "$1" | head -n 1)
echo "Pages:          ${n:-3}"
`

func cliIngestFileCases() []cliIngestCase {
	cwd := map[string]string{
		"doc.txt":   "Invoice 2026 from Müller\nrechnung total 12 EUR\n",
		"note.md":   "# Meeting\n\nstrom und wasser\n",
		"page.html": "<html><body><h1>Title</h1><p>hello html world</p></body></html>\n",
		"empty.txt": "",
		"other.txt": "a second distinct text\n",
		"dir/x.txt": "inside a directory\n",
	}
	steps := ingestConcat(
		ingestBoth("rules", "add", "invoice", "tag", "billing"),
		[]cliIngestStep{
			ingestStep("ingest", "doc.txt"),
			ingestStep("--json", "ingest", "note.md"),
			ingestStep("--json", "ingest", "page.html"),
			ingestStep("ingest", "doc.txt"),
			ingestStep("--json", "ingest", "doc.txt"),
			ingestStep("--json", "ingest", "missing.txt"),
			ingestStep("ingest", "missing.txt"),
			ingestStep("--json", "ingest", "dir"),
			ingestStep("--json", "ingest", "empty.txt"),
			ingestStep("--json", "ingest", "$ROOT/cwd/other.txt"),
			ingestStep("--json", "ingest", "doc.txt", "extra"),
		},
		ingestBoth("ingest", "jobs"),
		ingestBoth("ingest", "jobs", "--limit", "2"),
		ingestBoth("ingest", "jobs", "--limit=1", "--offset=1"),
		ingestBoth("ingest", "jobs", "--limit", "0"),
		ingestBoth("ingest", "jobs", "--limit", "-5", "--offset", "-1"),
		ingestBoth("ingest", "jobs", "--offset", "99"),
		ingestBoth("ingest", "jobs", "--limit", "x"),
		ingestBoth("ingest", "retry", "1"),
		ingestBoth("ingest", "retry", " 2 "),
		ingestBoth("ingest", "retry", "abc"),
		ingestBoth("ingest", "retry", "999"),
		ingestBoth("ingest", "retry", "-3"),
		ingestBoth("ingest", "jobs"),
		// reocr
		ingestBoth("ingest", "reocr", "--document-id", "1"),
		ingestBoth("ingest", "reocr", "--document-id", "999"),
		ingestBoth("ingest", "reocr", "--document-id", "-1"),
		ingestBoth("ingest", "reocr"),
		ingestBoth("ingest", "reocr", "archive/ingest/nope.txt"),
		ingestBoth("ingest", "reocr", "archive/ingest/nope.txt", "--document-id", "1"),
		ingestBoth("ingest", "jobs"),
	)
	return []cliIngestCase{
		{Name: "ingest-file-flow", CwdFiles: cwd, SnapshotVault: true, SnapshotHome: true, Steps: steps},
		{Name: "ingest-no-vault", NoVault: true, SkipIndex: true, CwdFiles: cwd, Steps: ingestConcat(
			ingestBoth("ingest"),
			ingestBoth("ingest", "doc.txt"),
			ingestBoth("ingest", "jobs"),
			ingestBoth("ingest", "retry", "1"),
			ingestBoth("ingest", "reocr", "--document-id", "1"),
		)},
		{Name: "ingest-vault-subdir", CwdFiles: cwd, Files: map[string]string{"keep.md": "keep\n"}, SnapshotVault: true,
			Env: map[string]string{"SYMINGEST_ARCHIVE_PATH": "$ROOT/home/shared-archive"},
			Steps: ingestConcat(
				ingestBoth("ingest", "doc.txt"),
				ingestBoth("ingest", "reocr", "--document-id", "1"),
				ingestBoth("ingest", "jobs"),
			)},
	}
}

func cliIngestPDFCases() []cliIngestCase {
	const toolPath = "$ROOT/home/bin:/usr/bin:/bin"
	tools := map[string]string{
		"bin/qpdf":    fakeQPDF,
		"bin/pdfinfo": fakePDFInfo,
	}
	cwd := map[string]string{
		"a.pdf":      "%PDF-1.4\npages=5\n",
		"b.pdf":      "%PDF-1.4\npages=2\n",
		"exists.pdf": "%PDF-1.4\npages=1\n",
		"dir.pdf/x":  "not a pdf\n",
	}
	withTools := ingestConcat(
		ingestBoth("ingest", "split", "a.pdf", "--at", "2,4", "--output-dir", "out1"),
		ingestBoth("ingest", "split", "a.pdf", "--at", "2-3", "--output-dir", "out2"),
		ingestBoth("ingest", "split", "a.pdf", "--at", "2,4", "--output-dir", "out1"),
		ingestBoth("ingest", "split", "a.pdf", "--at", "5", "--output-dir", "out3"),
		ingestBoth("ingest", "split", "a.pdf", "--at", "0", "--output-dir", "out3"),
		ingestBoth("ingest", "split", "a.pdf", "--at", "x", "--output-dir", "out3"),
		ingestBoth("ingest", "split", "a.pdf", "--at", "2,2", "--output-dir", "out3"),
		ingestBoth("ingest", "split", "missing.pdf", "--at", "2", "--output-dir", "out3"),
		ingestBoth("ingest", "split", "dir.pdf", "--at", "2", "--output-dir", "out3"),
		ingestBoth("ingest", "split", "a.pdf", "--output-dir", "out3"),
		ingestBoth("ingest", "split", "a.pdf", "--at", "2"),
		ingestBoth("ingest", "merge", "a.pdf", "b.pdf", "--output", "merged.pdf"),
		ingestBoth("ingest", "merge", "a.pdf", "b.pdf", "--output", "merged.pdf"),
		ingestBoth("ingest", "merge", "a.pdf", "b.pdf", "--output", "exists.pdf"),
		ingestBoth("ingest", "merge", "a.pdf", "a.pdf", "--output", "a.pdf"),
		ingestBoth("ingest", "merge", "a.pdf", "missing.pdf", "--output", "m2.pdf"),
		ingestBoth("ingest", "merge", "a.pdf", "b.pdf", "--output", "sub/dir/m3.pdf"),
		ingestBoth("ingest", "merge", "a.pdf", "b.pdf"),
		ingestBoth("ingest", "rotate", "a.pdf", "--degrees", "90", "--output", "r1.pdf"),
		ingestBoth("ingest", "rotate", "a.pdf", "--degrees=-270", "--pages", "1-3,5", "--output", "r2.pdf"),
		ingestBoth("ingest", "rotate", "a.pdf", "--degrees", "45", "--output", "r3.pdf"),
		ingestBoth("ingest", "rotate", "a.pdf", "--degrees", "360", "--output", "r3.pdf"),
		ingestBoth("ingest", "rotate", "a.pdf", "--degrees", "90", "--pages", "0", "--output", "r3.pdf"),
		ingestBoth("ingest", "rotate", "a.pdf", "--degrees", "90", "--output", "r1.pdf"),
		ingestBoth("ingest", "rotate", "missing.pdf", "--degrees", "90", "--output", "r4.pdf"),
		ingestBoth("ingest", "rotate", "a.pdf", "--output", "r5.pdf"),
		ingestBoth("ingest", "rotate", "a.pdf", "--degrees", "90"),
	)
	return []cliIngestCase{
		{Name: "ingest-pdf-ops", NoVault: true, SkipIndex: true, CwdFiles: cwd,
			HomeFiles: tools, Env: map[string]string{"PATH": toolPath}, Steps: withTools},
		{Name: "ingest-pdf-ops-no-tools", NoVault: true, SkipIndex: true, CwdFiles: cwd,
			Env: map[string]string{"PATH": "$ROOT/home/empty-bin"}, Steps: ingestConcat(
				ingestBoth("ingest", "split", "a.pdf", "--at", "2", "--output-dir", "out"),
				ingestBoth("ingest", "merge", "a.pdf", "b.pdf", "--output", "m.pdf"),
				ingestBoth("ingest", "rotate", "a.pdf", "--degrees", "90", "--output", "r.pdf"),
			)},
		{Name: "ingest-pdf-ops-only-qpdf", NoVault: true, SkipIndex: true, CwdFiles: cwd,
			HomeFiles: map[string]string{"bin/qpdf": fakeQPDF}, Env: map[string]string{"PATH": toolPath}, Steps: ingestConcat(
				ingestBoth("ingest", "split", "a.pdf", "--at", "2", "--output-dir", "out"),
				ingestBoth("ingest", "merge", "a.pdf", "b.pdf", "--output", "m.pdf"),
			)},
	}
}
