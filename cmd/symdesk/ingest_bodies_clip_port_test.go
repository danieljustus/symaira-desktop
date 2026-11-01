package main

// `clip <url>`, recorded for RUST-013 against the Go binary with a fake
// `symbrowse` in the case HOME (no network, no real browser).

func init() {
	cliIngestCaseBuilders = append(cliIngestCaseBuilders, cliIngestClipCases)
}

const fakeSymbrowse = `#!/bin/sh
case "$2" in
*titled*) printf -- '---\ntitle: "Fancy / Title"\nsource: x\n---\n\n# Heading\nbody & <tags>\n' ;;
*empty-title*) printf -- '---\ntitle: ""\n---\n\n# Fallback Heading\nbody\n' ;;
*heading*) printf '# Only Heading\n\ntext\n' ;;
*fail*) echo boom >&2; exit 3 ;;
*) printf 'plain body without any title\n' ;;
esac
`

func cliIngestClipCases() []cliIngestCase {
	const toolPath = "$ROOT/home/bin:/usr/bin:/bin"
	tools := map[string]string{"bin/symbrowse": fakeSymbrowse}
	first := ingestConcat(
		ingestBoth("clip", "https://example.test/titled"),
		ingestBoth("clip", "https://example.test/empty-title"),
		ingestBoth("clip", "https://example.test/heading"),
		ingestBoth("clip", "https://example.test/plain page"),
		ingestBoth("clip", "https://example.test/fail"),
		ingestBoth("clip", ""),
	)
	return []cliIngestCase{
		{Name: "clip-fresh", HomeFiles: tools, Env: map[string]string{"PATH": toolPath}, SnapshotVault: true, Steps: first},
		{Name: "clip-overwrite", HomeFiles: tools, Env: map[string]string{"PATH": toolPath}, Steps: ingestConcat(
			ingestBoth("clip", "https://example.test/heading"),
			ingestBoth("clip", "https://example.test/heading"),
		)},
		{Name: "clip-no-clipper", Env: map[string]string{"PATH": "$ROOT/home/empty-bin"}, Steps: ingestBoth("clip", "https://example.test/titled")},
		{Name: "clip-no-vault", NoVault: true, SkipIndex: true, HomeFiles: tools, Env: map[string]string{"PATH": toolPath}, Steps: ingestBoth("clip", "https://example.test/titled")},
		{Name: "clip-symaira-bin", HomeFiles: map[string]string{"managed/symbrowse": fakeSymbrowse}, Env: map[string]string{"PATH": "$ROOT/home/empty-bin", "SYMAIRA_BIN": "$ROOT/home/managed"}, SnapshotVault: true,
			Steps: ingestBoth("clip", "https://example.test/heading")},
	}
}
