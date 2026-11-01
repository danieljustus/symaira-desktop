.PHONY: paperless-mapping-differential paperless-mapping-fixtures-generate
paperless-mapping-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest/internal/paperlessimport -run '^TestPortPaperlessMappingContract$$'
	$(CARGO) test -p symdesk-ingest --test paperless_mapping_contracts --locked

paperless-mapping-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest/internal/paperlessimport -run '^TestPortPaperlessMappingContract$$'

.PHONY: paperless-state-differential paperless-state-fixtures-generate
paperless-state-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest/internal/store -run '^TestPortPaperlessStateContract$$' -args -port-paperless-state
	$(CARGO) test -p symdesk-ingest --test paperless_state_contracts --locked

paperless-state-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest/internal/store -run '^TestPortPaperlessStateContract$$' -args -port-paperless-state

.PHONY: paperless-export-differential paperless-export-fixtures-generate
paperless-export-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/paperless -run '^TestPortPaperlessExport(Unicode)?Contract$$'
	$(CARGO) test -p symdesk-ingest --test paperless_export_contracts --locked

paperless-export-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/paperless -run '^TestPortPaperlessExport(Unicode)?Contract$$'

.PHONY: paperless-http-differential paperless-http-fixtures-generate
paperless-http-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest/internal/paperless -run '^TestPortPaperlessHTTPContract$$'
	$(CARGO) test -p symdesk-ingest --test paperless_http_contracts --locked

paperless-http-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest/internal/paperless -run '^TestPortPaperlessHTTPContract$$'

.PHONY: notion-import-differential notion-import-fixtures-generate
notion-import-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest/internal/notionimport -run '^TestPortNotionContract$$'
	$(CARGO) test -p symdesk-ingest --test notion_contracts --locked

notion-import-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest/internal/notionimport -run '^TestPortNotionContract$$'

.PHONY: ingest-queue-differential ingest-queue-fixtures-generate
INGEST_GO ?= go
ingest-queue-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest/internal/store -run '^TestPortIngestQueueContract$$' -args -port-ingest-queue
	$(CARGO) test -p symdesk-ingest --locked

ingest-queue-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest/internal/store -run '^TestPortIngestQueueContract$$' -args -port-ingest-queue

.PHONY: ingest-store-differential ingest-classification-differential ingest-store-fixtures-generate
ingest-store-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest/internal/store -run '^TestPortIngestStoreContract$$' -args -port-ingest-store
	$(CARGO) test -p symdesk-ingest --test store_contracts --locked

ingest-store-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest/internal/store -run '^TestPortIngestStoreContract$$' -args -port-ingest-store

.PHONY: ingest-classification-differential ingest-classification-fixtures-generate
ingest-classification-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest -run '^TestPortIngestClassificationContract$$'
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest/internal/ingest -run '^TestPortIngestClassifyPipelineContract$$'
	$(CARGO) test -p symdesk-ingest --test classification_contracts --locked

ingest-classification-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest -run '^TestPortIngestClassificationContract$$'
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest/internal/ingest -run '^TestPortIngestClassifyPipelineContract$$'

.PHONY: ingest-extraction-differential ingest-extraction-fixtures-generate
ingest-extraction-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest/internal/extract -run '^TestPortIngestExtractionContract$$'
	$(CARGO) test -p symdesk-ingest --test extraction_contracts --locked

ingest-extraction-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest/internal/extract -run '^TestPortIngestExtractionContract$$'

.PHONY: ingest-structured-differential ingest-structured-fixtures-generate
ingest-structured-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest/internal/extract -run '^TestPortIngestStructuredContract$$'
	$(CARGO) test -p symdesk-ingest --test structured_contracts --locked

ingest-structured-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest/internal/extract -run '^TestPortIngestStructuredContract$$'

.PHONY: ingest-magika-differential ingest-magika-fixtures-generate
ingest-magika-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest/internal/extract -run '^TestPortIngestMagikaContract$$'
	$(CARGO) test -p symdesk-ingest --test magika_contracts --locked

ingest-magika-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest/internal/extract -run '^TestPortIngestMagikaContract$$'

.PHONY: ingest-pipeline-differential ingest-pipeline-fixtures-generate
ingest-pipeline-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest/internal/ingest -run '^TestPortIngestPipelineContract$$'
	$(CARGO) test -p symdesk-ingest --test pipeline_contracts --locked

ingest-pipeline-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest/internal/ingest -run '^TestPortIngestPipelineContract$$'

.PHONY: ingest-barcode-differential ingest-barcode-fixtures-generate
ingest-barcode-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest -run '^TestPortIngestBarcodeContract$$'
	$(CARGO) test -p symdesk-ingest --test barcode_contracts --locked

ingest-barcode-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest -run '^TestPortIngestBarcodeContract$$'

.PHONY: ingest-vlm-differential ingest-vlm-fixtures-generate
ingest-vlm-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest/internal/ocr -run '^TestPortIngestVLMContract$$'
	$(CARGO) test -p symdesk-ingest --test vlm_contracts --locked

ingest-vlm-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest/internal/ocr -run '^TestPortIngestVLMContract$$'

.PHONY: ingest-paperless-run-differential ingest-paperless-run-fixtures-generate
ingest-paperless-run-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest/internal/paperlessimport -run '^TestPortPaperlessRunContract$$'
	$(CARGO) test -p symdesk-ingest --test paperless_run_contracts --locked

ingest-paperless-run-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest/internal/paperlessimport -run '^TestPortPaperlessRunContract$$'

.PHONY: ingest-paperless-verify-differential ingest-paperless-verify-fixtures-generate
ingest-paperless-verify-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest/internal/paperlessimport -run '^TestPortPaperlessVerifyContract$$'
	$(CARGO) test -p symdesk-ingest --test paperless_verify_contracts --locked

ingest-paperless-verify-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest/internal/paperlessimport -run '^TestPortPaperlessVerifyContract$$'

.PHONY: ingest-notion-report-differential ingest-notion-report-fixtures-generate
ingest-notion-report-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest/internal/notionimport -run '^TestPortNotionReportContract$$'
	$(CARGO) test -p symdesk-ingest --test notion_report_contracts --locked

ingest-notion-report-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest/internal/notionimport -run '^TestPortNotionReportContract$$'

.PHONY: ingest-mail-differential ingest-mail-fixtures-generate
ingest-mail-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest/internal/ingest -run '^TestPortIngestMailContract$$'
	$(CARGO) test -p symdesk-ingest --test mail_contracts --test mail_behaviour --locked

ingest-mail-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest/internal/ingest -run '^TestPortIngestMailContract$$'

.PHONY: ingest-watcher-differential ingest-watcher-fixtures-generate
ingest-watcher-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest/internal/ingest -run '^TestPortIngestWatcherContract$$'
	$(CARGO) test -p symdesk-ingest --test watcher_contracts --locked

ingest-watcher-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest/internal/ingest -run '^TestPortIngestWatcherContract$$'

.PHONY: ingest-ocr-differential ingest-ocr-fixtures-generate
ingest-ocr-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest/internal/ocr -run '^TestPortIngestOCRContract$$'
	$(CARGO) test -p symdesk-ingest --test ocr_contracts --locked

ingest-ocr-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./internal/ingest/internal/ocr -run '^TestPortIngestOCRContract$$'

# RUST-010 acceptance: every ingest contract replays its Go-generated fixture.
.PHONY: ingest-differential
ingest-differential: ingest-queue-differential ingest-store-differential ingest-extraction-differential ingest-structured-differential ingest-magika-differential ingest-pipeline-differential ingest-barcode-differential ingest-vlm-differential ingest-paperless-run-differential ingest-paperless-verify-differential ingest-notion-report-differential ingest-mail-differential ingest-watcher-differential ingest-ocr-differential notion-import-differential paperless-mapping-differential paperless-state-differential paperless-export-differential paperless-http-differential

.PHONY: benchmark-large boundary-guard build build-identity-check clean core-differential core-fixtures-check core-fixtures-generate corekit-guard differential-go-selftest docker-build fmt-check font-guard frontmatter-write-differential http-differential lint mcp-differential mcp-fixtures-check mcp-fixtures-generate nested-version-guard port-contract port-fixtures-apply port-fixtures-check port-fixtures-generate release-signing-guard representative-differential representative-fixtures-check representative-fixtures-generate resource-stress rust-build rust-check rust-coverage rust-features rust-fuzz-smoke rust-gates rust-lint rust-security rust-test rust-version-contract room-journal-differential room-journal-fixtures-generate sidecar-differential sidecar-fixtures-check sidecar-fixtures-generate sidecar-metadata-differential sidecar-metadata-fixtures-generate sidecar-roundtrip symroom-differential symroom-fixtures-generate test value-001 value-001-validate vault-fixtures-check vault-fixtures-generate vault-history-differential vault-history-fixtures-generate vault-read-differential vault-retention-differential vault-retention-fixtures-generate vault-write-differential vault-write-fixtures-generate vuln

.PHONY: retention-state-fixtures-generate retention-state-differential
.PHONY: retention-cli-regressions-test
.PHONY: source-differential
.PHONY: source-watch-differential
.PHONY: room-run-projection-fixtures-generate room-run-projection-differential
.PHONY: room-run-cli-fixtures-generate room-run-cli-differential
.PHONY: dataset-sync-fixtures-generate dataset-sync-differential
.PHONY: dataset-cli-differential
.PHONY: history-purge-fixtures-generate history-purge-differential
.PHONY: history-trash-purge-fixtures-generate history-trash-purge-differential
.PHONY: room-run-wait-cli-fixtures-generate room-run-wait-cli-differential dataset-purge-fixtures-generate dataset-purge-differential
.PHONY: room-mcp-fixtures-generate room-mcp-differential
.PHONY: room-run-mutations-cli-fixtures-generate room-run-mutations-cli-differential
.PHONY: room-note-cli-fixtures-generate room-note-cli-differential
.PHONY: room-merge-read-fixtures-generate room-merge-read-differential history-prune-fixtures-generate history-prune-differential
.PHONY: room-identity-cli-fixtures-generate room-identity-cli-differential
.PHONY: room-index-fixtures-generate room-index-differential history-service-fixtures-generate history-service-differential
.PHONY: room-member-cli-fixtures-generate room-member-cli-differential
.PHONY: room-index-cli-fixtures-generate room-index-cli-differential
.PHONY: room-verify-fixtures-generate room-verify-differential room-verify-cli-fixtures-generate room-verify-cli-differential
.PHONY: index-backup-fixtures-generate index-backup-differential
.PHONY: room-decide-cli-fixtures-generate room-decide-cli-differential
.PHONY: room-log-fixtures-generate room-log-differential room-log-cli-fixtures-generate room-log-cli-differential
.PHONY: index-restore-fixtures-generate index-restore-differential
.PHONY: index-relocate-fixtures-generate index-relocate-differential
.PHONY: index-location-fixtures-generate index-location-differential
.PHONY: room-artifact-cli-fixtures-generate room-artifact-cli-differential
.PHONY: room-watch-stream-fixtures-generate room-watch-stream-differential
.PHONY: room-brain-profile-fixtures-generate room-brain-profile-differential
.PHONY: room-init-core-fixtures-generate room-init-core-differential
.PHONY: room-init-cli-fixtures-generate room-init-cli-differential
.PHONY: room-watch-cli-fixtures-generate room-watch-cli-differential
.PHONY: room-artifact-identity-cli-fixtures-generate room-artifact-identity-cli-differential room-doctor-cli-fixtures-generate room-doctor-cli-differential index-maintenance-cli-fixtures-generate index-maintenance-cli-differential recipe-validate-fixtures-generate recipe-validate-differential recipe-run-fixtures-generate recipe-run-differential
.PHONY: room-checkpoint-cli-fixtures-generate room-checkpoint-cli-differential index-build-cli-fixtures-generate index-build-cli-differential index-retrieval-cli-fixtures-generate index-retrieval-cli-differential index-retry-cli-fixtures-generate index-retry-cli-differential index-symlink-cli-fixtures-generate index-symlink-cli-differential sources-cli-fixtures-generate sources-cli-differential
.PHONY: config-precedence-differential config-vault-selection-differential room-run-approval-cli-fixtures-generate room-run-approval-cli-differential history-tasks-cli-differential
.PHONY: room-io-error-cli-fixtures-generate room-io-error-cli-differential
.PHONY: config-cli-fixtures-generate config-cli-differential
config-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(GO) test -count=1 ./cmd/symdesk -run '^TestPortConfigProcessContract$$'

config-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(GO) test -count=1 ./cmd/symdesk -run '^TestPortConfigProcessContract$$'
	$(CARGO) test -p symdesk-cli --test config --locked

.PHONY: help-cli-fixtures-generate help-cli-differential
help-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(GO) test -count=1 ./cmd/symdesk -run '^TestPortHelpProcessContract$$'

help-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(GO) test -count=1 ./cmd/symdesk -run '^TestPortHelpProcessContract$$'
	$(CARGO) test -p symdesk-cli --test help --locked

# CLI-001/003/004: Cobra command resolution, pflag grammar at every argv
# position, argument counts, required/exclusive flags, --help/--version and
# the generated help/completion/__complete commands, replayed from real Go
# processes. CLI-002: the recorded Cobra tree (paths, aliases, visibility,
# flags, argument validators) rebuilt in Rust and checked against every probe.
.PHONY: cobra-parse-cli-fixtures-generate cobra-parse-cli-differential command-tree-cli-differential
cobra-parse-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(GO) test -count=1 ./cmd/symdesk -run '^TestPortCobraParseProcessContract$$'

cobra-parse-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(GO) test -count=1 ./cmd/symdesk -run '^TestPortCobraParseProcessContract$$'
	$(CARGO) test -p symdesk-cli --test cobra_parse --locked

command-tree-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(GO) test -count=1 ./cmd/symdesk -run '^TestSymdeskCobraInventory$$'
	$(CARGO) test -p symdesk-cli --bin symdesk cobra:: --locked

.PHONY: duplicates-cli-fixtures-generate duplicates-cli-differential
.PHONY: cli-errors-fixtures-generate cli-errors-differential
.PHONY: remote-command-fixtures-generate remote-command-differential
.PHONY: room-flag-grammar-cli-fixtures-generate room-flag-grammar-cli-differential
.PHONY: room-identity-provider-cli-fixtures-generate room-identity-provider-cli-differential
.PHONY: room-store-cli-fixtures-generate room-store-cli-differential
.PHONY: notebook-write-differential
.PHONY: base-view-write-differential
.PHONY: retrieval-chunks-fixtures-generate retrieval-chunks-differential
.PHONY: retrieval-bm25-fixtures-generate retrieval-bm25-differential
.PHONY: retrieval-embedding-state-fixtures-generate retrieval-embedding-state-differential
.PHONY: retrieval-vector-fixtures-generate retrieval-vector-differential retrieval-ivf-fixtures-generate retrieval-ivf-differential
.PHONY: retrieval-turboquant-differential retrieval-turboquant-benchmark
.PHONY: retrieval-hybrid-fixtures-generate retrieval-hybrid-differential
.PHONY: retrieval-pending-rebuild-fixtures-generate retrieval-pending-rebuild-differential
.PHONY: retrieval-sections-fixtures-generate retrieval-sections-differential
.PHONY: retrieval-embedding-http-fixtures-generate retrieval-embedding-http-differential
.PHONY: retrieval-reembed-http-fixtures-generate retrieval-reembed-http-differential
.PHONY: retrieval-local-hash-fixtures-generate retrieval-local-hash-differential
.PHONY: retrieval-csv-sections-fixtures-generate retrieval-csv-sections-differential
.PHONY: retrieval-search-cli-fixtures-generate retrieval-search-cli-differential
.PHONY: retrieval-search-mcp-fixtures-generate retrieval-search-mcp-differential
.PHONY: retrieval-ask-offline-fixtures-generate retrieval-ask-offline-differential
.PHONY: retrieval-ask-mcp-fixtures-generate retrieval-ask-mcp-differential
.PHONY: render-ir-fixtures-generate render-ir-differential
.PHONY: render-html-fixtures-generate render-html-fixtures-check render-html-differential
.PHONY: render-differential render-pdfops-differential render-pdfops-hostile-differential
.PHONY: render-export-fixtures-generate render-export-differential
.PHONY: render-export-vault-fixtures-generate render-export-vault-differential
.PHONY: render-pdf-frontmatter-fixtures-generate render-pdf-frontmatter-differential
.PHONY: render-archive-fixtures-generate render-archive-differential
.PHONY: render-archive-stamp-fixtures-generate render-archive-stamp-differential
.PHONY: render-archive-pdf-fixtures-generate render-archive-pdf-differential
.PHONY: render-press-fixtures-generate render-press-differential

.PHONY: dataset-aggregate-fixtures-generate dataset-aggregate-differential

VERSION ?= $(shell git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//')
LDFLAGS = -X main.version=$(if $(VERSION),$(VERSION),(devel))
ROOM_LDFLAGS = -X github.com/danieljustus/symaira-desktop/internal/room/version.Version=$(if $(VERSION),$(VERSION),(dev))
# Go's VCS stamping recognizes only a .git directory, so a linked worktree
# nested inside another checkout is stamped with that checkout's revision
# (#1189). Point git at this worktree explicitly; in an ordinary checkout these
# are the same paths, and outside Git the prefix is empty.
GO_VCS_ENV := $(shell d=$$(git rev-parse --absolute-git-dir 2>/dev/null) && t=$$(git rev-parse --show-toplevel 2>/dev/null) && printf "GIT_DIR='%s' GIT_WORK_TREE='%s'" "$$d" "$$t")
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
PORT_ORACLE_COMMIT ?= $(shell python3 -c "import json; print(json.load(open('testdata/port/provenance.json'))['oracle']['commit'])")
PORT_ORACLE_RELEASE ?= $(shell python3 -c "import json; print(json.load(open('testdata/port/provenance.json'))['oracle']['release'])")
PORT_ORACLE_ANCHOR ?=
# The sidecar lifecycle fixture uses the same live source P and release.
# portgen enforces this pair during generation and immutable checks.
PORTGEN_SIDECAR_ORACLE_COMMIT ?= $(PORT_ORACLE_COMMIT)
PORTGEN_SIDECAR_ORACLE_RELEASE ?= $(PORT_ORACLE_RELEASE)
PORT_CASES ?= testdata/port/cli/cases.json
RUST_NIGHTLY ?= nightly-2026-09-03
FUZZ_RUNS ?= 10000

# Issue #932: check recipes must not inherit fixture-generation activation
# variables. `override` makes an accidental command-line assignment such as
# `make PORTGEN_CHECK_ENV=:` ineffective; the Go check also strips this set
# before running package-local fixture tests from its immutable snapshot.
override PORTGEN_CHECK_ENV := env -u PORT_GENERATE -u port_generate -u PORT_FIXTURES_GENERATE -u port_fixtures_generate -u PORTGEN_GENERATE -u portgen_generate -u GENERATE_PORT_FIXTURES -u generate_port_fixtures -u SYMDESK_PORT_GENERATE -u symdesk_port_generate -u PORTGEN_SIDECAR_ORACLE_COMMIT -u portgen_sidecar_oracle_commit -u PORTGEN_SIDECAR_ORACLE_RELEASE -u portgen_sidecar_oracle_release -u CONFIGGEN_GENERATE -u configgen_generate -u COREGEN_GENERATE -u coregen_generate -u QUERYGEN_GENERATE -u querygen_generate -u VAULTGEN_GENERATE -u vaultgen_generate -u VAULTFSGEN_GENERATE -u vaultfsgen_generate -u TYPEDVAULTGEN_GENERATE -u typedvaultgen_generate -u REPRESENTATIVEGEN_GENERATE -u representativegen_generate -u MCPGEN_GENERATE -u mcpgen_generate

# Check mode must read the committed fixture, not a caller-selected substitute.
override PORTGEN_CHECK_ENV += -u PORT_FIXTURE_PATH -u port_fixture_path -u PORT_DATASET_IMPORT_FIXTURE -u port_dataset_import_fixture -u PORT_DATASET_ERROR_FIXTURE -u port_dataset_error_fixture
override PORTGEN_CHECK_ENV += -u PORTGEN_FIXTURE_SOURCE_COMMIT -u portgen_fixture_source_commit -u PORTGEN_FIXTURE_SOURCE_RELEASE -u portgen_fixture_source_release
override PORTGEN_CHECK_ENV += -u PORT_RENDER_HTML_GENERATE -u port_render_html_generate
override PORTGEN_CHECK_ENV += -u PORT_RENDER_EXPORT_GENERATE -u port_render_export_generate
override PORTGEN_CHECK_ENV += -u PORT_RENDER_EXPORT_VAULT_GENERATE -u port_render_export_vault_generate
override PORTGEN_CHECK_ENV += -u PORT_RENDER_PDF_FRONTMATTER_GENERATE -u port_render_pdf_frontmatter_generate
override PORTGEN_CHECK_ENV += -u PORT_RENDER_ARCHIVE_GENERATE -u port_render_archive_generate
override PORTGEN_CHECK_ENV += -u PORT_RENDER_ARCHIVE_STAMP_GENERATE -u port_render_archive_stamp_generate -u PORT_RENDER_ARCHIVE_PDF_GENERATE -u port_render_archive_pdf_generate -u PORT_RENDER_PRESS_GENERATE -u port_render_press_generate
override PORTGEN_CHECK_ENV += GOWORK=off GOENV=off GOFLAGS=-mod=readonly

build:
	@mkdir -p bin
	$(GO_VCS_ENV) go build -ldflags="$(LDFLAGS)" -o bin/symdesk ./cmd/symdesk

# Current renderer aggregate only; full RUST-011 (PDF artifacts/archive/draw) is open.
# Supply fresh evidence/runtime roots. Native captures are never overwritten.
render-differential: render-export-differential render-export-vault-differential render-pdf-frontmatter-differential render-archive-differential render-archive-stamp-differential render-archive-pdf-differential render-press-differential render-pdfops-differential render-pdfops-hostile-differential

render-pdfops-differential:
	@test -n "$(RENDER_EVIDENCE_ROOT)" -a -n "$(RENDER_RUNTIME_ROOT)" || { printf '%s\n' 'RENDER_EVIDENCE_ROOT and RENDER_RUNTIME_ROOT are required'; exit 2; }
	python3 scripts/rust-port/pdfops/verify.py --output "$(RENDER_EVIDENCE_ROOT)" --runtime-root "$(RENDER_RUNTIME_ROOT)" $(if $(RENDER_BUILD_LAUNCHER),--build-launcher "$(RENDER_BUILD_LAUNCHER)",)

# Go-oracle hostile PDF replay with the REAL installed pdfinfo/pdfseparate/pdfunite/qpdf
# (hostile_inputs.py corpus). Own sibling roots, so it is independent of the stub-tool target.
render-pdfops-hostile-differential:
	@test -n "$(RENDER_EVIDENCE_ROOT)" -a -n "$(RENDER_RUNTIME_ROOT)" || { printf '%s\n' 'RENDER_EVIDENCE_ROOT and RENDER_RUNTIME_ROOT are required'; exit 2; }
	python3 scripts/rust-port/pdfops/hostile_replay.py --output "$(RENDER_EVIDENCE_ROOT)-pdfops-hostile" --runtime-root "$(RENDER_RUNTIME_ROOT)-pdfops-hostile"

# Verifies the identity of the binary actually built: the stamped revision must
# be this checkout's HEAD and the CLI must run.
build-identity-check: build
	@want=$$(git rev-parse HEAD); got=$$(go version -m bin/symdesk | awk '$$2 ~ /^vcs\.revision=/ { sub(/^vcs\.revision=/, "", $$2); print $$2 }'); \
	if [ "$$got" != "$$want" ]; then echo "bin/symdesk stamped vcs.revision=$$got, want HEAD $$want" >&2; exit 1; fi; \
	bin/symdesk version --json >/dev/null && echo "PASS build identity $$want"

test:
	CGO_ENABLED=0 go test -race ./...

lint: fmt-check corekit-guard boundary-guard nested-version-guard release-signing-guard
	go vet ./...

fmt-check:
	@UNFORMATTED="$$(git ls-files -z -- '*.go' | xargs -0 gofmt -l)"; \
	if [ -n "$$UNFORMATTED" ]; then echo "gofmt diff found:"; echo "$$UNFORMATTED"; exit 1; fi

# Issue #526: corekit dependency pin must stay aligned across all 6 modules.
corekit-guard:
	@./scripts/check-corekit-pins.sh

# Issue #536: packages outside permitted facades must not import absorbed library internals.
boundary-guard:
	@./scripts/check-module-boundaries.sh

# Issue #535: Go version directive and shared dependency versions must stay aligned across root and nested modules.
nested-version-guard:
	@./scripts/check-nested-versions.sh

# Release signing/notarization order and published-byte verification contract.
release-signing-guard:
	@./scripts/check-release-signing.sh

# Issue #352: macOS app text must use .symairaText(role) instead of inline
# .font(.caption/.headline/...) literals so Dynamic Type scales.
font-guard:
	@HITS="$$(grep -rnE '\.font\(\.(caption|caption2|headline|callout|subheadline|title|title2|title3|body|largeTitle)' Sources/SymDeskApp/ || true)"; \
	if [ -n "$$HITS" ]; then echo "$$HITS"; echo "inline role font literals found — use .symairaText(role) (issue #352)"; exit 1; fi

# Issue #753: govulncheck flags known vulnerabilities reachable from this
# module's code paths (its default text/json summary already excludes
# unreachable/informational findings, so no extra flags are needed here).
# Kept out of the `lint` dependency chain — unlike fmt-check/corekit-guard/
# boundary-guard/nested-version-guard, this hits the vulnerability database
# over the network, so it shouldn't make routine offline `make lint` runs
# fail or make lint's runtime depend on network latency. Run it directly, or
# via CI's dedicated govulncheck job.
vuln:
	@govulncheck ./...

benchmark-large:
	go test -run '^$$' -bench BenchmarkLargeVaultIndexAndSearch -benchtime=1x ./internal/demo
	go test -run '^$$' -bench BenchmarkGraphLargeVaultWithEntities -benchtime=1x ./internal/service

docker-build:
	docker build -t symaira-desktop:dev .

override PORTGEN_GENERATE_GO_ENV = env GOWORK=off GOENV=off GOFLAGS=-mod=readonly PORT_GENERATE=1 PORTGEN_FIXTURE_SOURCE_COMMIT=$(PORT_ORACLE_COMMIT) PORTGEN_FIXTURE_SOURCE_RELEASE=$(PORT_ORACLE_RELEASE)

core-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/configgen \
		--oracle-commit $(PORT_ORACLE_COMMIT) \
		--oracle-release $(PORT_ORACLE_RELEASE)
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/coregen \
		--oracle-commit $(PORT_ORACLE_COMMIT) \
		--oracle-release $(PORT_ORACLE_RELEASE)
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/querygen \
		--oracle-commit $(PORT_ORACLE_COMMIT) \
		--oracle-release $(PORT_ORACLE_RELEASE)
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/templatepathgen \
		--oracle-commit $(PORT_ORACLE_COMMIT) \
		--oracle-release $(PORT_ORACLE_RELEASE)

core-fixtures-check:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/configgen --check
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/coregen --check
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/querygen --check
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/templatepathgen --check

core-differential: core-fixtures-check
	$(CARGO) test -p symdesk-core --all-features --locked

# INGEST-006 storage-path templates: the Go-owned fixture records Compile
# acceptance/error text, Eval over field maps and Disambiguate from the
# production internal/templatepath package; Rust replays all of it.
CONTACTS_GO ?= go
.PHONY: contacts-keyprovider-differential
contacts-keyprovider-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(CONTACTS_GO) test -count=1 ./internal/contacts/internal/domain/security -run '^TestPortContactsKeyProviderContract$$' -v
	$(CARGO) test -p symdesk-contacts --locked --test keyprovider_contracts

.PHONY: contacts-erase-differential
contacts-erase-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(CONTACTS_GO) test -count=1 ./internal/contacts/internal/service/security -run '^TestPortContactsEraseContract$$' -v
	$(CARGO) test -p symdesk-contacts --locked --test erase_contracts

.PHONY: contacts-relationship-differential
contacts-relationship-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(CONTACTS_GO) test -count=1 ./internal/contacts/internal/service/relationship -run '^TestPortContactsRelationshipContract$$' -v
	$(CARGO) test -p symdesk-contacts --locked --test relationship_contracts

.PHONY: contacts-crud-differential
contacts-crud-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(CONTACTS_GO) test -count=1 ./internal/contacts/internal/service/contact -run '^TestPortContactsCRUDContract$$' -v
	$(CARGO) test -p symdesk-contacts --locked --test crud_contracts

.PHONY: contacts-vcard-differential
contacts-vcard-differential:
	GOTOOLCHAIN=go1.26.10 $(CONTACTS_GO) test -count=1 ./internal/contacts/internal/domain/importer -run '^TestPortContactsVCardContract$$'
	$(CARGO) test -p symdesk-contacts --locked --test vcard_contracts

.PHONY: contacts-import-differential
contacts-import-differential:
	GOTOOLCHAIN=go1.26.10 $(CONTACTS_GO) test -count=1 ./internal/contacts/internal/domain/importer ./internal/contacts/internal/service/importer -run '^TestPortContacts(CSV|Import)Contract$$'
	$(CARGO) test -p symdesk-contacts --locked --test csv_contracts --test import_contracts

# RUST-009 acceptance: every contacts differential (store, CRUD, relationships,
# timeline/meeting import, memberships, memory links, vCard/CSV/import,
# security/export/backup, erase, key provider, schema) plus the dataset sync,
# purge, query, aggregate and view-projection differentials.
.PHONY: contacts-dataset-differential
contacts-dataset-differential: contacts-keyprovider-differential contacts-erase-differential contacts-relationship-differential contacts-crud-differential contacts-vcard-differential contacts-import-differential contacts-security-differential contacts-memorylink-differential contacts-timeline-differential contacts-membership-differential contacts-schema-differential dataset-sync-differential dataset-purge-differential dataset-aggregate-differential dataset-query-full-differential dataset-cli-differential

.PHONY: contacts-security-fixtures-generate contacts-security-differential
contacts-security-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 $(CONTACTS_GO) test -count=1 ./internal/contacts/internal/domain/security -run '^TestPortContactsRedactContract$$'
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 $(CONTACTS_GO) test -count=1 ./internal/contacts/internal/service/security -run '^TestPortContactsExportBackupContract$$'

# Redact masks, export bytes and the AES-256-GCM/Argon2id backup format. The
# Rust test writes its own backup into a fresh directory and the Go oracle must
# restore it, so both directions of the format are proven in one run.
contacts-security-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(CONTACTS_GO) test -count=1 ./internal/contacts/internal/domain/security -run '^TestPortContactsRedactContract$$' -v
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(CONTACTS_GO) test -count=1 ./internal/contacts/internal/service/security -run '^TestPortContactsExportBackupContract$$' -v
	rm -rf "$(RUST_TARGET_DIR)/contacts-backup-exchange" && mkdir -p "$(RUST_TARGET_DIR)/contacts-backup-exchange"
	PORT_RUST_BACKUP_DIR="$(abspath $(RUST_TARGET_DIR))/contacts-backup-exchange" $(CARGO) test -p symdesk-contacts --locked --test security_contracts
	test -s "$(RUST_TARGET_DIR)/contacts-backup-exchange/rust.bak"
	PORT_RUST_BACKUP="$(abspath $(RUST_TARGET_DIR))/contacts-backup-exchange/rust.bak" $(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(CONTACTS_GO) test -count=1 ./internal/contacts/internal/service/security -run '^TestPortContactsRustBackupRestores$$' -v

.PHONY: contacts-memorylink-fixtures-generate contacts-memorylink-differential
contacts-memorylink-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 $(CONTACTS_GO) test -count=1 ./internal/contacts/internal/service/memorylink -run '^TestPortContactsMemoryLinkContract$$'

contacts-memorylink-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(CONTACTS_GO) test -count=1 ./internal/contacts/internal/service/memorylink -run '^TestPortContactsMemoryLinkContract$$' -v
	$(CARGO) test -p symdesk-contacts --locked --test memorylink_contracts

.PHONY: contacts-timeline-fixtures-generate contacts-timeline-differential
contacts-timeline-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 $(CONTACTS_GO) test -count=1 ./internal/contacts/internal/service/relationship -run '^TestPortContactsTimelineMeetingContract$$'

contacts-timeline-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(CONTACTS_GO) test -count=1 ./internal/contacts/internal/service/relationship -run '^TestPortContactsTimelineMeetingContract$$' -v
	$(CARGO) test -p symdesk-contacts --locked --test timeline_contracts

.PHONY: contacts-membership-fixtures-generate contacts-membership-differential
contacts-membership-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 $(CONTACTS_GO) test -count=1 ./internal/contacts/internal/service/contact -run '^TestPortContactsMembershipRefContract$$'

contacts-membership-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(CONTACTS_GO) test -count=1 ./internal/contacts/internal/service/contact -run '^TestPortContactsMembershipRefContract$$' -v
	$(CARGO) test -p symdesk-contacts --locked --test membership_contracts

.PHONY: contacts-schema-differential
contacts-schema-differential:
	GOTOOLCHAIN=go1.26.10 $(CONTACTS_GO) test -count=1 ./internal/contacts/internal/storage/sqlite -run '^TestPortContactsSchemaContract$$'
	$(CARGO) test -p symdesk-contacts --locked --test schema_contracts

.PHONY: template-path-differential
template-path-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/templatepathgen --check
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/templatepath ./scripts/rust-port/cmd/templatepathgen
	$(CARGO) test -p symdesk-core --locked --test template_path_contracts

.PHONY: config-verbatim-fixtures-check config-verbatim-differential
# Native Windows only: retain the recorded AMD64 bytes and compare fresh Go
# observations on either architecture. Foreign hosts must not imply native PASS.
config-verbatim-fixtures-check:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/windows-config-paths-gen -check testdata/port/config/windows-verbatim-paths.json

config-verbatim-differential: config-verbatim-fixtures-check
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/config
	$(CARGO) test -p symdesk-core --all-features --locked
	$(CARGO) test -p symdesk-cli --test ask_offline --locked

# CFG-004 config filesystem writes: the Go-owned fixture records the exact
# MkdirAll/OpenFile side effects — every created ancestor and its mode, an
# existing file's truncation, the created file's mode, the resulting file set
# and the wrapped failure stage — and replays them in Rust. Regenerate the
# fixture deliberately with `make config-save-fixtures-generate`.
config-save-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/config -run TestPortConfigSaveContract

config-save-differential:
	GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/config -run TestPortConfigSaveContract
	$(CARGO) test -p symdesk-core --test config_save_contracts --locked

# CFG-002 config precedence: replay the frozen Go loader results in Rust.
config-precedence-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/config -run '^TestPortConfigPrecedenceContract$$'
	$(CARGO) test -p symdesk-core --test config_precedence --locked

config-vault-selection-differential: port-fixtures-check
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestPortVaultSelectionCLIContract$$'
	$(CARGO) test -p symdesk-cli --test config_vault_selection --locked

.PHONY: config-paths-native config-paths-native-test
config-paths-native-test:
	PYTHONDONTWRITEBYTECODE=1 python3 scripts/rust-port/test_config_paths_native.py

# Build the pinned Go oracle and exact candidate Rust CLI, then retain every
# producer log/case. CONFIG_PATHS_EVIDENCE_DIR must be a new external path.
config-paths-native:
	@test -n "$(CONFIG_PATHS_EVIDENCE_DIR)" || { echo "CONFIG_PATHS_EVIDENCE_DIR must name a fresh external directory" >&2; exit 2; }
	@if [ "$${GITHUB_ACTIONS:-}" != "true" ] && { [ -z "$(CONFIG_PATHS_BUILD_LAUNCHER)" ] || [ -z "$(CONFIG_PATHS_ORACLE_WORKTREE)" ]; }; then echo "local builds require the dev-external launcher and an explicit clean Go oracle worktree" >&2; exit 2; fi
	PYTHONDONTWRITEBYTECODE=1 python3 scripts/rust-port/config_paths_native.py --evidence-dir "$(CONFIG_PATHS_EVIDENCE_DIR)" $(if $(CONFIG_PATHS_BUILD_LAUNCHER),--build-launcher "$(CONFIG_PATHS_BUILD_LAUNCHER)") $(if $(CONFIG_PATHS_ORACLE_WORKTREE),--oracle-worktree "$(CONFIG_PATHS_ORACLE_WORKTREE)")

vault-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/vaultgen \
		--oracle-commit $(PORT_ORACLE_COMMIT) \
		--oracle-release $(PORT_ORACLE_RELEASE)
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/vaultfsgen \
		--oracle-commit $(PORT_ORACLE_COMMIT) \
		--oracle-release $(PORT_ORACLE_RELEASE)
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/typedvaultgen
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/service -run TestVaultResolutionInventory
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/health -run TestHealthLinkResolutionInventory
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/notebook -run TestNotebookParseInventory
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/retrieval/internal/engine -run TestSearchMetadataInventory
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/vault -run TestMobileWriterFixture

vault-fixtures-check:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/vaultgen --check
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/vaultfsgen --check
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/typedvaultgen --check
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/service -run TestVaultResolutionInventory
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/health -run TestHealthLinkResolutionInventory
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/notebook -run TestNotebookParseInventory
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/retrieval/internal/engine -run TestSearchMetadataInventory
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/vault -run TestMobileWriterFixture

vault-read-differential: vault-fixtures-check
	$(CARGO) test -p symdesk-vault --all-features --locked

frontmatter-write-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/vaultwritegen --check
	$(CARGO) test -p symdesk-vault --test frontmatter_write_contracts --locked

# VAULT-004 write stack: the Go-owned filesystem harness for atomic writes,
# create/edit/move/delete, interruption and read-only filesystems replays in
# Rust byte-for-byte (bytes, modes, hashes, file set, trash entry).
# Regenerate the fixtures deliberately with `make vault-write-fixtures-generate`.
vault-write-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/vault -run TestPortVaultWriteFilesystemContract
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/service -run TestPortNoteOperationContract

vault-write-differential:
	GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/vault -run TestPortVaultWriteFilesystemContract
	GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/service -run TestPortNoteOperationContract
	$(CARGO) test -p symdesk-vault --test filesystem_write_contracts --locked
	$(CARGO) test -p symdesk-vault --test note_operations_contracts --locked

notebook-write-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/notebookwritegen --check
	$(CARGO) test -p symdesk-vault --test notebook_write_contracts --locked

base-view-write-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/baseviewwritegen --check
	$(CARGO) test -p symdesk-vault --test base_view_write_contracts --locked

# HTTP-002/HTTP-005 permissions manager: the Go-owned fixture executes the
# production internal/permissions package over users, groups and document
# rules (results, users/groups/permissions.json bytes and modes, Go JSON
# decoding quirks); Rust replays all of it. Regenerate deliberately with
# `make port-fixtures-generate`, never in place.
.PHONY: permissions-differential
permissions-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/permissionsgen --check
	$(CARGO) test -p symdesk-vault --test permissions_contracts --locked

vault-history-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/history -run TestPortHistoryLifecycleContract

# Checkpoints and the trash lifecycle against the pinned Go oracle. The fixture
# is Go-owned and only regenerated through the target above.
vault-history-differential:
	GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/history -run TestPortHistoryLifecycleContract
	$(CARGO) test -p symdesk-vault --test history_lifecycle_contracts --locked

history-tasks-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestPortHistoryTasksCLIContract$$'
	$(CARGO) test -p symdesk-cli --test history_tasks --locked

.PHONY: docs-read-cli-fixtures-generate docs-read-cli-differential
docs-read-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./cmd/symdesk -run '^TestPortDocsReadProcessContract$$'

docs-read-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(INGEST_GO) test -count=1 ./cmd/symdesk -run '^TestPortDocsReadProcessContract$$'
	$(CARGO) test -p symdesk-cli --test docs_read --locked
.PHONY: notebook-write-cli-fixtures-generate notebook-write-cli-differential
notebook-write-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(GO) test -count=1 ./cmd/symdesk -run '^TestPortNotebookWriteProcessContract$$'
notebook-write-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(GO) test -count=1 ./cmd/symdesk -run '^TestPortNotebookWriteProcessContract$$'
	$(CARGO) test -p symdesk-cli --test notebook_write --locked
.PHONY: notebook-read-cli-fixtures-generate notebook-read-cli-differential
notebook-read-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(GO) test -count=1 ./cmd/symdesk -run '^TestPortNotebookReadProcessContract$$'
notebook-read-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(GO) test -count=1 ./cmd/symdesk -run '^TestPortNotebookReadProcessContract$$'
	$(CARGO) test -p symdesk-cli --test notebook_read --locked
.PHONY: views-cli-fixtures-generate views-cli-differential
views-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(GO) test -count=1 ./cmd/symdesk -run '^TestPortViewsProcessContract$$'
views-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(GO) test -count=1 ./cmd/symdesk -run '^TestPortViewsProcessContract$$'
	$(CARGO) test -p symdesk-cli --test views --locked
.PHONY: relations-inverse-cli-fixtures-generate relations-inverse-cli-differential
relations-inverse-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(GO) test -count=1 ./cmd/symdesk -run '^TestPortRelationsInverseProcessContract$$'
relations-inverse-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(GO) test -count=1 ./cmd/symdesk -run '^TestPortRelationsInverseProcessContract$$'
	$(CARGO) test -p symdesk-cli --test relations_inverse --locked
.PHONY: history-read-cli-fixtures-generate history-read-cli-differential
history-read-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(GO) test -count=1 ./cmd/symdesk -run '^TestPortHistoryReadProcessContract$$'
history-read-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(GO) test -count=1 ./cmd/symdesk -run '^TestPortHistoryReadProcessContract$$'
	$(CARGO) test -p symdesk-cli --test history_read --locked

.PHONY: graph-cli-fixtures-generate graph-cli-differential
graph-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(GO) test -count=1 ./cmd/symdesk -run '^TestPortGraphProcessContract$$'
graph-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(GO) test -count=1 ./cmd/symdesk -run '^TestPortGraphProcessContract$$'
	$(CARGO) test -p symdesk-cli --test graph --locked

.PHONY: backlinks-cli-fixtures-generate backlinks-cli-differential
backlinks-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(GO) test -count=1 ./cmd/symdesk -run '^TestPortBacklinksProcessContract$$'
backlinks-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(GO) test -count=1 ./cmd/symdesk -run '^TestPortBacklinksProcessContract$$'
	$(CARGO) test -p symdesk-cli --test backlinks --locked

duplicates-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestPortDuplicatesProcessContract$$'

duplicates-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestPortDuplicatesProcessContract$$'
	$(CARGO) test -p symdesk-cli --test duplicates --locked

# CLI-005/CLI-006 output modes and error taxonomy: the Go binary's stdout,
# stderr and exit code for every command family, recorded in disposable
# HOME/XDG roots with loopback fakes for external providers.
cli-errors-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestPortCLIErrorsProcessContract$$'

cli-errors-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestPortCLIErrorsProcessContract$$'
	$(CARGO) test -p symdesk-cli --test cli_errors --locked

# CLI-008 remote compatibility command allowlist against the Go server over
# loopback (server-forced vault, rejected commands, identical result envelope).
remote-command-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestPortRemoteCommandProcessContract$$'

remote-command-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestPortRemoteCommandProcessContract$$'
	$(CARGO) test -p symdesk-cli --test remote_command --locked

# RUST-013 command bodies (props, tags, history checkpoint/undo-task, trash,
# search export/notebook, views, relations, ...): Go-recorded process
# fixtures with vault-tree snapshots after every step.
.PHONY: cli-bodies-fixtures-generate cli-bodies-differential
cli-bodies-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestPortCLIBodiesProcessContract$$'

cli-bodies-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestPortCLIBodiesProcessContract$$'
	$(CARGO) test -p symdesk-cli --test cli_bodies --locked

# RUST-013 slice r013-admin command bodies (perm, vault health/adopt, relations
# contact, demo init, doctor, dataset list, ai autofill): Go-recorded process
# fixtures with vault-tree and working-directory snapshots after every step.
.PHONY: cli-admin-bodies-fixtures-generate cli-admin-bodies-differential
cli-admin-bodies-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestPortCLIAdminBodiesProcessContract$$'

cli-admin-bodies-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestPortCLIAdminBodiesProcessContract$$'
	$(CARGO) test -p symdesk-cli --test cli_admin_bodies --locked

# VAULT-006 multi-document rules file and document-to-metadata mapping against
# the pinned Go oracle. The fixture is Go-owned and only regenerated through the
# target above.
retention-rules-differential:
	GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/retention -run TestPortRetentionRulesContract
	$(CARGO) test -p symdesk-vault --test retention_rules_contracts --locked

# RUST-007 authoritative Markdown/CSV state and post-mutation rereads.
# Generation is explicit; acceptance checks never rewrite frozen expectations.
retention-state-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/service -run '^TestPortRetentionStateContract$$'

retention-state-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/service -run '^TestPortRetentionStateContract$$' -v
	$(CARGO) test -p symdesk-vault --test retention_state_contracts --locked

vault-retention-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/retention -run TestPortRetentionContract

# Retention rules, evaluation and the proposal/history state files against the
# pinned Go oracle. The fixture is Go-owned and only regenerated through the
# target above.
vault-retention-differential:
	GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/retention -run TestPortRetentionContract
	$(CARGO) test -p symdesk-vault --test retention_contracts --locked

room-journal-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/room/room -run TestPortRoomJournalContract

# SymRoom journal append and read-back (ROOM-002): the Go oracle records the
# per-author chain, the Lamport ceiling and the appended bytes; the Rust replay
# reproduces them. The fixture is only regenerated through the target above.
room-journal-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/room/room -run 'TestPortRoomJournal(Contract|Modes)'
	$(CARGO) test -p symroom-core --locked --test journal_contracts

room-run-projection-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/room/run -run '^TestPortRunProjectionContract$$'

room-run-projection-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/room/run -run '^TestPortRunProjectionContract$$'
	$(CARGO) test -p symroom-core --locked --test run_projection_contracts

room-run-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/room/run -run '^TestPortRunCLIContract$$'

room-run-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/room/run -run '^TestPortRunCLIContract$$'
	$(CARGO) test -p symroom-cli --locked --test run_commands

room-run-wait-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/room/run -run '^TestPortRunWaitCLIContract$$'

room-run-wait-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/room/run -run '^TestPortRunWaitCLIContract$$'
	$(CARGO) test -p symroom-cli --locked --test run_commands run_wait_matches_go_process_contract

room-run-mutations-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/room/run -run '^TestPortRunMutationCLIContract$$'

room-run-mutations-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/room/run -run '^TestPortRunMutationCLIContract$$'
	$(CARGO) test -p symroom-cli --locked --test run_commands run_request_start_cancel_match_go_process_contract

room-note-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symroom -run '^TestPortNoteCLIContract$$'

room-note-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symroom -run '^TestPortNoteCLIContract$$'
	$(CARGO) test -p symroom-cli --locked --test note_commands note_cli_matches_go_process_and_journal_contract

room-decide-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symroom -run '^TestPortDecideCLIContract$$'

room-decide-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symroom -run '^TestPortDecideCLIContract$$'
	$(CARGO) test -p symroom-cli --locked --test decide_commands

room-identity-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symroom -run '^TestPortIdentityCLIContract$$'

room-identity-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symroom -run '^TestPortIdentityCLIContract$$'
	$(CARGO) test -p symroom-cli --locked --test identity_commands identity_cli_matches_go_process_and_key_file_contract

room-member-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symroom -run '^TestPortMemberCLIContract$$'

room-member-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symroom -run '^TestPortMemberCLIContract$$'
	$(CARGO) test -p symroom-cli --locked --test member_commands

room-grammar-process-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symroom -run '^TestPortGrammarProcessContract$$'

room-grammar-process-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symroom -run '^TestPortGrammarProcessContract$$'
	$(CARGO) test -p symroom-cli --locked --test grammar_process

room-mcp-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/room/mcp -run '^TestSymRoomMCP(Representative|Mutation)Oracle$$'
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symroom -run '^TestPortMCPProcessContract$$'

room-mcp-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/room/mcp -run '^Test(SymRoomMCP(Representative|Mutation)Oracle|StdioStreamHygiene)$$'
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 -v ./cmd/symroom -run '^TestPortMCPProcessContract$$'
	$(CARGO) test -p symroom-cli --locked --test mcp

room-merge-read-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/room/journal -run '^TestPortRoomMergeReadContract$$'

room-merge-read-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/room/journal -run '^TestPortRoomMergeReadContract$$'
	$(CARGO) test -p symroom-core --locked --test merge_read_contracts

room-index-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/room/index -run '^TestPortSymRoomIndexOracle$$'

room-index-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/room/index -run '^TestPortSymRoomIndexOracle$$'
	$(CARGO) test -p symroom-core --locked --test index_contracts

room-index-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symroom -run '^TestPortIndexCLIContract$$'

room-index-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symroom -run '^TestPortIndexCLIContract$$'
	$(CARGO) test -p symroom-cli --locked --test index_commands

room-verify-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/room/journal -run '^TestPortRoomVerifyContract$$'

room-verify-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/room/journal -run '^TestPortRoomVerifyContract$$'
	$(CARGO) test -p symroom-core --locked --test verify_contracts

room-verify-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symroom -run '^TestPortVerifyCLIContract$$'

room-verify-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symroom -run '^TestPortVerifyCLIContract$$'
	$(CARGO) test -p symroom-cli --locked --test verify_commands

room-log-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/room/journal -run '^TestPortRoomLogContract$$'

room-log-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/room/journal -run '^TestPortRoomLogContract$$'
	$(CARGO) test -p symroom-core --locked --test log_contracts

room-log-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symroom -run '^TestPortLogCLIContract$$'

room-log-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symroom -run '^TestPortLogCLIContract$$'
	$(CARGO) test -p symroom-cli --locked --test log_commands

room-artifact-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symroom -run '^TestPortArtifactCLIContract$$'

room-artifact-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symroom -run '^TestPortArtifactCLIContract$$'
	$(CARGO) test -p symroom-cli --locked --test artifact_commands

room-artifact-identity-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symroom -run '^TestPortArtifactIdentityCLIContract$$'

room-artifact-identity-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symroom -run '^TestPortArtifactIdentityCLIContract$$'
	$(CARGO) test -p symroom-cli --locked --test artifact_identity_commands

room-watch-stream-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/room/desk -run '^TestPortWatchStreamContract$$'

room-watch-stream-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/room/desk -run '^TestPortWatchStreamContract$$'
	$(CARGO) test -p symroom-core --locked --test watch_stream_contracts

room-brain-profile-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/room/brainprofile -run '^TestPortBrainProfileCLIContract$$'

room-brain-profile-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/room/brainprofile -run '^TestPortBrainProfileCLIContract$$'
	$(CARGO) test -p symroom-cli --locked --test brain_profile_commands

room-init-core-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/room/room -run '^TestPortRoomInitContract$$'

room-init-core-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/room/room -run '^TestPortRoomInitContract$$'
	$(CARGO) test -p symroom-core --locked --test room_init_contracts

room-init-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symroom -run '^TestPortInitCLIContract$$'

room-init-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symroom -run '^TestPortInitCLIContract$$'
	$(CARGO) test -p symroom-cli --locked --test init_commands

room-watch-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symroom -run '^TestPortWatchCLIContract$$'

room-watch-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symroom -run '^TestPortWatchCLIContract$$'
	$(CARGO) test -p symroom-cli --locked --test watch_commands

room-doctor-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symroom -run '^TestPortDoctorCLIContract$$'

room-doctor-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symroom -run '^TestPortDoctorCLIContract$$'
	$(CARGO) test -p symroom-cli --locked --test doctor_commands

room-checkpoint-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symroom -run '^TestPortCheckpointCLIContract$$'

room-checkpoint-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symroom -run '^TestPortCheckpointCLIContract$$'
	$(CARGO) test -p symroom-cli --locked --test checkpoint_commands

retrieval-chunks-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/retrieval/internal/engine -run '^TestRetrievalChunksFixture$$'

retrieval-chunks-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/retrieval/internal/engine -run '^TestRetrievalChunksFixture$$'
	$(CARGO) test -p symdesk-index --locked --test retrieval_chunks

retrieval-bm25-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/retrieval/internal/db -run '^TestRetrievalBM25Fixture$$'

retrieval-bm25-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/retrieval/internal/db -run '^TestRetrievalBM25Fixture$$'
	$(CARGO) test -p symdesk-index --locked --test retrieval_bm25

source-differential:
	@mkdir -p bin/port
	GOTOOLCHAIN=go1.26.10 go build -ldflags="-X main.version=0.12.2" -o "bin/port/symdesk-go$(EXE_SUFFIX)" ./cmd/symdesk
	SYMDESK_VERSION=0.12.2 $(CARGO) build -p symdesk-cli --locked
	python3 scripts/rust-port/source-differential.py "bin/port/symdesk-go$(EXE_SUFFIX)" "$(RUST_TARGET_DIR)/debug/symdesk$(EXE_SUFFIX)"

source-watch-differential:
	@mkdir -p bin/port
	GOTOOLCHAIN=go1.26.10 go build -ldflags="-X main.version=0.12.2" -o "bin/port/symdesk-go$(EXE_SUFFIX)" ./cmd/symdesk
	SYMDESK_VERSION=0.12.2 $(CARGO) build -p symdesk-cli --locked
	python3 scripts/rust-port/source-differential.py --watch "bin/port/symdesk-go$(EXE_SUFFIX)" "$(RUST_TARGET_DIR)/debug/symdesk$(EXE_SUFFIX)"

retrieval-embedding-state-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/retrieval/internal/db -run '^TestRetrievalEmbeddingStateFixture$$'

retrieval-embedding-state-differential:
	GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/retrieval/internal/db -run '^TestRetrievalEmbeddingStateFixture$$'
	$(CARGO) test -p symdesk-index --locked --test retrieval_embedding_state

retrieval-hybrid-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/retrieval/internal/engine -run '^TestRetrievalHybridFixture$$'

retrieval-hybrid-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/retrieval/internal/engine -run '^TestRetrievalHybridFixture$$'
	$(CARGO) test -p symdesk-index --locked --test retrieval_hybrid_contracts

retrieval-pending-rebuild-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/retrieval/internal/engine -run '^TestPendingRebuildPortFixture$$'

retrieval-pending-rebuild-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/retrieval/internal/engine -run '^TestPendingRebuildPortFixture$$'
	$(CARGO) test -p symdesk-index --locked --test retrieval_pending_rebuild --test retrieval_embedding_state

retrieval-sections-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/retrieval/internal/engine -run '^TestRetrievalSectionsFixture$$'

retrieval-sections-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/retrieval/internal/engine -run '^TestRetrievalSectionsFixture$$'
	$(CARGO) test -p symdesk-index --locked --test retrieval_sections --test retrieval_chunks

retrieval-embedding-http-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/retrieval/internal/engine -run '^TestEmbeddingHTTPPortFixture$$'

retrieval-embedding-http-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/retrieval/internal/engine -run '^TestEmbeddingHTTPPortFixture$$'
	$(CARGO) test -p symdesk-protocol --locked --test retrieval_embedding

retrieval-reembed-http-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/retrieval/internal/engine -run '^TestReembedHTTPPortFixture$$'

retrieval-reembed-http-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/retrieval/internal/engine -run '^TestReembedHTTPPortFixture$$'
	$(CARGO) test -p symdesk-cli --locked --test index_reembed_http

retrieval-local-hash-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/retrieval/internal/engine -run '^TestLocalHashPortFixture$$'

retrieval-local-hash-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/retrieval/internal/engine -run '^TestLocalHashPortFixture$$'
	$(CARGO) test -p symdesk-index --locked local_hash_tests

.PHONY: retrieval-document-extraction-fixtures-generate retrieval-document-extraction-differential
RETRIEVAL_EXTRACTION_GO ?= go
retrieval-document-extraction-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 $(RETRIEVAL_EXTRACTION_GO) test -count=1 ./internal/retrieval/internal/parser -run '^TestRetrievalExtractionFixture$$'

retrieval-document-extraction-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(RETRIEVAL_EXTRACTION_GO) test -count=1 ./internal/retrieval/internal/parser -run '^TestRetrievalExtractionFixture$$'
	$(CARGO) test -p symdesk-index --locked retrieval_extract

retrieval-csv-sections-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/retrieval/internal/parser -run '^TestRetrievalCSVSectionsFixture$$'

retrieval-csv-sections-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/retrieval/internal/parser -run '^TestRetrievalCSVSectionsFixture$$'
	$(CARGO) test -p symdesk-index --locked retrieval_csv

retrieval-search-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/service -run '^TestSearchCLIHybridOracle$$'

retrieval-search-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/service -run '^TestSearchCLIHybridOracle$$'
	$(CARGO) test -p symdesk-cli --locked --test search_hybrid

retrieval-search-mcp-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/mcp -run '^TestSearchHybridMCPOracle$$'

retrieval-search-mcp-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/mcp -run '^TestSearchHybridMCPOracle$$'
	$(CARGO) test -p symdesk-cli --locked --test mcp_search_hybrid

retrieval-ask-offline-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/service -run '^TestAskHybridOfflineOracle$$'

retrieval-ask-offline-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/service -run '^TestAskHybridOfflineOracle$$'
	$(CARGO) test -p symdesk-cli --locked --test ask_offline

retrieval-ask-mcp-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/mcp -run '^TestAskOfflineMCPOracle$$'

retrieval-ask-mcp-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/mcp -run '^TestAskOfflineMCPOracle$$'
	$(CARGO) test -p symdesk-cli --locked --test mcp_ask_offline

retrieval-vector-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/retrieval/internal/db -run '^TestRetrievalVectorFixture$$'

retrieval-vector-differential:
	GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/retrieval/internal/db -run '^TestRetrievalVectorFixture$$'
	$(CARGO) test -p symdesk-index --locked --test retrieval_vector

# IVF prefilter, Hamming shortlist and heap ranking of the Go vector layer.
retrieval-ivf-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/retrieval/internal/db -run '^TestRetrievalIVFFixture$$'

retrieval-ivf-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/retrieval/internal/db -run '^TestRetrievalIVFFixture$$'
	$(CARGO) test -p symdesk-index --locked --test retrieval_ivf
	$(CARGO) test -p symdesk-index --locked --lib retrieval_vector

# Pinned CoreKit TurboQuant codec parity (#1137): Go oracle bytes, Rust replay,
# and the Go decoder reading Rust-written sidecars.
retrieval-turboquant-differential:
	GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/turboquant-gen --check
	$(CARGO) test -p symdesk-index --locked --test turboquant_codec
	$(CARGO) test -p symdesk-index --locked --test turboquant_property
	GOTOOLCHAIN=go1.26.10 go test -count=1 -run '^TestGoReadsRustSidecars$$' ./scripts/rust-port/cmd/turboquant-gen

# Go vs Rust p95/throughput; a Rust regression above 10% exits nonzero.
TURBOQUANT_BENCH_REPORT ?= docs/rust-port/turboquant-benchmark.md
retrieval-turboquant-benchmark:
	$(CARGO) build --release --locked -p symdesk-index --bin turboquant-bench
	GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/turboquant-bench --rust-bin "$(RUST_TARGET_DIR)/release/turboquant-bench" --report "$(TURBOQUANT_BENCH_REPORT)"

render-ir-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/renderirgen
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/renderdslgen
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/rendermeasuregen
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/renderscenegen
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/renderlayoutgen

GO ?= go
.PHONY: render-emit-differential
render-emit-differential:
	GOTOOLCHAIN=go1.26.10 $(GO) run ./scripts/rust-port/cmd/renderemitgen --check
	$(CARGO) test -p symdesk-render --test draw_emit_contract --locked

# PNG bytes depend on the recording architecture (see renderpnggen); the Go
# check is skipped off arm64 while the Rust replay always runs.
.PHONY: render-png-differential
render-png-differential:
	GOTOOLCHAIN=go1.26.10 $(GO) run ./scripts/rust-port/cmd/renderpnggen --check
	$(CARGO) test -p symdesk-render --test draw_png_contract --locked

# GIF frames reuse the PNG rasterizer, so the same arm64 recording applies.
.PHONY: render-gif-differential
render-gif-differential:
	GOTOOLCHAIN=go1.26.10 $(GO) run ./scripts/rust-port/cmd/rendergifgen --check
	$(CARGO) test -p symdesk-render --test draw_gif_contract --locked

.PHONY: render-typst-differential
render-typst-differential:
	GOTOOLCHAIN=go1.26.10 $(GO) run ./scripts/rust-port/cmd/rendertypstgen --check
	$(CARGO) test -p symdesk-render --test draw_typst_contract --locked

.PHONY: render-facade-differential
render-facade-differential:
	GOTOOLCHAIN=go1.26.10 $(GO) run ./scripts/rust-port/cmd/renderfacadegen --check
	$(CARGO) test -p symdesk-render --test draw_facade_contract --locked

render-ir-differential: render-emit-differential render-png-differential render-gif-differential render-typst-differential render-facade-differential
	GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/renderirgen --check
	GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/renderdslgen --check
	GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/rendermeasuregen --check
	GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/renderscenegen --check
	GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/renderlayoutgen --check
	$(CARGO) test -p symdesk-render --locked

# Pure noteToHTML parity is paired with the pinned production Go integrity
# gate. Cargo alone compares HTML bytes and digest syntax, not digest values.
render-html-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_RENDER_HTML_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/export -run '^TestPortRenderHTMLFixture$$'

render-html-fixtures-check:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/export -run '^TestPortRenderHTMLFixture$$' -v

render-html-differential: render-html-fixtures-check
	$(CARGO) test -p symdesk-render --locked

# Search-result Markdown, view tables and plain links (RENDER-002 pure core).
render-export-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_RENDER_EXPORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/export -run '^TestPortRenderExportMarkdownFixture$$'

render-export-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/export -run '^TestPortRenderExportMarkdownFixture$$' -v
	$(CARGO) test -p symdesk-render --locked export_markdown

# Vault note/view export: transclusion expansion, embed error lines, path
# confinement and the Markdown/HTML/PDF-source shapes, over real Go-built
# disposable vaults (RENDER-002). Unix-only replay.
render-export-vault-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_RENDER_EXPORT_VAULT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/export -run '^TestPortRenderExportVaultFixture$$'

render-export-vault-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/export -run '^TestPortRenderExportVaultFixture$$' -v
	$(CARGO) test -p symdesk-render --locked export_vault

# PDF frontmatter block emitted before typesetting (RENDER-002 PDF path).
render-pdf-frontmatter-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_RENDER_PDF_FRONTMATTER_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/export -run '^TestPortRenderPDFFrontmatterFixture$$'

render-pdf-frontmatter-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/export -run '^TestPortRenderPDFFrontmatterFixture$$' -v
	$(CARGO) test -p symdesk-render --locked export_pdf

# Pure archive contract (page split, naming, text-layer probe, format dispatch).
# pdfcpu stamping is not covered by this target.
render-archive-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_RENDER_ARCHIVE_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/archive -run '^TestPortArchiveContractFixture$$'

render-archive-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/archive -run '^TestPortArchiveContractFixture$$' -v
	$(CARGO) test -p symdesk-render --locked archive

# Text stamp semantics (page count, boxes, stamp recovery, error kinds). Byte
# layout and geometry are not compared. Regeneration needs Poppler's pdftotext;
# Windows validates the stored oracle without regenerating it.
render-archive-stamp-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_RENDER_ARCHIVE_STAMP_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/archive -run '^TestPortArchiveStampFixture$$'

render-archive-stamp-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/archive -run '^TestPortArchiveStampFixture$$' -v
	$(CARGO) test -p symdesk-render --locked stamp

# pdfcpu image import and archive-stamp geometry/structure: real pdfcpu output
# (Go) against the Rust port through the writer-independent canonical PDF dump
# (scripts/rust-port/pdfops/pdfcanon.py, qpdf). Needs python3 + qpdf.
render-archive-pdf-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_RENDER_ARCHIVE_PDF_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/archive -run '^TestPortArchivePDFFixture$$'

render-archive-pdf-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/archive -run '^TestPortArchivePDFFixture$$' -v
	$(CARGO) test -p symdesk-render --locked --test archive_pdf_contract

# Press (Typst) render pipeline: strict frontmatter contract and meta.json bytes,
# then full renders through the installed typst (typed errors, resolved options,
# PDF/A and PDF/UA gates, reproducible PDF digests). Needs typst, python3, qpdf.
render-press-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_RENDER_PRESS_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/pdf/internal/press -run '^TestPortPressRenderFixture$$'

render-press-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/pdf/internal/press -run '^TestPortPressRenderFixture$$' -v
	$(CARGO) test -p symdesk-render --locked --test press_render_contract

dataset-aggregate-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/service -run '^TestPortDatasetQueryAggregateContract$$'

dataset-aggregate-differential:
	GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/service -run '^TestPortDatasetQueryAggregateContract$$'
	$(CARGO) test -p symdesk-index --locked --test dataset_aggregate_contract

# DATA-002 full dataset query: every operator, ordering, grouping, aggregate,
# paging and dataset-sourced view projection from the real Go sidecar/service,
# plus live Go-vs-Rust CLI processes for the new --group-by/--aggregates flags.
.PHONY: dataset-query-full-fixtures-generate dataset-query-full-differential
dataset-query-full-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/service -run '^TestPortDatasetQueryFullContract$$'

dataset-query-full-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/service -run '^TestPortDatasetQueryFullContract$$'
	$(CARGO) test -p symdesk-index --locked --test dataset_query_full
	@mkdir -p bin/port
	GOTOOLCHAIN=go1.26.10 go build -ldflags="-X main.version=0.12.2" -o "bin/port/symdesk-go$(EXE_SUFFIX)" ./cmd/symdesk
	SYMDESK_VERSION=0.12.2 $(CARGO) build -p symdesk-cli --locked
	GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/diffharness \
		--symdesk-left "bin/port/symdesk-go$(EXE_SUFFIX)" --symdesk-right "$(RUST_TARGET_DIR)/debug/symdesk$(EXE_SUFFIX)" \
		--cases "testdata/port/dataset/cli-query-full.json" --stage dataset-cli

index-backup-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/retrieval -run '^TestIndexBackupPortFixture$$'

index-backup-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/retrieval -run '^TestIndexBackupPortFixture$$'
	$(CARGO) test -p symdesk-index --locked --test index_backup

index-restore-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/retrieval -run '^TestIndexRestorePortFixture$$'

index-restore-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/retrieval -run '^TestIndexRestorePortFixture$$'
	$(CARGO) test -p symdesk-index --locked --test index_restore

index-relocate-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/retrieval -run '^TestIndexRelocatePortFixture$$'

index-relocate-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/retrieval -run '^TestIndexRelocatePortFixture$$'
	$(CARGO) test -p symdesk-index --locked --test index_relocate

index-location-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/retrieval -run '^TestIndexLocationPortFixture$$'

index-location-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/retrieval -run '^TestIndexLocationPortFixture$$'
	$(CARGO) test -p symdesk-index --locked --test index_location

index-maintenance-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestIndexMaintenanceProcessPortFixture$$'

index-maintenance-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestIndexMaintenanceProcessPortFixture$$'
	$(CARGO) test -p symdesk-cli --locked --test index_maintenance

index-build-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestIndexBuildProcessPortFixture$$'

index-build-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestIndexBuildProcessPortFixture$$'
	$(CARGO) test -p symdesk-cli --locked --test index_build

index-retrieval-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestIndexRetrievalProcessPortFixture$$'

index-retrieval-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestIndexRetrievalProcessPortFixture$$'
	$(CARGO) test -p symdesk-cli --locked --test index_retrieval

index-retry-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestIndexRetryProcessPortFixture$$'

index-retry-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestIndexRetryProcessPortFixture$$'
	$(CARGO) test -p symdesk-cli --locked --test index_retry

index-symlink-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestIndexSymlinkProcessPortFixture$$'

index-symlink-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestIndexSymlinkProcessPortFixture$$'
	$(CARGO) test -p symdesk-cli --locked --test index_symlink

sources-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestSourcesProcessPortFixture$$'

sources-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestSourcesProcessPortFixture$$'
	$(CARGO) test -p symdesk-cli --locked --test sources

# RUST-008 acceptance aggregates. `retrieval-differential` runs every retrieval
# differential; `index-crate-test` is `cargo nextest run -p symdesk-index`;
# `index-status-cli-differential` builds Go (oracle commit) and Rust, then
# compares 85 index-status cases (needs a clean committed tree).
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

INDEX_STATUS_OUTPUT ?= $(RUST_TARGET_DIR)/index-status-evidence/$(shell date -u +%Y%m%dT%H%M%SZ)
index-status-cli-differential:
	PYTHONDONTWRITEBYTECODE=1 python3 scripts/rust-port/index-status/verify.py --output "$(INDEX_STATUS_OUTPUT)" $(if $(INDEX_STATUS_BUILD_LAUNCHER),--build-launcher $(INDEX_STATUS_BUILD_LAUNCHER)) $(if $(INDEX_STATUS_ORACLE_WORKTREE),--oracle-worktree $(INDEX_STATUS_ORACLE_WORKTREE))

room-run-approval-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symroom -run '^TestPortRunApprovalCLIContract$$'

room-run-approval-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symroom -run '^TestPortRunApprovalCLIContract$$'
	$(CARGO) test -p symroom-cli --locked --test run_approval_commands

room-io-error-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symroom -run '^TestPortIOErrorCLIContract$$'

room-io-error-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symroom -run '^TestPortIOErrorCLIContract$$'
	$(CARGO) test -p symroom-cli --locked --test io_error_commands

room-flag-grammar-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symroom -run '^TestPortFlagGrammarCLIContract$$'

room-flag-grammar-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symroom -run '^(TestPortFlagGrammarCLIContract|TestWindowsPathSlashesKeepsJSONEscapes)$$'
	$(CARGO) test -p symroom-cli --locked --test flag_grammar_commands

room-identity-provider-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symroom -run '^TestPortIdentityProviderCLIContract$$'

room-identity-provider-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 -v ./cmd/symroom -run '^TestPortIdentityProviderCLIContract$$'
	$(CARGO) test -p symroom-cli --locked --test identity_provider_commands

room-store-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symroom -run '^TestPortStoreCLIContract$$'

room-store-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 -v ./cmd/symroom -run '^TestPortStoreCLIContract$$'
	$(CARGO) test -p symroom-cli --locked --test store_commands

history-prune-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/history -run '^TestPortHistoryPruneContract$$'

history-prune-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/history -run '^TestPortHistoryPruneContract$$'
	$(CARGO) test -p symdesk-vault --locked --test history_prune_contracts

history-service-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/service -run '^TestPortHistoryServiceContract$$'

history-service-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/service -run '^TestPortHistoryServiceContract$$'
	$(CARGO) test -p symdesk-index --locked --test history_service_contract

history-purge-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/history -run '^TestPortHistoryPurgeContract$$'

history-purge-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/history -run '^TestPortHistoryPurgeContract$$'
	$(CARGO) test -p symdesk-vault --locked --test history_purge_contracts

history-trash-purge-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/history -run '^TestPortHistorySelectedTrashPurgeContract$$'

history-trash-purge-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/history -run '^TestPortHistorySelectedTrash(PurgeContract|MixedSelectorSafetyDelta)$$'
	$(CARGO) test -p symdesk-vault --locked --test history_trash_purge_contracts

dataset-purge-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/service -run '^TestPortDatasetPurgeContract$$'

dataset-purge-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/service -run '^TestPortDatasetPurgeContract$$'
	$(CARGO) test -p symdesk-index --locked --test dataset_purge_contract

dataset-sync-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/service -run '^TestPortDataset(SyncContract|SyncServiceContract|ImportContract)$$'

dataset-sync-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/service -run '^TestPortDataset'
	$(CARGO) test -p symdesk-vault --locked --test dataset_contracts
	$(CARGO) test -p symdesk-index --locked --test dataset_service_contracts
	$(CARGO) test -p symdesk-index --locked --test dataset_import_contracts

dataset-cli-differential:
	@mkdir -p bin/port
	GOTOOLCHAIN=go1.26.10 go build -ldflags="-X main.version=0.12.2" -o "bin/port/symdesk-go$(EXE_SUFFIX)" ./cmd/symdesk
	SYMDESK_VERSION=0.12.2 $(CARGO) build -p symdesk-cli --locked
	GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/diffharness \
		--symdesk-left "bin/port/symdesk-go$(EXE_SUFFIX)" --symdesk-right "$(RUST_TARGET_DIR)/debug/symdesk$(EXE_SUFFIX)" \
		--cases "testdata/port/dataset/cli.json" --stage dataset-cli

symroom-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/room/room -run TestPortRoomIdentityEventContract

# Room identity and signed events against the pinned Go oracle (ROOM-001). The
# fixture is Go-owned and only regenerated through the target above.
symroom-differential:
	GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/room/room -run TestPortRoomIdentityEventContract
	$(CARGO) test -p symroom-core --test identity_events_contracts --locked

sidecar-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/sidecar -run TestPortSidecarContract
	$(PORTGEN_GENERATE_GO_ENV) PORTGEN_SIDECAR_ORACLE_COMMIT=$(PORTGEN_SIDECAR_ORACLE_COMMIT) PORTGEN_SIDECAR_ORACLE_RELEASE=$(PORTGEN_SIDECAR_ORACLE_RELEASE) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/sidecar -run TestPortSidecarLifecycleContract

sidecar-metadata-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/sidecar -run TestPortSidecarMetadataContract

# Per-vault sidecar metadata side effects (issue #1006): the Go oracle records
# `metadata.json`'s byte encoding and the durable directory contents, the Rust
# replay reproduces both.
sidecar-metadata-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/sidecar -run 'TestPortSidecarMetadata(Contract|Modes)'
	$(CARGO) test -p symdesk-index --locked --test metadata_contracts

sidecar-fixtures-check:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/sidecar -run 'TestPortSidecar(Contract|LifecycleContract|MetadataContract|MetadataModes)'

sidecar-differential: sidecar-fixtures-check
	SIDECAR_NATIVE=0 GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/sidecar-roundtrip
	$(CARGO) test -p symdesk-index --all-features --locked

# Full local/native gate. Lock and permission semantics are deliberately not
# part of the routine Ubuntu PR lane; native CI invokes this target directly.
sidecar-roundtrip:
	SIDECAR_NATIVE=1 GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/sidecar-roundtrip

port-fixtures-generate:
	@$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/portgen \
		--oracle-commit $(PORT_ORACLE_COMMIT) \
		--oracle-release $(PORT_ORACLE_RELEASE) \
		--fixture-oracle-commit $(PORT_ORACLE_COMMIT) \
		--oracle-anchor "$(PORT_ORACLE_ANCHOR)"

# Apply only a reviewed artifact. The generated commit is built and validated
# in a disposable worktree; the invoking checkout is not modified.
port-fixtures-apply:
	@test -n "$(PORT_FIXTURES_ARTIFACT)" || { echo "set PORT_FIXTURES_ARTIFACT to a reviewed port-fixture patch" >&2; exit 2; }
	@$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/portgen \
		--apply-artifact "$(PORT_FIXTURES_ARTIFACT)"

port-fixtures-check: core-fixtures-check vault-fixtures-check sidecar-fixtures-check
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/portgen --check

differential-go-selftest:
	@mkdir -p bin
	GOTOOLCHAIN=go1.26.10 go build -ldflags="$(LDFLAGS)" -o bin/symdesk ./cmd/symdesk
	GOTOOLCHAIN=go1.26.10 go build -ldflags="$(ROOM_LDFLAGS)" -o bin/symroom ./cmd/symroom
	GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/diffharness \
		--allow-same-binary \
		--symdesk-left "bin/symdesk" --symdesk-right "bin/symdesk" \
		--symroom-left "bin/symroom" --symroom-right "bin/symroom" \
		--cases "$(PORT_CASES)"

# Issue #1153: Go-independent contract aggregate. Replays every committed Go
# oracle fixture of each `*-differential` target through Rust with `go` removed
# from PATH, requires the recorded case counts in
# scripts/rust-port/frozen-contract.json, and checks that mutated fixtures fail.
.PHONY: port-contract-frozen
port-contract-frozen:
	PYTHONDONTWRITEBYTECODE=1 python3 -m unittest scripts/rust-port/test_frozen_contract.py
	PYTHONDONTWRITEBYTECODE=1 python3 scripts/rust-port/frozen_contract.py

port-contract: port-fixtures-check differential-go-selftest sidecar-differential sidecar-metadata-differential room-journal-differential room-run-projection-differential dataset-sync-differential retention-state-differential retrieval-turboquant-differential

representative-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/representativegen

representative-fixtures-check:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/representativegen --check

# VAULT-006 CLI slice: the Go and Rust `symdesk retention` command trees are run
# on the same synthetic vault and compared on stdout, stderr, exit code and the
# written vault files.
retention-cli-differential:
	@mkdir -p bin/port
	GOTOOLCHAIN=go1.26.10 go build -ldflags="-X main.version=0.12.2" -o "bin/port/symdesk-go$(EXE_SUFFIX)" ./cmd/symdesk
	SYMDESK_VERSION=0.12.2 $(CARGO) build -p symdesk-cli --locked
	GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/diffharness \
		--symdesk-left "bin/port/symdesk-go$(EXE_SUFFIX)" --symdesk-right "$(RUST_TARGET_DIR)/debug/symdesk$(EXE_SUFFIX)" \
		--cases "testdata/port/cli/retention-cases.json" --stage retention --show-mismatch-output

retention-cli-regressions-test:
	PYTHONDONTWRITEBYTECODE=1 python3 -m unittest scripts/test_retention_cli_regressions.py

representative-differential: representative-fixtures-check
	@mkdir -p bin/port
	GOTOOLCHAIN=go1.26.10 go build -ldflags="-X main.version=0.12.2" -o bin/port/symdesk-go ./cmd/symdesk
	SYMDESK_VERSION=0.12.2 $(CARGO) build -p symdesk-cli --locked
	GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/diffharness \
		--symdesk-left "bin/port/symdesk-go" --symdesk-right "$(RUST_TARGET_DIR)/debug/symdesk" \
		--cases "testdata/port/representative/cases.json" --stage representative
		$(MAKE) http-differential PORT_LEFT="bin/port/symdesk-go" PORT_RIGHT="$(RUST_TARGET_DIR)/debug/symdesk"

http-differential: representative-fixtures-check
	GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/httpdiff \
		--left "$(PORT_LEFT)" --right "$(PORT_RIGHT)" \
		--fixture "testdata/port/http/representative.json"

# VALUE-001: fail-closed paired representative Go/Rust benchmark.
.PHONY: value-runtime-dirs value-001-evidence-tests value-001-validate
VALUE_SAMPLES ?= 100
VALUE_WARMUPS ?= 20
VALUE_GO_COMMIT ?= 745c08e8144971c61133c5d0e5d61c7ce405aad2
VALUE_OUTPUT ?= docs/rust-port/results/value001-latest.json
VALUE_RETAINED ?= docs/rust-port/results/value001-retained.json
VALUE_RUNTIME_ROOT ?= /Volumes/1TB_NVMe_SN850X/Dev/Symaira_Dev/builds/BuildTargets/symaira-desktop-value001-$(shell git rev-parse --short HEAD)
VALUE_RUSTUP_HOME ?= /Volumes/1TB_NVMe_SN850X/Dev/caches/rustup
VALUE_RUNTIME_ENV = HOME="$(VALUE_RUNTIME_ROOT)/home" USERPROFILE="$(VALUE_RUNTIME_ROOT)/home" TMPDIR="$(VALUE_RUNTIME_ROOT)/tmp" TMP="$(VALUE_RUNTIME_ROOT)/tmp" TEMP="$(VALUE_RUNTIME_ROOT)/tmp" XDG_CONFIG_HOME="$(VALUE_RUNTIME_ROOT)/xdg-config" XDG_DATA_HOME="$(VALUE_RUNTIME_ROOT)/xdg-data" XDG_CACHE_HOME="$(VALUE_RUNTIME_ROOT)/xdg-cache" PYTHONDONTWRITEBYTECODE=1 PYTHONPYCACHEPREFIX="$(VALUE_RUNTIME_ROOT)/pycache" GOCACHE="$(VALUE_RUNTIME_ROOT)/go-cache" GOMODCACHE="$(VALUE_RUNTIME_ROOT)/go-modcache" GOPATH="$(VALUE_RUNTIME_ROOT)/gopath" RUSTUP_HOME="$(VALUE_RUSTUP_HOME)" CARGO_HOME="$(VALUE_RUNTIME_ROOT)/cargo-home" CARGO_TARGET_DIR="$(RUST_TARGET_DIR)" GOTOOLCHAIN=local
# Keep local SEC-003 outputs and the Rust toolchain cache on the attached NVMe.
RESOURCE_STRESS_ROOT ?= /Volumes/1TB_NVMe_SN850X/Dev/Symaira_Dev/builds/BuildTargets/symaira-desktop-sec003
RESOURCE_RUSTUP_HOME ?= /Volumes/1TB_NVMe_SN850X/Dev/caches/rustup
RESOURCE_EXE_SUFFIX := $(if $(filter Windows_NT,$(OS)),.exe,)
RESOURCE_GO_ENV = HOME="$(RESOURCE_STRESS_ROOT)/home" TMPDIR="$(RESOURCE_STRESS_ROOT)/tmp" GOCACHE="$(RESOURCE_STRESS_ROOT)/go-cache" GOMODCACHE="$(RESOURCE_STRESS_ROOT)/go-modcache" GOPATH="$(RESOURCE_STRESS_ROOT)/gopath" GOTOOLCHAIN=local GOENV=off GOWORK=off GOFLAGS=-mod=readonly

value-runtime-dirs:
	@mkdir -p "$(VALUE_RUNTIME_ROOT)/home" "$(VALUE_RUNTIME_ROOT)/tmp" "$(VALUE_RUNTIME_ROOT)/xdg-config" "$(VALUE_RUNTIME_ROOT)/xdg-data" "$(VALUE_RUNTIME_ROOT)/xdg-cache" "$(VALUE_RUNTIME_ROOT)/pycache"

# SEC-003: native black-box resource and cleanup evidence for the representative
# Go/Rust binaries. Every generated root and language cache is explicit so a
# local gate cannot write under the developer's home or MacBook filesystem.
resource-stress:
	@mkdir -p "$(RESOURCE_STRESS_ROOT)/home" "$(RESOURCE_STRESS_ROOT)/tmp"
	@mkdir -p "$(RESOURCE_STRESS_ROOT)/bin"
	$(RESOURCE_GO_ENV) python3 scripts/rust-port/download_go_modules.py "$(RESOURCE_STRESS_ROOT)/bootstrap"
	$(RESOURCE_GO_ENV) GOPROXY=off \
		go build -ldflags="-X main.version=0.12.2" -o "$(RESOURCE_STRESS_ROOT)/bin/symdesk-go$(RESOURCE_EXE_SUFFIX)" ./cmd/symdesk
	HOME="$(RESOURCE_STRESS_ROOT)/home" TMPDIR="$(RESOURCE_STRESS_ROOT)/tmp" RUSTUP_HOME="$(RESOURCE_RUSTUP_HOME)" CARGO_HOME="$(RESOURCE_STRESS_ROOT)/cargo-home" CARGO_TARGET_DIR="$(RESOURCE_STRESS_ROOT)/cargo-target" \
		SYMDESK_VERSION=0.12.2 $(CARGO) build -p symdesk-cli --locked
	HOME="$(RESOURCE_STRESS_ROOT)/home" TMPDIR="$(RESOURCE_STRESS_ROOT)/tmp" RUSTUP_HOME="$(RESOURCE_RUSTUP_HOME)" CARGO_HOME="$(RESOURCE_STRESS_ROOT)/cargo-home" CARGO_TARGET_DIR="$(RESOURCE_STRESS_ROOT)/cargo-target" \
		$(CARGO) test -p symdesk-cli --locked oversized_response_is_rejected_before_writing
	$(RESOURCE_GO_ENV) GOPROXY=off RUSTUP_HOME="$(RESOURCE_RUSTUP_HOME)" CARGO_HOME="$(RESOURCE_STRESS_ROOT)/cargo-home" CARGO_TARGET_DIR="$(RESOURCE_STRESS_ROOT)/cargo-target" \
		go run ./scripts/rust-port/cmd/resource-stress --go "$(RESOURCE_STRESS_ROOT)/bin/symdesk-go$(RESOURCE_EXE_SUFFIX)" --rust "$(RESOURCE_STRESS_ROOT)/cargo-target/debug/symdesk$(RESOURCE_EXE_SUFFIX)" --root "$(RESOURCE_STRESS_ROOT)"

value-001-evidence-tests: value-runtime-dirs
	$(VALUE_RUNTIME_ENV) python3 -m unittest discover -s scripts/rust-port -p 'test_*value001*.py' -v
	$(VALUE_RUNTIME_ENV) python3 scripts/rust-port/validate_value001_retained.py "$(VALUE_RETAINED)"
	$(VALUE_RUNTIME_ENV) python3 scripts/rust-port/value001_report.py "$(VALUE_RETAINED)"
	$(VALUE_RUNTIME_ENV) python3 scripts/rust-port/value001_report.py docs/rust-port/results/value001-latest.json

# Explicit candidate acceptance; historical evidence tests do not approve HEAD.
VALUE_CANDIDATE_ROOT ?= .
value-001-validate: value-runtime-dirs
	@test -n "$(VALUE_CANDIDATE)" -a -n "$(VALUE_TRUSTED_SHA256)"
	$(VALUE_RUNTIME_ENV) python3 scripts/rust-port/validate_value001_candidate.py "$(VALUE_OUTPUT)" --candidate "$(VALUE_CANDIDATE)" --root "$(VALUE_CANDIDATE_ROOT)" --trusted-sha256 "$(VALUE_TRUSTED_SHA256)"


value-001: value-runtime-dirs
	@mkdir -p bin/port "$$(dirname "$(VALUE_OUTPUT)")"
	$(VALUE_RUNTIME_ENV) SYMDESK_VERSION=0.12.2 $(CARGO) build --release -p symdesk-cli --locked
	$(VALUE_RUNTIME_ENV) python3 scripts/rust-port/value001.py \
		--root . \
		--go-source-commit $(VALUE_GO_COMMIT) \
		--rust-binary "$(RUST_TARGET_DIR)/release/symdesk" \
		--rust-build-command "SYMDESK_VERSION=0.12.2 cargo build --release -p symdesk-cli --locked" \
		--samples $(VALUE_SAMPLES) \
		--warmups $(VALUE_WARMUPS) \
		--output "$(VALUE_OUTPUT)"

mcp-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/mcpgen

mcp-fixtures-check:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/mcpgen --check

mcp-differential: mcp-fixtures-check
	@mkdir -p bin/port
	GOTOOLCHAIN=go1.26.10 go build -ldflags="-X main.version=0.12.2" -o bin/port/symdesk-go ./cmd/symdesk
	SYMDESK_VERSION=0.12.2 $(CARGO) build -p symdesk-cli --locked
	GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/mcpdiff \
		--left "bin/port/symdesk-go" --right "$(RUST_TARGET_DIR)/debug/symdesk"

.PHONY: mcp-initialize-differential
mcp-initialize-differential: mcp-fixtures-check
	@mkdir -p bin/port
	GOTOOLCHAIN=go1.26.10 go build -ldflags="-X main.version=0.12.2" -o bin/port/symdesk-go ./cmd/symdesk
	SYMDESK_VERSION=0.12.2 $(CARGO) build -p symdesk-cli --locked
	GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/mcpdiff \
		--left "bin/port/symdesk-go" --right "$(RUST_TARGET_DIR)/debug/symdesk" --case-prefix mcp001-

.PHONY: history-differential
history-differential:
	python3 scripts/rust-port/history_live.py

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
	$(CARGO) llvm-cov nextest --workspace --all-features --locked --summary-only

rust-security:
	$(CARGO) audit
	$(CARGO) deny check

rust-version-contract:
	@mkdir -p bin/port
	GOTOOLCHAIN=go1.26.10 go build -ldflags="-X main.version=0.12.2" -o bin/port/symdesk-go ./cmd/symdesk
	GOTOOLCHAIN=go1.26.10 go build -ldflags="-X github.com/danieljustus/symaira-desktop/internal/room/version.Version=0.12.2" -o bin/port/symroom-go ./cmd/symroom
	SYMDESK_VERSION=0.12.2 SYMROOM_VERSION=0.12.2 $(CARGO) build --release --workspace --locked
	GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/diffharness \
		--symdesk-left "bin/port/symdesk-go" --symdesk-right "$(RUST_TARGET_DIR)/release/symdesk" \
		--symroom-left "bin/port/symroom-go" --symroom-right "$(RUST_TARGET_DIR)/release/symroom" \
		--cases "$(PORT_CASES)" --stage version

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

rust-gates: rust-check rust-lint rust-test rust-features rust-coverage rust-security rust-version-contract

clean:
	go clean -cache -testcache
	rm -rf vendor/

.PHONY: ai-provider-fixtures-generate ai-provider-differential
ai-provider-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(GO) test -count=1 ./internal/ai -run '^TestPortAIProviderContract$$'

ai-provider-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(GO) test -count=1 ./internal/ai -run '^TestPortAIProviderContract$$'
	$(CARGO) test -p symdesk-core --locked --test ai_providers

.PHONY: compose-probe-fixtures-generate compose-probe-differential
compose-probe-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(GO) test -count=1 ./internal/compose -run '^TestPortComposeProbeContract$$'

compose-probe-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(GO) test -count=1 ./internal/compose -run '^TestPortComposeProbeContract$$'
	$(CARGO) test -p symdesk-core --locked --test compose_probe

.PHONY: compose-resolve-fixtures-generate compose-resolve-differential
compose-resolve-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(GO) test -count=1 ./internal/compose -run '^TestPortComposeResolveContract$$'

compose-resolve-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(GO) test -count=1 ./internal/compose -run '^TestPortComposeResolveContract$$'
	$(CARGO) test -p symdesk-core --locked --test compose_resolve

recipe-validate-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestPortRecipeValidateCLIContract$$'

recipe-validate-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestPortRecipeValidateCLIContract$$'
	$(CARGO) test -p symdesk-cli --locked --test recipe_validate

.PHONY: ai-compose-differential
# RUST-012: provider selection/streaming, sibling-tool discovery and the
# reviewed recipe runner, each replayed against Go-recorded fixtures.
ai-compose-differential: ask-hermes-differential secret-canary-differential secrets-resolve-differential ai-provider-differential compose-memory-differential ai-compose-hermes-differential transform-hermes-differential compose-probe-differential compose-resolve-differential recipe-validate-differential recipe-run-differential

.PHONY: ask-hermes-fixtures-generate ask-hermes-differential secret-canary-differential secrets-resolve-fixtures-generate secrets-resolve-differential compose-memory-fixtures-generate compose-memory-differential ai-compose-fixtures-generate ai-compose-hermes-differential transform-hermes-fixtures-generate transform-hermes-differential
ai-compose-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(GO) test -count=1 ./internal/ai -run '^TestPortAIComposeContract$$'

ask-hermes-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestPortAskHermesProcessContract$$'

ask-hermes-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestPortAskHermesProcessContract$$'
	$(CARGO) test -p symdesk-cli --locked --test ask_hermes

# SEC-002: Rust-side canary scan of a failing provider that echoes the key.
secret-canary-differential:
	$(CARGO) test -p symdesk-cli --locked --test secret_canary

secrets-resolve-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/secrets -run '^TestPortSecretsResolveContract$$'

secrets-resolve-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/secrets -run '^TestPortSecretsResolveContract$$'
	$(CARGO) test -p symdesk-service --locked --test secrets_resolve

compose-memory-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/compose -run '^TestPortComposeMemoryContract$$'

compose-memory-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/compose -run '^TestPortComposeMemoryContract$$'
	$(CARGO) test -p symdesk-service --locked --test compose_memory

transform-hermes-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestPortTransformHermesProcessContract$$'

transform-hermes-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestPortTransformHermesProcessContract$$'
	$(CARGO) test -p symdesk-cli --locked --test transform_hermes

.PHONY: ask-provider-mcp-fixtures-generate ask-provider-mcp-differential ask-cancel-fixtures-generate ask-cancel-differential
ask-provider-mcp-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestPortAskProviderMCPContract$$'

ask-provider-mcp-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestPortAskProviderMCPContract$$'
	$(CARGO) test -p symdesk-cli --locked --test mcp_ask_provider

ask-cancel-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestPortAskCancelProcessContract$$'

ask-cancel-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestPortAskCancelProcessContract$$'
	$(CARGO) test -p symdesk-cli --locked --test ask_cancel

# CLI-007: signal and lifecycle contract of events, mcp, serve, worker,
# `sources watch` and the children they spawn (Go-recorded real processes).
.PHONY: cli-signals-fixtures-generate cli-signals-differential
cli-signals-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestPortSignalsProcessContract$$'

cli-signals-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestPortSignalsProcessContract$$'
	$(CARGO) test -p symdesk-cli --locked --test signals

.PHONY: ask-agent-fixtures-generate ask-agent-differential
ask-agent-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestPortAskAgentProcessContract$$'

ask-agent-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestPortAskAgentProcessContract$$'
	$(CARGO) test -p symdesk-cli --locked --test ask_agent

ai-compose-hermes-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(GO) test -count=1 ./internal/ai -run '^TestPortAIComposeContract$$'
	$(CARGO) test -p symdesk-service --locked --test ai_compose

recipe-run-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestPortRecipeRunProcessContract$$'

recipe-run-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 go test -count=1 ./cmd/symdesk -run '^TestPortRecipeRunProcessContract$$'
	$(CARGO) test -p symdesk-cli --locked --test recipe_run

.PHONY: mcp-write-differential mcp-write-fixtures-generate
mcp-write-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(GO) test -count=1 ./cmd/symdesk -run '^TestMCPWriteProcessPortFixture$$'
	$(CARGO) test -p symdesk-cli --test mcp_write --locked

mcp-write-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(GO) test -count=1 ./cmd/symdesk -run '^TestMCPWriteProcessPortFixture$$'

.PHONY: note-doc-cli-fixtures-generate note-doc-cli-differential
note-doc-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(GO) test -count=1 ./cmd/symdesk -run '^TestPortNoteDocProcessContract$$'
note-doc-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(GO) test -count=1 ./cmd/symdesk -run '^TestPortNoteDocProcessContract$$'
	$(CARGO) test -p symdesk-cli --test note_doc --locked

.PHONY: conflict-cli-fixtures-generate conflict-cli-differential
conflict-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(GO) test -count=1 ./cmd/symdesk -run '^TestPortConflictProcessContract$$'
conflict-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(GO) test -count=1 ./cmd/symdesk -run '^TestPortConflictProcessContract$$'
	$(CARGO) test -p symdesk-cli --test conflict --locked

.PHONY: restore-cli-fixtures-generate restore-cli-differential
restore-cli-fixtures-generate:
	$(PORTGEN_GENERATE_GO_ENV) GOTOOLCHAIN=go1.26.10 $(GO) test -count=1 ./cmd/symdesk -run '^TestPortRestoreProcessContract$$'
restore-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.10 $(GO) test -count=1 ./cmd/symdesk -run '^TestPortRestoreProcessContract$$'
	$(CARGO) test -p symdesk-cli --test restore --locked
