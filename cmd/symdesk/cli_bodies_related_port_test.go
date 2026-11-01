package main

// `related` and `relations related`, recorded for RUST-013 against fake
// `symmemory` scripts in the case HOME (executable files below `bin/`), so no
// real sibling tool, credential or network is involved.

func init() {
	cliBodiesCaseBuilders = append(cliBodiesCaseBuilders, cliBodiesRelatedCases)
}

const fakeSymmemoryHealthy = `#!/bin/sh
case "$1 $2" in
"version --json")
  echo '{"tool":"symmemory","version":"1.2.3","schema_version":1}'
  ;;
"entity list")
  cat <<'EOF'
[
 {"id":"e1","name":"Alpha","type":"person","aliases":["Al"],"description":"first"},
 {"id":"e2","name":"gamma","type":"topic","aliases":null,"description":""},
 {"id":"e3","name":"Zeta Corp","type":"org","aliases":["Zeta"],"description":""}
]
EOF
  ;;
"entity neighbors")
  case "$3" in
  Alpha)
    cat <<'EOF'
{"nodes":[{"id":"e1","name":"Alpha","type":"person","aliases":["Al"],"description":"first"},{"id":"n1","name":"Neighbor Corp","type":"org","aliases":null,"description":""},{"id":"n1","name":"Neighbor Corp","type":"org","aliases":null,"description":""},{"id":"n2","name":"Loner","type":"topic","aliases":["Lone"],"description":""}],
 "edges":[{"from_entity_id":"n1","to_entity_id":"e1","relation_type":"works_with"}]}
EOF
    ;;
  gamma)
    echo '{"nodes":null,"edges":null}'
    ;;
  *)
    echo "no such entity" >&2
    exit 3
    ;;
  esac
  ;;
*)
  echo "unexpected: $*" >&2
  exit 2
  ;;
esac
`

const fakeSymmemoryBroken = `#!/bin/sh
case "$1 $2" in
"version --json") echo '{"tool":"symmemory","version":"1.2.3"}' ;;
"entity list") echo "database is locked" >&2; exit 1 ;;
*) exit 2 ;;
esac
`

const fakeSymmemoryGarbage = `#!/bin/sh
case "$1 $2" in
"version --json") echo 'not json' ;;
"entity list") echo '{"not":"a list"}' ;;
*) exit 2 ;;
esac
`

func cliBodiesRelatedCases() []cliBodiesCase {
	vault := bodiesVault()
	vault["mentions.md"] = "---\ntitle: Mentions\n---\nAlpha is discussed with Neighbor Corp and Al.\n"
	vault["zeta.md"] = "---\ntitle: Zeta\n---\nunrelated body\n"
	vault["notes/neighbor-corp.md"] = "no frontmatter\n"
	const memoryPath = "$ROOT/home/bin:/usr/bin:/bin"
	related := func() []cliBodiesStep {
		return bodiesConcat(
			bodiesBoth("related", "a.md"),
			bodiesBoth("relations", "related", "a.md"),
			bodiesBoth("related", "notes/c.md"),
			bodiesBoth("related", "zeta.md"),
			bodiesBoth("related", "mentions.md"),
			bodiesBoth("related", "plain.md"),
			bodiesBoth("related", "missing.md"),
			bodiesBoth("related", "../escape.md"),
			bodiesBoth("related", ""),
			bodiesBoth("related"),
			bodiesBoth("related", "a.md", "b.md"),
		)
	}
	var cases []cliBodiesCase
	cases = append(cases, cliBodiesCase{Name: "related-memory", Files: vault, Env: map[string]string{"PATH": memoryPath},
		HomeFiles: withHomeFile(cliErrorsHomeFiles(), "bin/symmemory", fakeSymmemoryHealthy), Steps: related()})
	cases = append(cases, cliBodiesCase{Name: "related-no-memory", Files: vault, Env: map[string]string{"PATH": "/usr/bin:/bin"},
		Steps: bodiesConcat(bodiesBoth("related", "a.md"), bodiesBoth("related", "missing.md"))})
	cases = append(cases, cliBodiesCase{Name: "related-memory-broken", Files: vault, Env: map[string]string{"PATH": memoryPath},
		HomeFiles: withHomeFile(cliErrorsHomeFiles(), "bin/symmemory", fakeSymmemoryBroken), Steps: bodiesConcat(bodiesBoth("related", "a.md"))})
	cases = append(cases, cliBodiesCase{Name: "related-memory-garbage", Files: vault, Env: map[string]string{"PATH": memoryPath},
		HomeFiles: withHomeFile(cliErrorsHomeFiles(), "bin/symmemory", fakeSymmemoryGarbage), Steps: bodiesConcat(bodiesBoth("related", "a.md"))})
	return cases
}

func withHomeFile(files map[string]string, name, content string) map[string]string {
	files[name] = content
	return files
}
