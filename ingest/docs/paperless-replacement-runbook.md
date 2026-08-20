# Paperless replacement runbook

This runbook is the production gate for replacing a Paperless-ngx instance with
`symingest`-generated Markdown (in a note vault) plus `symseek` search. It is a
staged, repeatable path with explicit pass/fail gates.

**Golden rule:** Paperless remains the source of truth until every gate below
passes. Do not decommission Paperless, delete originals, or redirect users to
the vault until the dry-run, pilot import, full import, verifier, and search
gates are all green.

## 0. Prerequisites

On the machine running the migration:

- `symingest` built and on `PATH` (`go build ./cmd/symingest`).
- External OCR tools installed and on `PATH`:
  - `tesseract` (image and PDF OCR)
  - `pdftoppm` (Poppler; renders PDF pages for OCR)
- Network access to the Paperless instance and a Paperless API token.
- A target **vault** directory (Markdown output) and an **archive** directory
  (original files), on a backed-up volume.

Configuration inputs (flag, environment variable, or `~/.config/symingest/config.toml`):

| Purpose | Flag | Environment variable |
| --- | --- | --- |
| Paperless base URL | `--base-url` | `PAPERLESS_URL` |
| Paperless API token | `--token` | `PAPERLESS_TOKEN` |
| Vault directory | `--vault` | `SYMINGEST_VAULT` |
| Archive directory | `--archive` | `SYMINGEST_ARCHIVE_PATH` |
| SQLite state DB | `--db` | `SYMINGEST_DB_PATH` |
| OCR language | `--ocr-lang` | `SYMINGEST_OCR_LANG` |

> Never commit or paste a real token, private base URL, or private document
> name into scripts, tickets, or logs. The examples below use placeholders.

`--token` also accepts a secret reference instead of a raw value:
`keychain://service/account` (macOS Keychain) or `symvault://ref` (symvault),
resolved the same way IMAP `password_secret` values are.

Export the connection once for the session:

```bash
export PAPERLESS_URL="https://paperless.internal.example:8001"
export PAPERLESS_TOKEN="<api-token>"
export SYMINGEST_VAULT="$HOME/paperless-vault"
export SYMINGEST_ARCHIVE_PATH="$HOME/paperless-archive"
```

## 1. Full dry-run (no writes) — Gate A

Scan the whole archive without downloading or writing anything. This proves the
importer can reach every page and surfaces unsupported file types and
unresolved metadata before any real work.

```bash
symingest import paperless --dry-run --report dryrun-report.json
```

**Pass criteria (Gate A):**

- The command completes without error and reaches the last page (a real
  archive spans many pages; a failure on page 2 means pagination is broken).
- `dryrun-report.json` lists a `total` matching the document count you expect.
- `unsupported_file_types` and the `unresolved_*` metadata ID lists are empty,
  or every entry is understood and accepted.

If Gate A fails, stop and fix the importer or the metadata in Paperless. Do not
proceed.

## 2. Bounded pilot import — Gate B

Import a small, inspectable subset first. Use `--limit` for the newest N
documents, or `--ids` for a hand-picked set.

```bash
# Newest 20 documents
symingest import paperless --limit 20 --report pilot-report.json

# Or an explicit, deterministic set
symingest import paperless --ids 101,102,103 --report pilot-report.json
```

Then verify exactly that subset (see step 4) and open a few generated notes by
hand.

**Pass criteria (Gate B):**

- `pilot-report.json` shows `failed: 0`.
- The generated notes contain the expected text and frontmatter (see
  [FRONTMATTER.md](FRONTMATTER.md)); the archived originals exist.
- `symingest import paperless --verify --limit 20` (or the same `--ids`) reports
  no discrepancies.

If Gate B fails, fix the cause and re-run the pilot. Imports are resumable, so a
re-run only retries what failed.

## 3. Full import — Gate C

Once the pilot is trusted, import the whole archive. The run is resumable and
idempotent: already-imported documents are skipped, previously failed ones are
retried.

```bash
symingest import paperless --report import-report.json
```

Re-run the same command until `failed: 0`. Use `--status` to inspect any
document still failing:

```bash
symingest import paperless --status --json
```

**Pass criteria (Gate C):**

- `import-report.json` shows `failed: 0` and `imported + skipped == total`.
- The import command exits `0`. A run with any failed document exits non-zero by design; do not continue to verification until a re-run or `--retry-failed` reaches `failed: 0`.

## 4. Post-import verification — Gate D

The verifier is the completeness gate. It re-reads the Paperless source and
compares it against the generated notes and archived originals. Use `--deep` for
final cutover evidence: it re-downloads every selected Paperless original and
compares its SHA-256 with the archived original.

```bash
symingest import paperless --verify --deep --json > verify-report.json
echo "exit code: $?"
```

The command exits non-zero if any document is missing, duplicated, missing its
archived original, has drifted metadata (tags, correspondent, document type,
storage path, created date), or deep verification finds a source/archive hash
mismatch.

**Pass criteria (Gate D):**

- Exit code `0`.
- `verify-report.json`: `missing`, `duplicate`, `missing_archive`,
  `hash_mismatch`, `source_hash_mismatch`, and `mismatches` are all empty;
  `verified == source_documents`; with `--deep`, `deep_verified == source_documents`.

If Gate D fails, the report names the affected document IDs and fields. Fix the
cause (re-import the missing documents, correct metadata in Paperless, remove
duplicate notes) and re-verify.

## 5. Vault/search validation with symseek — Gate E

`validate-vault` catches structural vault problems and can also act as an OCR quality tripwire. Then `symseek` owns indexing/search.

```bash
symingest validate-vault --min-body-length 40 "$SYMINGEST_VAULT"
```

`--min-body-length` fails notes whose extracted Markdown body is empty or suspiciously short. Adjust the threshold if you intentionally imported very short text snippets.

Point [`symseek`](https://github.com/danieljustus/symaira-seek) at the vault and confirm that migrated documents are findable. Use `symingest` to produce machine-readable search evidence:

```bash
symingest search index "$SYMINGEST_VAULT"

cat > search-fixtures.json <<'JSON'
[
  {"query":"known title or unique phrase", "min_results":1, "must_contain":["expected-file-or-term"]}
]
JSON

symingest search validate \
  --fixtures search-fixtures.json \
  --report search-report.json
```

Fixture rules are intentionally simple: each query must return at least `min_results`, and optional `must_contain` strings must appear in the raw JSON results. This keeps the report body-safe while still proving retrieval works.

3. Spot-check that a result links back to its archived original.

**Pass criteria (Gate E):**

- `validate-vault --min-body-length ...` exits `0`.
- `symseek` indexes the vault without error.
- `symingest search validate --report search-report.json` exits `0`.
- Known documents are returned by search across title, metadata, and content.

## 6. Cutover decision

Run the strict cutover gate from the same evidence files:

```bash
symingest cutover-check \
  --dry-run-report dryrun-report.json \
  --import-report import-report.json \
  --verify-report verify-report.json \
  --search-report search-report.json \
  --vault "$SYMINGEST_VAULT" \
  --min-documents <expected-paperless-count> \
  --min-body-length 40
```

Use `--json` in automation. The command exits non-zero when any required gate is missing, any report contains failures, search validation fails, the vault fails validation, or document counts disagree.

Only when Gates A–E **and** `cutover-check` are **all** green:

- Announce the cutover and switch users/search to the vault + `symseek`.
- Keep Paperless running in read-only mode for a defined grace period.

## 7. Rollback / fallback

Because Paperless was never modified by this process, rollback is simply
"keep using Paperless":

- The importer only reads from Paperless; it never deletes or edits Paperless
  documents. The source archive is untouched at every stage.
- If any gate regresses after cutover, revert users to Paperless as the source
  of truth. The vault and archive can be rebuilt from scratch by re-running the
  import (steps 1–4).
- Do not delete Paperless originals until the vault has been independently
  backed up and Gate D has passed on the full archive at least once.

## Gate summary

| Gate | Step | Passes when |
| --- | --- | --- |
| A | Dry-run | Full scan completes; report clean of unsupported/unresolved surprises |
| B | Pilot | Subset imports with `failed: 0` and verifies clean |
| C | Full import | `failed: 0`, `imported + skipped == total` |
| D | Verifier | Exit `0`; no missing/duplicate/missing-archive/mismatch |
| E | Search | `symingest search validate` report is clean; known documents are findable |
| F | Cutover check | `symingest cutover-check` exits `0`; all evidence reports agree |

Paperless stays the source of truth until Gate F passes.
