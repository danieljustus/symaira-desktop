# Integrated RUST-007 native acceptance

All 17 unchanged RUST-007 acceptance commands passed on six native targets in
[run 37188397430](https://github.com/danieljustus/symaira-desktop/actions/runs/37188397430).
Each job also passed strict Vault/Index Clippy and verified that acceptance did
not mutate frozen fixture bytes. The 22-case actual-Go import corpus executed
in every job; the five native Go service rejection cases also executed.

| Native target | Accepted job |
| --- | --- |
| Linux ARM64 | 111395214619 |
| Linux AMD64 | 111395214836 |
| macOS ARM64 | 111395214727 |
| macOS AMD64 | 111395214847 |
| Windows ARM64 | 111395214753 |
| Windows AMD64 | 111395214735 |

The immutable native candidate is
`02ef9b35ffa509268c71a1a8355ea4082e0af047`. It differs from reviewed PR #1174's
source `c50e2d39ea09168d84cae6f8ada22a791bd1107d` only in its diagnostic CI
workflow; that workflow was not merged. Actual main after #1174 is
`2294dd453fae9f6aff31e9302305ed31b135ba09`, whose complete Git tree is identical
to that reviewed source: `691441dba60036f46f73dbf0f72eec6095ca648f`.
The canonical full Go production source P remains
`012e350bfc7b5def92e7b87b2f15c71fe5431b6b`.

[Retained command records](results/rust007-native-37188397430.json) bind each
platform's independently fetched log records to the exact ledger command list,
candidate, reviewed source and integrated tree. No cross-compilation or skipped
job is counted as native execution. Child-process-only ignored test bodies are
invoked by their parent contracts; the optional extra live history test does not
replace the required frozen and native history differentials.

The accepted rows are VAULT-004, VAULT-005, VAULT-006, CFG-004 and DATA-001.
The source-import helper remains distinct from production DatasetSync. Go's
nonfinite JSON projection failure after authoritative files are written remains
documented and tested. The deliberate fail-closed Rust behavior for mixed stale
trash selectors remains a named safety delta, with both outcomes exercised.

This completes the writes/history/retention/dataset slice. Full retrieval,
ingestion, CLI/MCP/HTTP coverage, release qualification, VALUE-002 and production
cutover remain separate requirements. Go stays the production implementation.
