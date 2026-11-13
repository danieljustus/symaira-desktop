.PHONY: paperless-mapping-differential
paperless-mapping-differential:
	$(CARGO) test -p symdesk-ingest --test paperless_mapping_contracts --locked


.PHONY: paperless-state-differential
paperless-state-differential:
	$(CARGO) test -p symdesk-ingest --test paperless_state_contracts --locked


.PHONY: paperless-export-differential
paperless-export-differential:
	$(CARGO) test -p symdesk-ingest --test paperless_export_contracts --locked


.PHONY: paperless-http-differential
paperless-http-differential:
	$(CARGO) test -p symdesk-ingest --test paperless_http_contracts --locked


.PHONY: notion-import-differential
notion-import-differential:
	$(CARGO) test -p symdesk-ingest --test notion_contracts --locked


.PHONY: ingest-queue-differential
ingest-queue-differential:
	$(CARGO) test -p symdesk-ingest --locked


.PHONY: ingest-store-differential ingest-classification-differential
ingest-store-differential:
	$(CARGO) test -p symdesk-ingest --test store_contracts --locked


.PHONY: ingest-classification-differential
ingest-classification-differential:
	$(CARGO) test -p symdesk-ingest --test classification_contracts --locked


.PHONY: ingest-extraction-differential
ingest-extraction-differential:
	$(CARGO) test -p symdesk-ingest --test extraction_contracts --locked


.PHONY: ingest-structured-differential
ingest-structured-differential:
	$(CARGO) test -p symdesk-ingest --test structured_contracts --locked


.PHONY: ingest-magika-differential
ingest-magika-differential:
	$(CARGO) test -p symdesk-ingest --test magika_contracts --locked


.PHONY: ingest-pipeline-differential
ingest-pipeline-differential:
	$(CARGO) test -p symdesk-ingest --test pipeline_contracts --locked


.PHONY: ingest-barcode-differential
ingest-barcode-differential:
	$(CARGO) test -p symdesk-ingest --test barcode_contracts --locked


.PHONY: ingest-vlm-differential
ingest-vlm-differential:
	$(CARGO) test -p symdesk-ingest --test vlm_contracts --locked


.PHONY: ingest-paperless-run-differential
ingest-paperless-run-differential:
	$(CARGO) test -p symdesk-ingest --test paperless_run_contracts --locked


.PHONY: ingest-paperless-verify-differential
ingest-paperless-verify-differential:
	$(CARGO) test -p symdesk-ingest --test paperless_verify_contracts --locked


.PHONY: ingest-notion-report-differential
ingest-notion-report-differential:
	$(CARGO) test -p symdesk-ingest --test notion_report_contracts --locked


.PHONY: ingest-mail-differential
ingest-mail-differential:
	$(CARGO) test -p symdesk-ingest --test mail_contracts --test mail_behaviour --locked


.PHONY: ingest-watcher-differential
ingest-watcher-differential:
	$(CARGO) test -p symdesk-ingest --test watcher_contracts --locked


.PHONY: ingest-ocr-differential
ingest-ocr-differential:
	$(CARGO) test -p symdesk-ingest --test ocr_contracts --locked


# RUST-010 acceptance: every ingest contract replays its Go-generated fixture.
.PHONY: ingest-differential
ingest-differential: ingest-queue-differential ingest-store-differential ingest-extraction-differential ingest-structured-differential ingest-magika-differential ingest-pipeline-differential ingest-barcode-differential ingest-vlm-differential ingest-paperless-run-differential ingest-paperless-verify-differential ingest-notion-report-differential ingest-mail-differential ingest-watcher-differential ingest-ocr-differential notion-import-differential paperless-mapping-differential paperless-state-differential paperless-export-differential paperless-http-differential

.PHONY: rust-miri rust-unsafe-inventory build clean core-differential docker-build font-guard frontmatter-write-differential mcp-differential port-contract release-signing-guard representative-differential rust-build rust-check rust-coverage rust-features rust-fuzz-smoke rust-gates rust-lint rust-security rust-test rust-version-contract room-journal-differential sidecar-differential sidecar-metadata-differential symroom-differential vault-history-differential vault-read-differential vault-retention-differential vault-write-differential

.PHONY: retention-state-differential
.PHONY: source-differential
.PHONY: source-watch-differential
.PHONY: room-run-projection-differential
.PHONY: room-run-cli-differential
.PHONY: dataset-sync-differential
.PHONY: dataset-cli-differential
.PHONY: history-purge-differential
.PHONY: history-trash-purge-differential
.PHONY: room-run-wait-cli-differential dataset-purge-differential
.PHONY: room-mcp-differential
.PHONY: room-run-mutations-cli-differential
.PHONY: room-note-cli-differential
.PHONY: room-merge-read-differential history-prune-differential
.PHONY: room-identity-cli-differential
.PHONY: room-index-differential history-service-differential
.PHONY: room-member-cli-differential
.PHONY: room-index-cli-differential
.PHONY: room-verify-differential room-verify-cli-differential
.PHONY: index-backup-differential
.PHONY: room-decide-cli-differential
.PHONY: room-log-differential room-log-cli-differential
.PHONY: index-restore-differential
.PHONY: index-relocate-differential
.PHONY: index-location-differential
.PHONY: room-artifact-cli-differential
.PHONY: room-watch-stream-differential
.PHONY: room-brain-profile-differential
.PHONY: room-init-core-differential
.PHONY: room-init-cli-differential
.PHONY: room-watch-cli-differential
.PHONY: room-artifact-identity-cli-differential room-doctor-cli-differential index-maintenance-cli-differential recipe-validate-differential recipe-run-differential
.PHONY: room-checkpoint-cli-differential index-build-cli-differential index-retrieval-cli-differential index-retry-cli-differential index-symlink-cli-differential sources-cli-differential
.PHONY: config-precedence-differential config-vault-selection-differential room-run-approval-cli-differential history-tasks-cli-differential
.PHONY: room-io-error-cli-differential
.PHONY: config-cli-differential

config-cli-differential:
	$(CARGO) test -p symdesk-cli --test config --locked

.PHONY: help-cli-differential

help-cli-differential:
	$(CARGO) test -p symdesk-cli --test help --locked

# CLI-001/003/004: Cobra command resolution, pflag grammar at every argv
# position, argument counts, required/exclusive flags, --help/--version and
# the generated help/completion/__complete commands, replayed from real Go
# processes. CLI-002: the recorded Cobra tree (paths, aliases, visibility,
# flags, argument validators) rebuilt in Rust and checked against every probe.
.PHONY: cobra-parse-cli-differential command-tree-cli-differential

cobra-parse-cli-differential:
	$(CARGO) test -p symdesk-cli --test cobra_parse --locked

command-tree-cli-differential:
	$(CARGO) test -p symdesk-cli --bin symdesk cobra:: --locked

.PHONY: duplicates-cli-differential
.PHONY: cli-errors-differential
.PHONY: remote-command-differential
.PHONY: room-flag-grammar-cli-differential
.PHONY: room-identity-provider-cli-differential
.PHONY: room-store-cli-differential
.PHONY: notebook-write-differential
.PHONY: base-view-write-differential
.PHONY: retrieval-chunks-differential
.PHONY: retrieval-bm25-differential
.PHONY: retrieval-embedding-state-differential
.PHONY: retrieval-vector-differential retrieval-ivf-differential
.PHONY: retrieval-turboquant-differential retrieval-turboquant-benchmark
.PHONY: retrieval-hybrid-differential
.PHONY: retrieval-pending-rebuild-differential
.PHONY: retrieval-sections-differential
.PHONY: retrieval-embedding-http-differential
.PHONY: retrieval-reembed-http-differential
.PHONY: retrieval-local-hash-differential
.PHONY: retrieval-csv-sections-differential
.PHONY: retrieval-search-cli-differential
.PHONY: retrieval-search-mcp-differential
.PHONY: retrieval-ask-offline-differential
.PHONY: retrieval-ask-mcp-differential
.PHONY: render-ir-differential
.PHONY: render-html-differential
.PHONY: render-differential render-pdfops-differential render-pdfops-hostile-differential
.PHONY: render-export-differential
.PHONY: render-export-vault-differential
.PHONY: render-pdf-frontmatter-differential
.PHONY: render-archive-differential
.PHONY: render-archive-stamp-differential
.PHONY: render-archive-pdf-differential
.PHONY: render-press-differential

.PHONY: dataset-aggregate-differential

VERSION ?= $(shell git describe --tags --abbrev=0 --match 'v[0-9]*' 2>/dev/null | sed 's/^v//')
CARGO ?= cargo
# Keep differential artifacts in the candidate's isolated Cargo target tree.
RUST_TARGET_DIR ?= $(if $(CARGO_TARGET_DIR),$(CARGO_TARGET_DIR),target)
# Native differential recipes compare freshly built binaries, so they need the
# platform's executable suffix rather than a Unix-only path.
EXE_SUFFIX := $(if $(filter Windows_NT,$(OS)),.exe,)
# Command-line make variables are not inherited by recipes. When callers
# select an isolated Cargo target tree, pass it through to Cargo as well as
# using it for the differential binary path below.
ifneq ($(origin CARGO_TARGET_DIR),undefined)
export CARGO_TARGET_DIR
endif
RUST_NIGHTLY ?= nightly-2026-09-03
FUZZ_RUNS ?= 10000



# RUST-019: the Go core is retired (last Go commit 36aad3d4, branch
# oracle-pins/last-go-36aad3d4; last Go release tag v0.12.2 = 29520267); build,
# test and lint map to the Rust workspace. `build` stamps both binaries with
# VERSION and copies them to bin/.
build:
	SYMDESK_VERSION="$(if $(VERSION),$(VERSION),devel)" SYMROOM_VERSION="$(if $(VERSION),$(VERSION),dev)" \
		$(CARGO) build --release --locked -p symdesk-cli -p symroom-cli --bin symdesk --bin symroom
	@mkdir -p bin
	cp "$(RUST_TARGET_DIR)/release/symdesk$(EXE_SUFFIX)" "$(RUST_TARGET_DIR)/release/symroom$(EXE_SUFFIX)" bin/

test: rust-test

lint: rust-lint release-signing-guard

# Current renderer aggregate only; full RUST-011 (PDF artifacts/archive/draw) is open.
# Supply fresh evidence/runtime roots. Native captures are never overwritten.
render-differential: render-export-differential render-export-vault-differential render-pdf-frontmatter-differential render-archive-differential render-archive-stamp-differential render-archive-pdf-differential render-press-differential render-pdfops-differential render-pdfops-hostile-differential

render-pdfops-differential:
	$(CARGO) build -p symdesk-render --examples --locked
	$(CARGO) test -p symdesk-render --locked --examples --test pdfops_frozen


# Hostile PDFs through the REAL installed pdfinfo/pdfseparate/pdfunite/qpdf (the
# hostile_inputs.py corpus, 455 cases). The former live Go/Rust comparison (and
# its RENDER_EVIDENCE_ROOT/RENDER_RUNTIME_ROOT evidence directories) is retired:
# it compared the same inputs that are now frozen.
render-pdfops-hostile-differential:
	$(CARGO) build -p symdesk-render --examples --locked
	$(CARGO) test -p symdesk-render --locked --examples --test pdfops_hostile_frozen


# Verifies the identity of the binary actually built: the stamped revision must
# be this checkout's HEAD and the CLI must run.




# Release signing/notarization order and published-byte verification contract.
release-signing-guard:
	@./scripts/check-release-signing.sh

# Issue #352: macOS app text must use .symairaText(role) instead of inline
# .font(.caption/.headline/...) literals so Dynamic Type scales.
font-guard:
	@HITS="$$(grep -rnE '\.font\(\.(caption|caption2|headline|callout|subheadline|title|title2|title3|body|largeTitle)' Sources/SymDeskApp/ || true)"; \
	if [ -n "$$HITS" ]; then echo "$$HITS"; echo "inline role font literals found — use .symairaText(role) (issue #352)"; exit 1; fi



docker-build:
	docker build -t symaira-desktop:dev .




core-differential:
	$(CARGO) test -p symdesk-core --all-features --locked

# INGEST-006 storage-path templates: the Go-owned fixture records Compile
# acceptance/error text, Eval over field maps and Disambiguate from the
# production internal/templatepath package; Rust replays all of it.
.PHONY: contacts-keyprovider-differential
contacts-keyprovider-differential:
	$(CARGO) test -p symdesk-contacts --locked --test keyprovider_contracts

.PHONY: contacts-erase-differential
contacts-erase-differential:
	$(CARGO) test -p symdesk-contacts --locked --test erase_contracts

.PHONY: contacts-relationship-differential
contacts-relationship-differential:
	$(CARGO) test -p symdesk-contacts --locked --test relationship_contracts

.PHONY: contacts-crud-differential
contacts-crud-differential:
	$(CARGO) test -p symdesk-contacts --locked --test crud_contracts

.PHONY: contacts-vcard-differential
contacts-vcard-differential:
	$(CARGO) test -p symdesk-contacts --locked --test vcard_contracts

.PHONY: contacts-import-differential
contacts-import-differential:
	$(CARGO) test -p symdesk-contacts --locked --test csv_contracts --test import_contracts

# RUST-009 acceptance: every contacts differential (store, CRUD, relationships,
# timeline/meeting import, memberships, memory links, vCard/CSV/import,
# security/export/backup, erase, key provider, schema) plus the dataset sync,
# purge, query, aggregate and view-projection differentials.
.PHONY: contacts-dataset-differential
contacts-dataset-differential: contacts-keyprovider-differential contacts-erase-differential contacts-relationship-differential contacts-crud-differential contacts-vcard-differential contacts-import-differential contacts-security-differential contacts-memorylink-differential contacts-timeline-differential contacts-membership-differential contacts-schema-differential dataset-sync-differential dataset-purge-differential dataset-aggregate-differential dataset-query-full-differential dataset-cli-differential

.PHONY: contacts-security-differential

# Redact masks, export bytes and the AES-256-GCM/Argon2id backup format. The
# Rust test writes its own backup into a fresh directory and the Go oracle must
# restore it, so both directions of the format are proven in one run.
contacts-security-differential:
	$(CARGO) test -p symdesk-contacts --locked --test security_contracts
	rm -rf "$(RUST_TARGET_DIR)/contacts-backup-exchange" && mkdir -p "$(RUST_TARGET_DIR)/contacts-backup-exchange"
	PORT_RUST_BACKUP_DIR="$(abspath $(RUST_TARGET_DIR))/contacts-backup-exchange" $(CARGO) test -p symdesk-contacts --locked --test security_contracts
	test -s "$(RUST_TARGET_DIR)/contacts-backup-exchange/rust.bak"

.PHONY: contacts-memorylink-differential

contacts-memorylink-differential:
	$(CARGO) test -p symdesk-contacts --locked --test memorylink_contracts

.PHONY: contacts-timeline-differential

contacts-timeline-differential:
	$(CARGO) test -p symdesk-contacts --locked --test timeline_contracts

.PHONY: contacts-membership-differential

contacts-membership-differential:
	$(CARGO) test -p symdesk-contacts --locked --test membership_contracts

.PHONY: contacts-schema-differential
contacts-schema-differential:
	$(CARGO) test -p symdesk-contacts --locked --test schema_contracts

.PHONY: template-path-differential
template-path-differential:
	$(CARGO) test -p symdesk-core --locked --test template_path_contracts

.PHONY: config-verbatim-differential
# Native Windows only: retain the recorded AMD64 bytes and compare fresh Go
# observations on either architecture. Foreign hosts must not imply native PASS.

config-verbatim-differential:
	$(CARGO) test -p symdesk-core --all-features --locked
	$(CARGO) test -p symdesk-cli --test ask_offline --locked

# CFG-004 config filesystem writes: the Go-owned fixture records the exact
# MkdirAll/OpenFile side effects — every created ancestor and its mode, an
# existing file's truncation, the created file's mode, the resulting file set
# and the wrapped failure stage — and replays them in Rust.

config-save-differential:
	$(CARGO) test -p symdesk-core --test config_save_contracts --locked

# CFG-002 config precedence: replay the frozen Go loader results in Rust.
config-precedence-differential:
	$(CARGO) test -p symdesk-core --test config_precedence --locked

config-vault-selection-differential:
	$(CARGO) test -p symdesk-cli --test config_vault_selection --locked




vault-read-differential:
	$(CARGO) test -p symdesk-vault --all-features --locked

frontmatter-write-differential:
	$(CARGO) test -p symdesk-vault --test frontmatter_write_contracts --locked

# VAULT-004 write stack: the Go-owned filesystem harness for atomic writes,
# create/edit/move/delete, interruption and read-only filesystems replays in
# Rust byte-for-byte (bytes, modes, hashes, file set, trash entry).

vault-write-differential:
	$(CARGO) test -p symdesk-vault --test filesystem_write_contracts --locked
	$(CARGO) test -p symdesk-vault --test note_operations_contracts --locked

notebook-write-differential:
	$(CARGO) test -p symdesk-vault --test notebook_write_contracts --locked

base-view-write-differential:
	$(CARGO) test -p symdesk-vault --test base_view_write_contracts --locked

# HTTP-002/HTTP-005 permissions manager: the Go-owned fixture executes the
# production internal/permissions package over users, groups and document rules
# (results, users/groups/permissions.json bytes and modes, Go JSON decoding
# quirks); Rust replays all of it.
.PHONY: permissions-differential
permissions-differential:
	$(CARGO) test -p symdesk-vault --test permissions_contracts --locked

# PG-001 PostgreSQL self-host store: the Go-recorded fixture (testdata/port/storage/
# postgres.json) is replayed through the Rust store against a real PostgreSQL
# server. This target starts its own disposable server
# (scripts/rust-port/postgres-sidecar.sh: a container on a random 127.0.0.1 port
# with a per-run password; if SYMDESK_PG_ADMIN_DSN is already set that server is
# used instead, which is how local-gate.sh hands the Linux gate container its
# sidecar). The Go recorder left the tree in RUST-019.
.PHONY: postgres-integration
postgres-integration:
	CARGO="$(CARGO)" scripts/rust-port/postgres-sidecar.sh run -- scripts/rust-port/postgres-integration.sh



# Checkpoints and the trash lifecycle against the pinned Go oracle.
vault-history-differential:
	$(CARGO) test -p symdesk-vault --test history_lifecycle_contracts --locked

history-tasks-cli-differential:
	$(CARGO) test -p symdesk-cli --test history_tasks --locked

.PHONY: docs-read-cli-differential

docs-read-cli-differential:
	$(CARGO) test -p symdesk-cli --test docs_read --locked
.PHONY: notebook-write-cli-differential
notebook-write-cli-differential:
	$(CARGO) test -p symdesk-cli --test notebook_write --locked
.PHONY: notebook-read-cli-differential
notebook-read-cli-differential:
	$(CARGO) test -p symdesk-cli --test notebook_read --locked
.PHONY: views-cli-differential
views-cli-differential:
	$(CARGO) test -p symdesk-cli --test views --locked
.PHONY: relations-inverse-cli-differential
relations-inverse-cli-differential:
	$(CARGO) test -p symdesk-cli --test relations_inverse --locked
.PHONY: history-read-cli-differential
history-read-cli-differential:
	$(CARGO) test -p symdesk-cli --test history_read --locked

.PHONY: graph-cli-differential
graph-cli-differential:
	$(CARGO) test -p symdesk-cli --test graph --locked

.PHONY: backlinks-cli-differential
backlinks-cli-differential:
	$(CARGO) test -p symdesk-cli --test backlinks --locked


duplicates-cli-differential:
	$(CARGO) test -p symdesk-cli --test duplicates --locked

# CLI-005/CLI-006 output modes and error taxonomy: the Go binary's stdout,
# stderr and exit code for every command family, recorded in disposable
# HOME/XDG roots with loopback fakes for external providers.

cli-errors-differential:
	$(CARGO) test -p symdesk-cli --test cli_errors --locked

# CLI-008 remote compatibility command allowlist against the Go server over
# loopback (server-forced vault, rejected commands, identical result envelope).

remote-command-differential:
	$(CARGO) test -p symdesk-cli --test remote_command --locked

# RUST-013 command bodies (props, tags, history checkpoint/undo-task, trash,
# search export/notebook, views, relations, ...): Go-recorded process
# fixtures with vault-tree snapshots after every step.
.PHONY: cli-bodies-differential

cli-bodies-differential:
	$(CARGO) test -p symdesk-cli --test cli_bodies --locked

# RUST-013 ingest, mail, meeting, rules and clip command bodies: Go-recorded
# process fixtures (loopback fakes only) in their own fixture file.
.PHONY: cli-ingest-bodies-differential

cli-ingest-bodies-differential:
	$(CARGO) test -p symdesk-cli --test cli_ingest_bodies --locked

# RUST-013 slice r013-paperviews: `paperless import|migrate` (loopback fake
# Paperless-ngx) and `views exec|new-entry|export-csv|import-csv|embed`.
.PHONY: cli-paperviews-bodies-differential

cli-paperviews-bodies-differential:
	$(CARGO) test -p symdesk-cli --test cli_paperviews_bodies --locked

# RUST-013 slice r013-admin command bodies (perm, vault health/adopt, relations
# contact, demo init, doctor, dataset list, export, ai autofill, notebook
# generate): Go-recorded process fixtures with vault-tree and working-directory
# snapshots after every step.
.PHONY: cli-admin-bodies-differential

cli-admin-bodies-differential:
	$(CARGO) test -p symdesk-cli --test cli_admin_bodies --locked

# RUST-015 HTTP slices (HTTP-001/002/003/005/006/007, SEC-002 failure paths):
# the real Go `symdesk serve` process is probed over a raw socket in a disposable
# HOME/vault with generated tokens and loopback fakes; the Rust server replays
# the recorded status line, headers, body, connection behavior and vault tree.
.PHONY: http-surface-differential

http-surface-differential:
	$(CARGO) test -p symdesk-cli --test http_surface --locked

# VAULT-006 multi-document rules file and document-to-metadata mapping against
# the pinned Go oracle.
retention-rules-differential:
	$(CARGO) test -p symdesk-vault --test retention_rules_contracts --locked

# RUST-007 authoritative Markdown/CSV state and post-mutation rereads.
# Generation is explicit; acceptance checks never rewrite frozen expectations.

retention-state-differential:
	$(CARGO) test -p symdesk-vault --test retention_state_contracts --locked


# Retention rules, evaluation and the proposal/history state files against the
# pinned Go oracle.
vault-retention-differential:
	$(CARGO) test -p symdesk-vault --test retention_contracts --locked


# SymRoom journal append and read-back (ROOM-002): the Go oracle records the
# per-author chain, the Lamport ceiling and the appended bytes; the Rust replay
# reproduces them.
room-journal-differential:
	$(CARGO) test -p symroom-core --locked --test journal_contracts


room-run-projection-differential:
	$(CARGO) test -p symroom-core --locked --test run_projection_contracts


room-run-cli-differential:
	$(CARGO) test -p symroom-cli --locked --test run_commands


room-run-wait-cli-differential:
	$(CARGO) test -p symroom-cli --locked --test run_commands run_wait_matches_go_process_contract


room-run-mutations-cli-differential:
	$(CARGO) test -p symroom-cli --locked --test run_commands run_request_start_cancel_match_go_process_contract


room-note-cli-differential:
	$(CARGO) test -p symroom-cli --locked --test note_commands note_cli_matches_go_process_and_journal_contract


room-decide-cli-differential:
	$(CARGO) test -p symroom-cli --locked --test decide_commands


room-identity-cli-differential:
	$(CARGO) test -p symroom-cli --locked --test identity_commands identity_cli_matches_go_process_and_key_file_contract


room-member-cli-differential:
	$(CARGO) test -p symroom-cli --locked --test member_commands


room-grammar-process-differential:
	$(CARGO) test -p symroom-cli --locked --test grammar_process


room-mcp-differential:
	$(CARGO) test -p symroom-cli --locked --test mcp


room-merge-read-differential:
	$(CARGO) test -p symroom-core --locked --test merge_read_contracts


room-index-differential:
	$(CARGO) test -p symroom-core --locked --test index_contracts


room-index-cli-differential:
	$(CARGO) test -p symroom-cli --locked --test index_commands


room-verify-differential:
	$(CARGO) test -p symroom-core --locked --test verify_contracts


room-verify-cli-differential:
	$(CARGO) test -p symroom-cli --locked --test verify_commands


room-log-differential:
	$(CARGO) test -p symroom-core --locked --test log_contracts


room-log-cli-differential:
	$(CARGO) test -p symroom-cli --locked --test log_commands


room-artifact-cli-differential:
	$(CARGO) test -p symroom-cli --locked --test artifact_commands


room-artifact-identity-cli-differential:
	$(CARGO) test -p symroom-cli --locked --test artifact_identity_commands


room-watch-stream-differential:
	$(CARGO) test -p symroom-core --locked --test watch_stream_contracts


room-brain-profile-differential:
	$(CARGO) test -p symroom-cli --locked --test brain_profile_commands


room-init-core-differential:
	$(CARGO) test -p symroom-core --locked --test room_init_contracts


room-init-cli-differential:
	$(CARGO) test -p symroom-cli --locked --test init_commands


room-watch-cli-differential:
	$(CARGO) test -p symroom-cli --locked --test watch_commands


room-doctor-cli-differential:
	$(CARGO) test -p symroom-cli --locked --test doctor_commands


room-checkpoint-cli-differential:
	$(CARGO) test -p symroom-cli --locked --test checkpoint_commands


retrieval-chunks-differential:
	$(CARGO) test -p symdesk-index --locked --test retrieval_chunks


retrieval-bm25-differential:
	$(CARGO) test -p symdesk-index --locked --test retrieval_bm25

source-differential:
	$(CARGO) test -p symdesk-cli --locked --test sources_frozen registry


source-watch-differential:
	$(CARGO) test -p symdesk-cli --locked --test sources_frozen watch



retrieval-embedding-state-differential:
	$(CARGO) test -p symdesk-index --locked --test retrieval_embedding_state


retrieval-hybrid-differential:
	$(CARGO) test -p symdesk-index --locked --test retrieval_hybrid_contracts


retrieval-pending-rebuild-differential:
	$(CARGO) test -p symdesk-index --locked --test retrieval_pending_rebuild --test retrieval_embedding_state


retrieval-sections-differential:
	$(CARGO) test -p symdesk-index --locked --test retrieval_sections --test retrieval_chunks


retrieval-embedding-http-differential:
	$(CARGO) test -p symdesk-protocol --locked --test retrieval_embedding


retrieval-reembed-http-differential:
	$(CARGO) test -p symdesk-cli --locked --test index_reembed_http


retrieval-local-hash-differential:
	$(CARGO) test -p symdesk-index --locked local_hash_tests

.PHONY: retrieval-document-extraction-differential

retrieval-document-extraction-differential:
	$(CARGO) test -p symdesk-index --locked retrieval_extract


retrieval-csv-sections-differential:
	$(CARGO) test -p symdesk-index --locked retrieval_csv


retrieval-search-cli-differential:
	$(CARGO) test -p symdesk-cli --locked --test search_hybrid


retrieval-search-mcp-differential:
	$(CARGO) test -p symdesk-cli --locked --test mcp_search_hybrid


retrieval-ask-offline-differential:
	$(CARGO) test -p symdesk-cli --locked --test ask_offline


retrieval-ask-mcp-differential:
	$(CARGO) test -p symdesk-cli --locked --test mcp_ask_offline


retrieval-vector-differential:
	$(CARGO) test -p symdesk-index --locked --test retrieval_vector

# IVF prefilter, Hamming shortlist and heap ranking of the Go vector layer.

retrieval-ivf-differential:
	$(CARGO) test -p symdesk-index --locked --test retrieval_ivf

# Pinned CoreKit TurboQuant codec parity (#1137): Go oracle bytes, Rust replay,
# and the Go decoder reading Rust-written sidecars.
retrieval-turboquant-differential:
	$(CARGO) test -p symdesk-index --locked --test turboquant_codec
	$(CARGO) test -p symdesk-index --locked --test turboquant_property

# Go vs Rust p95/throughput; a Rust regression above 10% exits nonzero.
TURBOQUANT_BENCH_REPORT ?= docs/rust-port/turboquant-benchmark.md
retrieval-turboquant-benchmark:
	$(CARGO) build --release --locked -p symdesk-index --bin turboquant-bench


.PHONY: render-emit-differential
render-emit-differential:
	$(CARGO) test -p symdesk-render --test draw_emit_contract --locked

# PNG bytes depend on the recording architecture (see renderpnggen); the Go
# check is skipped off arm64 while the Rust replay always runs.
.PHONY: render-png-differential
render-png-differential:
	$(CARGO) test -p symdesk-render --test draw_png_contract --locked

# GIF frames reuse the PNG rasterizer, so the same arm64 recording applies.
.PHONY: render-gif-differential
render-gif-differential:
	$(CARGO) test -p symdesk-render --test draw_gif_contract --locked

.PHONY: render-typst-differential
render-typst-differential:
	$(CARGO) test -p symdesk-render --test draw_typst_contract --locked

.PHONY: render-facade-differential
render-facade-differential:
	$(CARGO) test -p symdesk-render --test draw_facade_contract --locked

render-ir-differential: render-emit-differential render-png-differential render-gif-differential render-typst-differential render-facade-differential
	$(CARGO) test -p symdesk-render --locked

# Pure noteToHTML parity is paired with the pinned production Go integrity
# gate. Cargo alone compares HTML bytes and digest syntax, not digest values.


render-html-differential:
	$(CARGO) test -p symdesk-render --locked

# Search-result Markdown, view tables and plain links (RENDER-002 pure core).

render-export-differential:
	$(CARGO) test -p symdesk-render --locked export_markdown

# Vault note/view export: transclusion expansion, embed error lines, path
# confinement and the Markdown/HTML/PDF-source shapes, over real Go-built
# disposable vaults (RENDER-002). Unix-only replay.

render-export-vault-differential:
	$(CARGO) test -p symdesk-render --locked export_vault

# PDF frontmatter block emitted before typesetting (RENDER-002 PDF path).

render-pdf-frontmatter-differential:
	$(CARGO) test -p symdesk-render --locked export_pdf

# Pure archive contract (page split, naming, text-layer probe, format dispatch).
# pdfcpu stamping is not covered by this target.

render-archive-differential:
	$(CARGO) test -p symdesk-render --locked archive

# Text stamp semantics (page count, boxes, stamp recovery, error kinds). Byte
# layout and geometry are not compared.

render-archive-stamp-differential:
	$(CARGO) test -p symdesk-render --locked stamp

# pdfcpu image import and archive-stamp geometry/structure: real pdfcpu output
# (Go) against the Rust port through the writer-independent canonical PDF dump
# (scripts/rust-port/pdfops/pdfcanon.py, qpdf). Needs python3 + qpdf.

render-archive-pdf-differential:
	$(CARGO) test -p symdesk-render --locked --test archive_pdf_contract

# Press (Typst) render pipeline: strict frontmatter contract and meta.json bytes,
# then full renders through the installed typst (typed errors, resolved options,
# PDF/A and PDF/UA gates, reproducible PDF digests). Needs typst, python3, qpdf.

render-press-differential:
	$(CARGO) test -p symdesk-render --locked --test press_render_contract


dataset-aggregate-differential:
	$(CARGO) test -p symdesk-index --locked --test dataset_aggregate_contract

# DATA-002 full dataset query: every operator, ordering, grouping, aggregate,
# paging and dataset-sourced view projection, replayed from Go recordings
# (query-full.json), plus the frozen CLI cases (cli-query-full.json) for the
# --group-by/--aggregates flags. No live Go process runs.
.PHONY: dataset-query-full-differential

dataset-query-full-differential:
	$(CARGO) test -p symdesk-index --locked --test dataset_query_full


index-backup-differential:
	$(CARGO) test -p symdesk-index --locked --test index_backup


index-restore-differential:
	$(CARGO) test -p symdesk-index --locked --test index_restore


index-relocate-differential:
	$(CARGO) test -p symdesk-index --locked --test index_relocate


index-location-differential:
	$(CARGO) test -p symdesk-index --locked --test index_location


index-maintenance-cli-differential:
	$(CARGO) test -p symdesk-cli --locked --test index_maintenance


index-build-cli-differential:
	$(CARGO) test -p symdesk-cli --locked --test index_build


index-retrieval-cli-differential:
	$(CARGO) test -p symdesk-cli --locked --test index_retrieval


index-retry-cli-differential:
	$(CARGO) test -p symdesk-cli --locked --test index_retry


index-symlink-cli-differential:
	$(CARGO) test -p symdesk-cli --locked --test index_symlink


sources-cli-differential:
	$(CARGO) test -p symdesk-cli --locked --test sources

# RUST-008 acceptance aggregates. `retrieval-differential` runs every retrieval
# differential; `index-crate-test` is `cargo nextest run -p symdesk-index`;
RETRIEVAL_DIFFERENTIALS := retrieval-chunks-differential retrieval-bm25-differential \
	retrieval-embedding-state-differential retrieval-vector-differential retrieval-ivf-differential \
	retrieval-hybrid-differential retrieval-pending-rebuild-differential \
	retrieval-sections-differential retrieval-embedding-http-differential \
	retrieval-reembed-http-differential retrieval-local-hash-differential \
	retrieval-document-extraction-differential retrieval-csv-sections-differential \
	retrieval-search-cli-differential retrieval-search-mcp-differential \
	retrieval-ask-offline-differential retrieval-ask-mcp-differential \
	retrieval-turboquant-differential
.PHONY: retrieval-differential index-crate-test index-status-cli-differential
retrieval-differential: $(RETRIEVAL_DIFFERENTIALS)

index-crate-test:
	$(CARGO) nextest run -p symdesk-index --all-features --locked --no-fail-fast

# `symdesk index status` (126 cases, #1225): the canonical Go oracle was run
# once through the reviewed capture.py world builder and case runner and
# recorded in testdata/port/cli/index-status-recording.json; frozen.py replays
# the same inventory with the Rust binary, no Go needed.
index-status-cli-differential:
	$(CARGO) test -p symdesk-cli --locked --test index_status_frozen



room-run-approval-cli-differential:
	$(CARGO) test -p symroom-cli --locked --test run_approval_commands


room-io-error-cli-differential:
	$(CARGO) test -p symroom-cli --locked --test io_error_commands


room-flag-grammar-cli-differential:
	$(CARGO) test -p symroom-cli --locked --test flag_grammar_commands


room-identity-provider-cli-differential:
	$(CARGO) test -p symroom-cli --locked --test identity_provider_commands


room-store-cli-differential:
	$(CARGO) test -p symroom-cli --locked --test store_commands


history-prune-differential:
	$(CARGO) test -p symdesk-vault --locked --test history_prune_contracts


history-service-differential:
	$(CARGO) test -p symdesk-index --locked --test history_service_contract


history-purge-differential:
	$(CARGO) test -p symdesk-vault --locked --test history_purge_contracts


history-trash-purge-differential:
	$(CARGO) test -p symdesk-vault --locked --test history_trash_purge_contracts


dataset-purge-differential:
	$(CARGO) test -p symdesk-index --locked --test dataset_purge_contract


dataset-sync-differential:
	$(CARGO) test -p symdesk-vault --locked --test dataset_contracts
	$(CARGO) test -p symdesk-index --locked --test dataset_service_contracts
	$(CARGO) test -p symdesk-index --locked --test dataset_import_contracts

# The recording holds what the real Go binary printed for every case; the first
# line proves it still does, the second replays it against the Rust binary
# without Go (RUST-019).
dataset-cli-differential:
	$(CARGO) test -p symdesk-cli --locked --test frozen_diffharness dataset



# Room identity and signed events against the pinned Go oracle (ROOM-001).
symroom-differential:
	$(CARGO) test -p symroom-core --test identity_events_contracts --locked



# Per-vault sidecar metadata side effects (issue #1006): the Go oracle records
# `metadata.json`'s byte encoding and the durable directory contents, the Rust
# replay reproduces both.
sidecar-metadata-differential:
	$(CARGO) test -p symdesk-index --locked --test metadata_contracts


sidecar-differential:
	$(CARGO) test -p symdesk-index --all-features --locked

# Full local/native gate. Lock and permission semantics are deliberately not
# part of the routine Ubuntu PR lane; native CI invokes this target directly.

# RUST-018 cutover data safety. The stable release ships Rust only, so the
# evidence that a Rust binary opens state a Go binary created is frozen:
# testdata/port/cutover/state-v1-<os>.json plus the .cxa archives hold a
# realistic tree a real Go symdesk/symroom built (vault, sidecar and retrieval
# indexes, an ingested document, contacts, saved views, a signed room journal)
# and the Go outputs of read-only queries. The Rust binaries replay everything
# without Go: byte-identical store files after reads, Rust writes, the first-run
# backup and a verified restore.
.PHONY: cutover-state-differential

cutover-state-differential:
	$(CARGO) test -p symdesk-cli --test cutover_state --locked
	$(CARGO) test -p symdesk-cli --test pre_rust_backup --locked
	$(CARGO) test -p symroom-cli --test cutover_state --locked





# Issue #1153: Go-independent contract aggregate. Replays every committed Go
# oracle fixture of each `*-differential` target through Rust with `go` removed
# from PATH, requires the recorded case counts in
# scripts/rust-port/frozen-contract.json, and checks that mutated fixtures fail.
.PHONY: port-contract-frozen
port-contract-frozen:
	PYTHONDONTWRITEBYTECODE=1 python3 -m unittest scripts/rust-port/test_frozen_contract.py
	PYTHONDONTWRITEBYTECODE=1 python3 scripts/rust-port/frozen_contract.py

# Go-free contract aggregate (RUST-019). The Go oracle is retired, so the
# contract is the frozen replay of every committed Go-recorded fixture plus the
# fixture checksum/provenance check; no target here needs a Go toolchain.
.PHONY: port-contract port-provenance port-provenance-history
port-contract: port-provenance port-contract-frozen

# Every fixture checksum in testdata/port/provenance.json (234 files), the
# recorded oracle identity and the embedded oracle bundle.
port-provenance:
	PYTHONDONTWRITEBYTECODE=1 python3 -m unittest scripts/rust-port/test_provenance_check.py
	PYTHONDONTWRITEBYTECODE=1 python3 scripts/rust-port/provenance_check.py

# Optional, needs the full git history: also binds every fixture `source_hashes`
# entry to the Go blobs at the last Go commit (36aad3d4, reachable via the
# branch oracle-pins/last-go-36aad3d4).
port-provenance-history:
	PYTHONDONTWRITEBYTECODE=1 python3 scripts/rust-port/provenance_check.py --history




# VAULT-006 CLI slice: the Go and Rust `symdesk retention` command trees are run
# on the same synthetic vault and compared on stdout, stderr, exit code and the
# written vault files.
retention-cli-differential:
	$(CARGO) test -p symdesk-cli --locked --test frozen_diffharness retention


# CLI-000: the Go diffharness cases are recorded in
# testdata/port/representative/cli-recording.json and replayed by Rust without Go.
# The HTTP half of this target (`http-differential`, a live Go/Rust comparison of
# the 291-case testdata/port/http/representative.json corpus) is retired; see the
# "retired" entry of scripts/rust-port/frozen-contract.json and the README section
# "Go recordings of the formerly live gates". The `httpdiff` tool is gone with the Go sources.
representative-differential:
	$(CARGO) test -p symdesk-cli --locked --test frozen_diffharness representative


# VALUE-001: retained paired Go/Rust benchmark evidence. The benchmark itself
# needed a Go build and is retired with the Go tree (RUST-019); only the
# validation of the retained, Go-recorded results stays.
.PHONY: value-runtime-dirs value-001-evidence-tests
VALUE_RETAINED ?= docs/rust-port/results/value001-retained.json
VALUE_RUNTIME_ROOT ?= /Volumes/1TB_NVMe_SN850X/Dev/Symaira_Dev/builds/BuildTargets/symaira-desktop-value001-$(shell git rev-parse --short HEAD)
VALUE_RUSTUP_HOME ?= /Volumes/1TB_NVMe_SN850X/Dev/caches/rustup
VALUE_RUNTIME_ENV = HOME="$(VALUE_RUNTIME_ROOT)/home" USERPROFILE="$(VALUE_RUNTIME_ROOT)/home" TMPDIR="$(VALUE_RUNTIME_ROOT)/tmp" TMP="$(VALUE_RUNTIME_ROOT)/tmp" TEMP="$(VALUE_RUNTIME_ROOT)/tmp" XDG_CONFIG_HOME="$(VALUE_RUNTIME_ROOT)/xdg-config" XDG_DATA_HOME="$(VALUE_RUNTIME_ROOT)/xdg-data" XDG_CACHE_HOME="$(VALUE_RUNTIME_ROOT)/xdg-cache" PYTHONDONTWRITEBYTECODE=1 PYTHONPYCACHEPREFIX="$(VALUE_RUNTIME_ROOT)/pycache" GOCACHE="$(VALUE_RUNTIME_ROOT)/go-cache" GOMODCACHE="$(VALUE_RUNTIME_ROOT)/go-modcache" GOPATH="$(VALUE_RUNTIME_ROOT)/gopath" RUSTUP_HOME="$(VALUE_RUSTUP_HOME)" CARGO_HOME="$(VALUE_RUNTIME_ROOT)/cargo-home" CARGO_TARGET_DIR="$(RUST_TARGET_DIR)" GOTOOLCHAIN=local
# Keep local SEC-003 outputs and the Rust toolchain cache on the attached NVMe.

value-runtime-dirs:
	@mkdir -p "$(VALUE_RUNTIME_ROOT)/home" "$(VALUE_RUNTIME_ROOT)/tmp" "$(VALUE_RUNTIME_ROOT)/xdg-config" "$(VALUE_RUNTIME_ROOT)/xdg-data" "$(VALUE_RUNTIME_ROOT)/xdg-cache" "$(VALUE_RUNTIME_ROOT)/pycache"


value-001-evidence-tests: value-runtime-dirs
	$(VALUE_RUNTIME_ENV) python3 -m unittest discover -s scripts/rust-port -p 'test_*value001*.py' -v
	$(VALUE_RUNTIME_ENV) python3 scripts/rust-port/validate_value001_retained.py "$(VALUE_RETAINED)"
	$(VALUE_RUNTIME_ENV) python3 scripts/rust-port/value001_report.py "$(VALUE_RETAINED)"
	$(VALUE_RUNTIME_ENV) python3 scripts/rust-port/value001_report.py docs/rust-port/results/value001-latest.json

# Explicit candidate acceptance; historical evidence tests do not approve HEAD.



# VALUE-002: fail-closed full-release gate (docs/rust-port/value002.md); owner-
# accepted for 8ba6eb5b. Kept for auditing the retained captures: the runner
# never compiles and measures two prebuilt release archives (the Go archive is
# the retained v0.12.2-era build, no Go toolchain is needed). The release candidates are built beforehand as direct
# dev-external commands and documented with `value002.py record-build`
# (build record), then measured here from the two release archives.
.PHONY: value-002 value-002-tests value-002-validate
VALUE002_DIR ?= /Volumes/1TB_NVMe_SN850X/Dev/Symaira_Dev/builds/symaira-desktop/value002
VALUE002_RECORD ?= $(VALUE002_DIR)/build-record.json
VALUE002_GO_ARCHIVE ?= $(VALUE002_DIR)/rebuild/go-archive/symaira-desktop_0.12.2_darwin_arm64.tar.gz
VALUE002_RUST_ARCHIVE ?= $(VALUE002_DIR)/rebuild/rust-archive/symaira-desktop_0.12.2_darwin_arm64.tar.gz
VALUE002_TOOLS ?= $(VALUE002_DIR)/rebuild/tools
VALUE002_OUTPUT ?= docs/rust-port/results/value002-latest.json
VALUE002_BINARIES ?= $(VALUE002_DIR)/measured

value-002-tests: value-runtime-dirs
	$(VALUE_RUNTIME_ENV) python3 -m unittest discover -s scripts/rust-port -p 'test_value002*.py' -v

value-002: value-runtime-dirs
	@mkdir -p "$$(dirname "$(VALUE002_OUTPUT)")" "$(VALUE002_BINARIES)"
	$(VALUE_RUNTIME_ENV) python3 scripts/rust-port/value002.py run \
		--root . \
		--build-record "$(VALUE002_RECORD)" \
		--go-archive "$(VALUE002_GO_ARCHIVE)" \
		--rust-archive "$(VALUE002_RUST_ARCHIVE)" \
		--contract-tools "$(VALUE002_TOOLS)" \
		--binary-dir "$(VALUE002_BINARIES)" \
		--output "$(VALUE002_OUTPUT)"

value-002-validate: value-runtime-dirs
	@test -n "$(VALUE002_CANDIDATE)"
	$(VALUE_RUNTIME_ENV) python3 scripts/rust-port/validate_value002.py "$(VALUE002_OUTPUT)" --candidate "$(VALUE002_CANDIDATE)"



# The raw-process representative MCP corpus (MCP-001 and the early surface) was
# recorded once from the real Go `symdesk mcp` into
# testdata/port/mcp/representative-recording.json; the Rust replay needs no Go.
# The former live Go/Rust comparison is retired: it compared the same requests,
# now frozen.


mcp-differential:
	$(CARGO) test -p symdesk-cli --locked --test mcp_representative

.PHONY: mcp-initialize-differential
mcp-initialize-differential:
	$(CARGO) test -p symdesk-cli --locked --test mcp_representative mcp001

.PHONY: history-differential
history-differential:
	$(CARGO) test -p symdesk-vault --locked --test history_contracts


rust-build:
	$(CARGO) build --workspace --locked

rust-check:
	$(CARGO) check --workspace --all-targets --all-features --locked

rust-lint:
	$(CARGO) fmt --all --check
	$(CARGO) clippy --workspace --all-targets --all-features --locked -- -D warnings

rust-test:
	$(CARGO) nextest run --workspace --all-features --locked --no-fail-fast
	$(CARGO) test --workspace --doc --all-features --locked

rust-features:
	$(CARGO) hack check --workspace --each-feature --locked

rust-coverage:
	$(CARGO) llvm-cov nextest --workspace --all-features --locked --no-fail-fast --summary-only

rust-security:
	$(CARGO) audit
	$(CARGO) deny check

# SEC-004 unsafe policy. The deterministic inventory is the gate (default
# deny(unsafe_code) at every crate root, exact per-file `unsafe` counts against
# scripts/rust-port/unsafe-allowlist.json, SAFETY comments, a documented focused
# test per exception). It has a negative-control self test. cargo-geiger is not
# the gate: it works per package (`cargo geiger --manifest-path <abs crate
# Cargo.toml>`), counts dependency code and cannot fail on a workspace-owned
# regression; use it ad hoc as an inventory only.
rust-unsafe-inventory:
	PYTHONDONTWRITEBYTECODE=1 python3 -m unittest scripts/rust-port/test_unsafe_inventory.py
	python3 scripts/rust-port/unsafe-inventory.py

# CORE-002/003 (and CORE-001, German search normalization) under Miri: the
# core_contracts replay reads only include_str! fixtures, so no filesystem or
# process isolation exception is needed. Install once with
#   rustup toolchain install $(RUST_NIGHTLY) --profile minimal --component miri,rust-src
# The gate fails when Miri runs fewer than MIRI_MIN_TESTS cases: zero or
# filtered-out tests are not a pass.
MIRI_MIN_TESTS ?= 4
rust-miri:
	@out="$$(mktemp)"; \
	$(CARGO) +$(RUST_NIGHTLY) miri test -p symdesk-core --test core_contracts --locked > "$$out" 2>&1; rc=$$?; \
	cat "$$out"; \
	if [ $$rc -ne 0 ]; then rm -f "$$out"; exit $$rc; fi; \
	passed="$$(sed -n 's/^test result: ok\. \([0-9][0-9]*\) passed.*/\1/p' "$$out" | tail -n 1)"; rm -f "$$out"; \
	if [ "$${passed:-0}" -lt "$(MIRI_MIN_TESTS)" ]; then echo "rust-miri: only $${passed:-0} test(s) executed, expected >= $(MIRI_MIN_TESTS)" >&2; exit 1; fi; \
	echo "rust-miri: $$passed test(s) executed under Miri ($(RUST_NIGHTLY))"

rust-version-contract:
	$(CARGO) test -p symdesk-cli --locked --test version
	$(CARGO) test -p symroom-cli --locked --test version
	SYMDESK_VERSION=0.12.2 SYMROOM_VERSION=0.12.2 $(CARGO) build --release --workspace --locked
	test "$$("$(RUST_TARGET_DIR)/release/symdesk$(EXE_SUFFIX)" version --json)" = '{"tool":"symdesk","version":"0.12.2","schema_version":1}'
	test "$$("$(RUST_TARGET_DIR)/release/symroom$(EXE_SUFFIX)" version --json)" = '{"tool":"symroom","version":"0.12.2","schema_version":1}'

rust-fuzz-smoke:
	$(CARGO) +$(RUST_NIGHTLY) fuzz run frontmatter -- -runs=$(FUZZ_RUNS) -max_len=65536
	$(CARGO) +$(RUST_NIGHTLY) fuzz run room_event -- -runs=$(FUZZ_RUNS) -max_len=65536
	$(CARGO) +$(RUST_NIGHTLY) fuzz run mime_input -- -runs=$(FUZZ_RUNS) -max_len=65536

# Current-build native evidence, independent of the historical version corpus.
.PHONY: native-record
native-record:
	@test -n "$(SYMDESK_NATIVE_RECORD)" || { echo "set SYMDESK_NATIVE_RECORD to a fresh evidence directory" >&2; exit 2; }
	@test -n "$(NATIVE_RECORD_TEMP_ROOT)" || { echo "set NATIVE_RECORD_TEMP_ROOT outside /private/tmp" >&2; exit 2; }
	@python3 scripts/rust-port/test-native-record.py
	@SYMDESK_VERSION="$(if $(VERSION),$(VERSION),(devel))" $(CARGO) build --release -p symdesk-cli --locked
	@python3 scripts/rust-port/native-record.py --binary "$(RUST_TARGET_DIR)/release/symdesk" \
		--expected-version "$(if $(VERSION),$(VERSION),(devel))" --temp-root "$(NATIVE_RECORD_TEMP_ROOT)"

rust-gates: rust-check rust-lint rust-test rust-features rust-coverage rust-security rust-unsafe-inventory rust-version-contract

clean:
	$(CARGO) clean
	rm -rf bin/

.PHONY: ai-provider-differential

ai-provider-differential:
	$(CARGO) test -p symdesk-core --locked --test ai_providers

.PHONY: compose-probe-differential

compose-probe-differential:
	$(CARGO) test -p symdesk-core --locked --test compose_probe

.PHONY: compose-resolve-differential

compose-resolve-differential:
	$(CARGO) test -p symdesk-core --locked --test compose_resolve


recipe-validate-differential:
	$(CARGO) test -p symdesk-cli --locked --test recipe_validate

.PHONY: ai-compose-differential
# RUST-012: provider selection/streaming, sibling-tool discovery and the
# reviewed recipe runner, each replayed against Go-recorded fixtures.
ai-compose-differential: ask-hermes-differential secret-canary-differential secrets-resolve-differential ai-provider-differential compose-memory-differential ai-compose-hermes-differential transform-hermes-differential compose-probe-differential compose-resolve-differential recipe-validate-differential recipe-run-differential

.PHONY: ask-hermes-differential secret-canary-differential secrets-resolve-differential compose-memory-differential ai-compose-hermes-differential transform-hermes-differential


ask-hermes-differential:
	$(CARGO) test -p symdesk-cli --locked --test ask_hermes

# SEC-002: Rust-side canary scan of a failing provider that echoes the key.
secret-canary-differential:
	$(CARGO) test -p symdesk-cli --locked --test secret_canary


secrets-resolve-differential:
	$(CARGO) test -p symdesk-service --locked --test secrets_resolve


compose-memory-differential:
	$(CARGO) test -p symdesk-service --locked --test compose_memory


transform-hermes-differential:
	$(CARGO) test -p symdesk-cli --locked --test transform_hermes

.PHONY: ask-provider-mcp-differential ask-cancel-differential

ask-provider-mcp-differential:
	$(CARGO) test -p symdesk-cli --locked --test mcp_ask_provider


ask-cancel-differential:
	$(CARGO) test -p symdesk-cli --locked --test ask_cancel

# CLI-007: signal and lifecycle contract of events, mcp, serve, worker,
# `sources watch` and the children they spawn (Go-recorded real processes).
.PHONY: cli-signals-differential

cli-signals-differential:
	$(CARGO) test -p symdesk-cli --locked --test signals

.PHONY: ask-agent-differential

ask-agent-differential:
	$(CARGO) test -p symdesk-cli --locked --test ask_agent

ai-compose-hermes-differential:
	$(CARGO) test -p symdesk-service --locked --test ai_compose


recipe-run-differential:
	$(CARGO) test -p symdesk-cli --locked --test recipe_run

.PHONY: mcp-write-differential
mcp-write-differential:
	$(CARGO) test -p symdesk-cli --test mcp_write --locked


.PHONY: mcp-protocol-differential

mcp-protocol-differential:
	$(CARGO) test -p symdesk-cli --test mcp_protocol --locked

# RUST-014 conformance: every recorded Go MCP transcript (raw protocol, catalog,
# read/write/remaining tool calls) replayed against the native server, plus the
# pure transport port and its bounded property tests.
.PHONY: mcp-calls-differential

mcp-calls-differential:
	$(CARGO) test -p symdesk-cli --test mcp_calls --locked

# MCP-002: every legacy alias answers byte for byte like its canonical tool.
.PHONY: mcp-aliases
mcp-aliases:
	$(CARGO) test -p symdesk-cli --locked --test mcp_aliases

# SEC-002 (MCP part): planted credentials never reach responses, stderr or
# files; includes a planted-leak negative control.
.PHONY: mcp-secret-canary
mcp-secret-canary:
	$(CARGO) test -p symdesk-cli --locked --test mcp_secret_canary

.PHONY: mcp-conformance
mcp-conformance: mcp-protocol-differential mcp-calls-differential mcp-aliases mcp-secret-canary
	$(CARGO) test -p symdesk-protocol --test mcp_frame --locked
	$(CARGO) test -p symdesk-cli --locked --test mcp_surface --test mcp_read --test mcp_write --test mcp_initialize

.PHONY: note-doc-cli-differential
note-doc-cli-differential:
	$(CARGO) test -p symdesk-cli --test note_doc --locked

.PHONY: conflict-cli-differential
conflict-cli-differential:
	$(CARGO) test -p symdesk-cli --test conflict --locked

.PHONY: restore-cli-differential
restore-cli-differential:
	$(CARGO) test -p symdesk-cli --test restore --locked

# SWIFT-001/002/003: the native apps against the Rust `symdesk` (no Go).
# SYMDESK_CONSUMER_BINARY must name the Rust binary; without it the target
# fails, so the env-gated XCTest classes can never "pass" by skipping.
# See scripts/rust-port/swift-consumer.sh for the optional knobs
# (SWIFT_CONSUMER_XCODEBUILD, SWIFT_CONSUMER_BUILD_DIR, SWIFT_CONSUMER_SIM_ID,
# SWIFT_CONSUMER_ONLY).
.PHONY: swift-consumer
swift-consumer:
	@test -n "$(SYMDESK_CONSUMER_BINARY)" || { echo "swift-consumer: SYMDESK_CONSUMER_BINARY (absolute path of the Rust symdesk binary) is required" >&2; exit 2; }
	SYMDESK_CONSUMER_BINARY="$(SYMDESK_CONSUMER_BINARY)" scripts/rust-port/swift-consumer.sh

# DIST-001/002: Rust release archives (replacing GoReleaser). Builds the host
# target with a contract version, packages it and checks the member contract
# and `version` output of both binaries. CI's release workflow runs the same
# scripts for every target.
RELEASE_VERSION ?= 0.0.0-contract
RELEASE_DIST ?= $(RUST_TARGET_DIR)/release-contract
RELEASE_HOST := $(shell python3 -c "import platform;print({'Darwin':'darwin','Linux':'linux'}.get(platform.system(),'unsupported'), {'arm64':'arm64','aarch64':'arm64','x86_64':'amd64'}.get(platform.machine(),'unsupported'))")
.PHONY: release-contract container-smoke
release-contract:
	PYTHONDONTWRITEBYTECODE=1 python3 -m unittest scripts/release/test_package.py
	rm -rf "$(RELEASE_DIST)"
	scripts/release/build.sh $(RELEASE_HOST) "$(RELEASE_VERSION)" "$(RELEASE_DIST)"

# DIST-004: build the server image and run the ecosystem smoke against it.
container-smoke:
	docker build --build-arg VERSION=$(RELEASE_VERSION) -t symdesk-smoke:local .
	scripts/release/container-smoke.sh symdesk-smoke:local "$(RELEASE_VERSION)"
