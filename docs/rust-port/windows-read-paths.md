# Windows read-tool path contract

The Unix recording `testdata/port/mcp/read-process.json` remains unchanged.
The first failing `desk_docs-*` window was **vault-relative**:
`notebooks/nested/hidden.md`, not an absolute path following `$ROOT`.
Prefix-only notebook normalization had hidden the earlier separator and exposed
only the next one. This was a product bug, not grounds to normalize all output.

| Tool / service | Path meaning | Windows implementation / decision |
| --- | --- | --- |
| `desk_docs`, `list_documents` / `DocsList` | Vault-relative Markdown identifier | `filepath.Rel` now followed by `filepath.ToSlash` at the result boundary. |
| `desk_ls` / `Ls` | Vault-relative Markdown identifier | Same boundary correction; index storage remains native absolute paths. |
| `desk_search`, `search_documents` | Vault-relative hits; registered external hits are absolute host paths | `searchVaultPath` already uses `ToSlash`; preserve native external source identities. |
| `desk_backlinks` | Relative document identifiers, including `../external/...` | Already `ToSlash`; previous basename fix remains intact. Rust now also applies the conversion to the different-volume fallback, matching Go. |
| `notebook_list`, `notebook_get`, `notebook_ask` | Notebook path is a vault-relative identifier; source metadata is authored Markdown | Normalize generated notebook paths and parsed notebook path at the Go boundary; Rust parser matches. Do not rewrite source text. |
| `meeting_list` | Vault-relative summary and failure paths | Convert `filepath.Rel` result with `ToSlash`, including every nested level. Rust already emits slash-form summaries. |
| `docs_review`, `docs_similar` | Stored **absolute host paths** | Leave native sidecar paths unchanged in Go and Rust. |
| `meeting_get` | `Document.Path` is an **absolute host path** | Leave native path unchanged. |
| `desk_diagram` | Relative graph node identifiers | `Graph` already applies `ToSlash`. |
| `desk_props` | Property values, no generated filesystem paths | Preserve properties byte-exactly. |
| `desk_read_result` | Result content / handle diagnostics, no generated document identifier | Preserve content and handles byte-exactly. |

## Harness invariants

- Drift diagnostics enumerate differing case IDs (first 50 with byte windows),
  including added/missing cases. The full original byte comparison still fails.
- Live process output only substitutes the isolated root; it no longer folds
  absolute or relative separators. Relative notebook/meeting prefix rewrites
  were removed from this harness.
- On Windows, derive the expectation from the recording by converting `/` only
  inside `$ROOT`-prefixed host-path tokens in stdout/stderr. JSON escaping is
  adjusted for the token's nesting level. Unix expectations remain untouched.
  The fixture's input files, requests and post-state identifiers are unchanged.
- Both language harnesses include wrong-absolute-separator and
  wrong-relative-separator negative controls. These remain unequal after the
  real output normalizer; globally separator-insensitive matching is forbidden.
- Rust replay seeds native absolute index keys using the same lexical component
  cleanup as Go's `filepath.Join`; slash-containing fixture input names must not
  create mixed-separator index keys that the production oracle never stores.
- Portable service regressions exercise multiple nested native levels, including
  notebook loads and meeting summaries. Explicit-backslash Rust controls cover
  the identifier conversion and native absolute index preservation on Unix too.

Native Windows execution is required to validate the final candidate; local
macOS tests and cross-compilation are not native acceptance evidence.
