.PHONY: benchmark-large boundary-guard build clean core-differential core-fixtures-check core-fixtures-generate corekit-guard differential-go-selftest docker-build fmt-check font-guard frontmatter-write-differential http-differential lint mcp-differential mcp-fixtures-check mcp-fixtures-generate nested-version-guard port-contract port-fixtures-check port-fixtures-generate release-signing-guard representative-differential representative-fixtures-check representative-fixtures-generate resource-stress rust-build rust-check rust-coverage rust-features rust-fuzz-smoke rust-gates rust-lint rust-security rust-test rust-version-contract room-journal-differential room-journal-fixtures-generate sidecar-differential sidecar-fixtures-check sidecar-fixtures-generate sidecar-metadata-differential sidecar-metadata-fixtures-generate sidecar-roundtrip symroom-differential symroom-fixtures-generate test value-001 value-001-validate vault-fixtures-check vault-fixtures-generate vault-history-differential vault-history-fixtures-generate vault-read-differential vault-retention-differential vault-retention-fixtures-generate vault-write-differential vault-write-fixtures-generate vuln

.PHONY: retention-state-fixtures-generate retention-state-differential
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
.PHONY: room-artifact-cli-fixtures-generate room-artifact-cli-differential

VERSION ?= $(shell git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//')
LDFLAGS = -X main.version=$(if $(VERSION),$(VERSION),(devel))
ROOM_LDFLAGS = -X github.com/danieljustus/symaira-desktop/internal/room/version.Version=$(if $(VERSION),$(VERSION),(dev))
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
PORT_ORACLE_COMMIT ?= 745c08e8144971c61133c5d0e5d61c7ce405aad2
PORT_ORACLE_RELEASE ?= post-v0.12.2-security-880
# The sidecar lifecycle fixture is P-bound provenance evidence, distinct from
# the historical fixture oracle above. portgen resolves and enforces this same
# pair during generation and immutable checks.
PORTGEN_SIDECAR_ORACLE_COMMIT ?= $(shell git rev-parse HEAD)
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
override PORTGEN_CHECK_ENV += -u PORT_FIXTURE_PATH -u port_fixture_path

build:
	@mkdir -p bin
	go build -ldflags="$(LDFLAGS)" -o bin/symdesk ./cmd/symdesk

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

core-fixtures-generate:
	GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/configgen \
		--oracle-commit $(PORT_ORACLE_COMMIT) \
		--oracle-release $(PORT_ORACLE_RELEASE)
	GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/coregen \
		--oracle-commit $(PORT_ORACLE_COMMIT) \
		--oracle-release $(PORT_ORACLE_RELEASE)
	GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/querygen \
		--oracle-commit $(PORT_ORACLE_COMMIT) \
		--oracle-release $(PORT_ORACLE_RELEASE)

core-fixtures-check:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/configgen --check
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/coregen --check
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/querygen --check

core-differential: core-fixtures-check
	$(CARGO) test -p symdesk-core --all-features --locked

# CFG-004 config filesystem writes: the Go-owned fixture records the exact
# MkdirAll/OpenFile side effects — every created ancestor and its mode, an
# existing file's truncation, the created file's mode, the resulting file set
# and the wrapped failure stage — and replays them in Rust. Regenerate the
# fixture deliberately with `make config-save-fixtures-generate`.
config-save-fixtures-generate:
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/config -run TestPortConfigSaveContract

config-save-differential:
	GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/config -run TestPortConfigSaveContract
	$(CARGO) test -p symdesk-core --test config_save_contracts --locked

vault-fixtures-generate:
	GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/vaultgen \
		--oracle-commit $(PORT_ORACLE_COMMIT) \
		--oracle-release $(PORT_ORACLE_RELEASE)
	GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/vaultfsgen \
		--oracle-commit $(PORT_ORACLE_COMMIT) \
		--oracle-release $(PORT_ORACLE_RELEASE)
	GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/typedvaultgen
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/service -run TestVaultResolutionInventory
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/health -run TestHealthLinkResolutionInventory
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/notebook -run TestNotebookParseInventory
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/retrieval/internal/engine -run TestSearchMetadataInventory
	GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/vault -run TestMobileWriterFixture

vault-fixtures-check:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/vaultgen --check
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/vaultfsgen --check
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/typedvaultgen --check
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/service -run TestVaultResolutionInventory
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/health -run TestHealthLinkResolutionInventory
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/notebook -run TestNotebookParseInventory
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/retrieval/internal/engine -run TestSearchMetadataInventory
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/vault -run TestMobileWriterFixture

vault-read-differential: vault-fixtures-check
	$(CARGO) test -p symdesk-vault --all-features --locked

frontmatter-write-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/vaultwritegen --check
	$(CARGO) test -p symdesk-vault --test frontmatter_write_contracts --locked

# VAULT-004 write stack: the Go-owned filesystem harness for atomic writes,
# create/edit/move/delete, interruption and read-only filesystems replays in
# Rust byte-for-byte (bytes, modes, hashes, file set, trash entry).
# Regenerate the fixtures deliberately with `make vault-write-fixtures-generate`.
vault-write-fixtures-generate:
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/vault -run TestPortVaultWriteFilesystemContract
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/service -run TestPortNoteOperationContract

vault-write-differential:
	GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/vault -run TestPortVaultWriteFilesystemContract
	GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/service -run TestPortNoteOperationContract
	$(CARGO) test -p symdesk-vault --test filesystem_write_contracts --locked
	$(CARGO) test -p symdesk-vault --test note_operations_contracts --locked

vault-history-fixtures-generate:
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/history -run TestPortHistoryLifecycleContract

# Checkpoints and the trash lifecycle against the pinned Go oracle. The fixture
# is Go-owned and only regenerated through the target above.
vault-history-differential:
	GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/history -run TestPortHistoryLifecycleContract
	$(CARGO) test -p symdesk-vault --test history_lifecycle_contracts --locked

# VAULT-006 multi-document rules file and document-to-metadata mapping against
# the pinned Go oracle. The fixture is Go-owned and only regenerated through the
# target above.
retention-rules-differential:
	GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/retention -run TestPortRetentionRulesContract
	$(CARGO) test -p symdesk-vault --test retention_rules_contracts --locked

# RUST-007 authoritative Markdown/CSV state and post-mutation rereads.
# Generation is explicit; acceptance checks never rewrite frozen expectations.
retention-state-fixtures-generate:
	$(PORTGEN_CHECK_ENV) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/service -run '^TestPortRetentionStateContract$$'

retention-state-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/service -run '^TestPortRetentionStateContract$$' -v
	$(CARGO) test -p symdesk-vault --test retention_state_contracts --locked

vault-retention-fixtures-generate:
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/retention -run TestPortRetentionContract

# Retention rules, evaluation and the proposal/history state files against the
# pinned Go oracle. The fixture is Go-owned and only regenerated through the
# target above.
vault-retention-differential:
	GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/retention -run TestPortRetentionContract
	$(CARGO) test -p symdesk-vault --test retention_contracts --locked

room-journal-fixtures-generate:
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/room/room -run TestPortRoomJournalContract

# SymRoom journal append and read-back (ROOM-002): the Go oracle records the
# per-author chain, the Lamport ceiling and the appended bytes; the Rust replay
# reproduces them. The fixture is only regenerated through the target above.
room-journal-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/room/room -run 'TestPortRoomJournal(Contract|Modes)'
	$(CARGO) test -p symroom-core --locked --test journal_contracts

room-run-projection-fixtures-generate:
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/room/run -run '^TestPortRunProjectionContract$$'

room-run-projection-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/room/run -run '^TestPortRunProjectionContract$$'
	$(CARGO) test -p symroom-core --locked --test run_projection_contracts

room-run-cli-fixtures-generate:
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/room/run -run '^TestPortRunCLIContract$$'

room-run-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/room/run -run '^TestPortRunCLIContract$$'
	$(CARGO) test -p symroom-cli --locked --test run_commands

room-run-wait-cli-fixtures-generate:
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/room/run -run '^TestPortRunWaitCLIContract$$'

room-run-wait-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/room/run -run '^TestPortRunWaitCLIContract$$'
	$(CARGO) test -p symroom-cli --locked --test run_commands run_wait_matches_go_process_contract

room-run-mutations-cli-fixtures-generate:
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/room/run -run '^TestPortRunMutationCLIContract$$'

room-run-mutations-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/room/run -run '^TestPortRunMutationCLIContract$$'
	$(CARGO) test -p symroom-cli --locked --test run_commands run_request_start_cancel_match_go_process_contract

room-note-cli-fixtures-generate:
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./cmd/symroom -run '^TestPortNoteCLIContract$$'

room-note-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go test -count=1 ./cmd/symroom -run '^TestPortNoteCLIContract$$'
	$(CARGO) test -p symroom-cli --locked --test note_commands note_cli_matches_go_process_and_journal_contract

room-decide-cli-fixtures-generate:
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./cmd/symroom -run '^TestPortDecideCLIContract$$'

room-decide-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go test -count=1 ./cmd/symroom -run '^TestPortDecideCLIContract$$'
	$(CARGO) test -p symroom-cli --locked --test decide_commands

room-identity-cli-fixtures-generate:
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./cmd/symroom -run '^TestPortIdentityCLIContract$$'

room-identity-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go test -count=1 ./cmd/symroom -run '^TestPortIdentityCLIContract$$'
	$(CARGO) test -p symroom-cli --locked --test identity_commands identity_cli_matches_go_process_and_key_file_contract

room-member-cli-fixtures-generate:
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./cmd/symroom -run '^TestPortMemberCLIContract$$'

room-member-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go test -count=1 ./cmd/symroom -run '^TestPortMemberCLIContract$$'
	$(CARGO) test -p symroom-cli --locked --test member_commands

room-mcp-fixtures-generate:
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/room/mcp -run '^TestSymRoomMCP(Representative|Mutation)Oracle$$'

room-mcp-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/room/mcp -run '^TestSymRoomMCP(Representative|Mutation)Oracle$$'
	$(CARGO) test -p symroom-cli --locked --test mcp

room-merge-read-fixtures-generate:
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/room/journal -run '^TestPortRoomMergeReadContract$$'

room-merge-read-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/room/journal -run '^TestPortRoomMergeReadContract$$'
	$(CARGO) test -p symroom-core --locked --test merge_read_contracts

room-index-fixtures-generate:
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/room/index -run '^TestPortSymRoomIndexOracle$$'

room-index-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/room/index -run '^TestPortSymRoomIndexOracle$$'
	$(CARGO) test -p symroom-core --locked --test index_contracts

room-index-cli-fixtures-generate:
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./cmd/symroom -run '^TestPortIndexCLIContract$$'

room-index-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go test -count=1 ./cmd/symroom -run '^TestPortIndexCLIContract$$'
	$(CARGO) test -p symroom-cli --locked --test index_commands

room-verify-fixtures-generate:
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/room/journal -run '^TestPortRoomVerifyContract$$'

room-verify-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/room/journal -run '^TestPortRoomVerifyContract$$'
	$(CARGO) test -p symroom-core --locked --test verify_contracts

room-verify-cli-fixtures-generate:
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./cmd/symroom -run '^TestPortVerifyCLIContract$$'

room-verify-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go test -count=1 ./cmd/symroom -run '^TestPortVerifyCLIContract$$'
	$(CARGO) test -p symroom-cli --locked --test verify_commands

room-log-fixtures-generate:
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/room/journal -run '^TestPortRoomLogContract$$'

room-log-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/room/journal -run '^TestPortRoomLogContract$$'
	$(CARGO) test -p symroom-core --locked --test log_contracts

room-log-cli-fixtures-generate:
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./cmd/symroom -run '^TestPortLogCLIContract$$'

room-log-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go test -count=1 ./cmd/symroom -run '^TestPortLogCLIContract$$'
	$(CARGO) test -p symroom-cli --locked --test log_commands

room-artifact-cli-fixtures-generate:
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./cmd/symroom -run '^TestPortArtifactCLIContract$$'

room-artifact-cli-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go test -count=1 ./cmd/symroom -run '^TestPortArtifactCLIContract$$'
	$(CARGO) test -p symroom-cli --locked --test artifact_commands

index-backup-fixtures-generate:
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/retrieval -run '^TestIndexBackupPortFixture$$'

index-backup-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/retrieval -run '^TestIndexBackupPortFixture$$'
	$(CARGO) test -p symdesk-index --locked --test index_backup

index-restore-fixtures-generate:
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/retrieval -run '^TestIndexRestorePortFixture$$'

index-restore-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/retrieval -run '^TestIndexRestorePortFixture$$'
	$(CARGO) test -p symdesk-index --locked --test index_restore

index-relocate-fixtures-generate:
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/retrieval -run '^TestIndexRelocatePortFixture$$'

index-relocate-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/retrieval -run '^TestIndexRelocatePortFixture$$'
	$(CARGO) test -p symdesk-index --locked --test index_relocate

history-prune-fixtures-generate:
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/history -run '^TestPortHistoryPruneContract$$'

history-prune-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/history -run '^TestPortHistoryPruneContract$$'
	$(CARGO) test -p symdesk-vault --locked --test history_prune_contracts

history-service-fixtures-generate:
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/service -run '^TestPortHistoryServiceContract$$'

history-service-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/service -run '^TestPortHistoryServiceContract$$'
	$(CARGO) test -p symdesk-index --locked --test history_service_contract

history-purge-fixtures-generate:
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/history -run '^TestPortHistoryPurgeContract$$'

history-purge-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/history -run '^TestPortHistoryPurgeContract$$'
	$(CARGO) test -p symdesk-vault --locked --test history_purge_contracts

history-trash-purge-fixtures-generate:
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/history -run '^TestPortHistorySelectedTrashPurgeContract$$'

history-trash-purge-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/history -run '^TestPortHistorySelectedTrash(PurgeContract|MixedSelectorSafetyDelta)$$'
	$(CARGO) test -p symdesk-vault --locked --test history_trash_purge_contracts

dataset-purge-fixtures-generate:
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/service -run '^TestPortDatasetPurgeContract$$'

dataset-purge-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/service -run '^TestPortDatasetPurgeContract$$'
	$(CARGO) test -p symdesk-index --locked --test dataset_purge_contract

dataset-sync-fixtures-generate:
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/service -run '^TestPortDataset(SyncContract|SyncServiceContract|ImportContract)$$'

dataset-sync-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/service -run '^TestPortDataset'
	$(CARGO) test -p symdesk-vault --locked --test dataset_contracts
	$(CARGO) test -p symdesk-index --locked --test dataset_service_contracts
	$(CARGO) test -p symdesk-index --locked --test dataset_import_contracts

dataset-cli-differential:
	@mkdir -p bin/port
	GOTOOLCHAIN=go1.26.6 go build -ldflags="-X main.version=0.12.2" -o "bin/port/symdesk-go$(EXE_SUFFIX)" ./cmd/symdesk
	SYMDESK_VERSION=0.12.2 $(CARGO) build -p symdesk-cli --locked
	GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/diffharness \
		--symdesk-left "bin/port/symdesk-go$(EXE_SUFFIX)" --symdesk-right "$(RUST_TARGET_DIR)/debug/symdesk$(EXE_SUFFIX)" \
		--cases "testdata/port/dataset/cli.json" --stage dataset-cli

symroom-fixtures-generate:
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/room/room -run TestPortRoomIdentityEventContract

# Room identity and signed events against the pinned Go oracle (ROOM-001). The
# fixture is Go-owned and only regenerated through the target above.
symroom-differential:
	GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/room/room -run TestPortRoomIdentityEventContract
	$(CARGO) test -p symroom-core --test identity_events_contracts --locked

sidecar-fixtures-generate:
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/sidecar -run TestPortSidecarContract
	PORTGEN_SIDECAR_ORACLE_COMMIT=$(PORTGEN_SIDECAR_ORACLE_COMMIT) PORTGEN_SIDECAR_ORACLE_RELEASE=$(PORTGEN_SIDECAR_ORACLE_RELEASE) PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/sidecar -run TestPortSidecarLifecycleContract

sidecar-metadata-fixtures-generate:
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/sidecar -run TestPortSidecarMetadataContract

# Per-vault sidecar metadata side effects (issue #1006): the Go oracle records
# `metadata.json`'s byte encoding and the durable directory contents, the Rust
# replay reproduces both.
sidecar-metadata-differential:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/sidecar -run 'TestPortSidecarMetadata(Contract|Modes)'
	$(CARGO) test -p symdesk-index --locked --test metadata_contracts

sidecar-fixtures-check:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/sidecar -run 'TestPortSidecar(Contract|LifecycleContract|MetadataContract|MetadataModes)'

sidecar-differential: sidecar-fixtures-check
	SIDECAR_NATIVE=0 GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/sidecar-roundtrip
	$(CARGO) test -p symdesk-index --all-features --locked

# Full local/native gate. Lock and permission semantics are deliberately not
# part of the routine Ubuntu PR lane; native CI invokes this target directly.
sidecar-roundtrip:
	SIDECAR_NATIVE=1 GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/sidecar-roundtrip

port-fixtures-generate: core-fixtures-generate vault-fixtures-generate sidecar-fixtures-generate sidecar-metadata-fixtures-generate
	GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/portgen \
		--oracle-release $(PORT_ORACLE_RELEASE)

port-fixtures-check: core-fixtures-check vault-fixtures-check sidecar-fixtures-check
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/portgen --check

differential-go-selftest:
	@mkdir -p bin
	GOTOOLCHAIN=go1.26.6 go build -ldflags="$(LDFLAGS)" -o bin/symdesk ./cmd/symdesk
	GOTOOLCHAIN=go1.26.6 go build -ldflags="$(ROOM_LDFLAGS)" -o bin/symroom ./cmd/symroom
	GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/diffharness \
		--allow-same-binary \
		--symdesk-left "bin/symdesk" --symdesk-right "bin/symdesk" \
		--symroom-left "bin/symroom" --symroom-right "bin/symroom" \
		--cases "$(PORT_CASES)"

port-contract: port-fixtures-check differential-go-selftest sidecar-differential sidecar-metadata-differential room-journal-differential room-run-projection-differential dataset-sync-differential retention-state-differential

representative-fixtures-generate:
	GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/representativegen

representative-fixtures-check:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/representativegen --check

# VAULT-006 CLI slice: the Go and Rust `symdesk retention` command trees are run
# on the same synthetic vault and compared on stdout, stderr, exit code and the
# written vault files.
retention-cli-differential:
	@mkdir -p bin/port
	GOTOOLCHAIN=go1.26.6 go build -ldflags="-X main.version=0.12.2" -o "bin/port/symdesk-go$(EXE_SUFFIX)" ./cmd/symdesk
	SYMDESK_VERSION=0.12.2 $(CARGO) build -p symdesk-cli --locked
	GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/diffharness \
		--symdesk-left "bin/port/symdesk-go$(EXE_SUFFIX)" --symdesk-right "$(RUST_TARGET_DIR)/debug/symdesk$(EXE_SUFFIX)" \
		--cases "testdata/port/cli/retention-cases.json" --stage retention

representative-differential: representative-fixtures-check
	@mkdir -p bin/port
	GOTOOLCHAIN=go1.26.6 go build -ldflags="-X main.version=0.12.2" -o bin/port/symdesk-go ./cmd/symdesk
	SYMDESK_VERSION=0.12.2 $(CARGO) build -p symdesk-cli --locked
	GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/diffharness \
		--symdesk-left "bin/port/symdesk-go" --symdesk-right "$(RUST_TARGET_DIR)/debug/symdesk" \
		--cases "testdata/port/representative/cases.json" --stage representative
		$(MAKE) http-differential PORT_LEFT="bin/port/symdesk-go" PORT_RIGHT="$(RUST_TARGET_DIR)/debug/symdesk"

http-differential: representative-fixtures-check
	GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/httpdiff \
		--left "$(PORT_LEFT)" --right "$(PORT_RIGHT)" \
		--fixture "testdata/port/http/representative.json"

# VALUE-001: fail-closed paired representative Go/Rust benchmark.
.PHONY: value-runtime-dirs value-001-evidence-tests value-001-validate
VALUE_SAMPLES ?= 100
VALUE_WARMUPS ?= 20
VALUE_GO_COMMIT ?= 745c08e8144971c61133c5d0e5d61c7ce405aad2
VALUE_OUTPUT ?= docs/rust-port/results/value001-latest.json
VALUE_RETAINED ?= docs/rust-port/results/value001-retained.json
VALUE_RUNTIME_ROOT ?= /Volumes/1TB_NVMe_SN850X/Dev/Symaira_Dev/BuildTargets/symaira-desktop-value001-$(shell git rev-parse --short HEAD)
VALUE_RUSTUP_HOME ?= /Volumes/1TB_NVMe_SN850X/Dev/caches/rustup
VALUE_RUNTIME_ENV = HOME="$(VALUE_RUNTIME_ROOT)/home" USERPROFILE="$(VALUE_RUNTIME_ROOT)/home" TMPDIR="$(VALUE_RUNTIME_ROOT)/tmp" TMP="$(VALUE_RUNTIME_ROOT)/tmp" TEMP="$(VALUE_RUNTIME_ROOT)/tmp" XDG_CONFIG_HOME="$(VALUE_RUNTIME_ROOT)/xdg-config" XDG_DATA_HOME="$(VALUE_RUNTIME_ROOT)/xdg-data" XDG_CACHE_HOME="$(VALUE_RUNTIME_ROOT)/xdg-cache" PYTHONDONTWRITEBYTECODE=1 PYTHONPYCACHEPREFIX="$(VALUE_RUNTIME_ROOT)/pycache" GOCACHE="$(VALUE_RUNTIME_ROOT)/go-cache" GOMODCACHE="$(VALUE_RUNTIME_ROOT)/go-modcache" GOPATH="$(VALUE_RUNTIME_ROOT)/gopath" RUSTUP_HOME="$(VALUE_RUSTUP_HOME)" CARGO_HOME="$(VALUE_RUNTIME_ROOT)/cargo-home" CARGO_TARGET_DIR="$(RUST_TARGET_DIR)" GOTOOLCHAIN=local
# Keep local SEC-003 outputs and the Rust toolchain cache on the attached NVMe.
RESOURCE_STRESS_ROOT ?= /Volumes/1TB_NVMe_SN850X/Dev/Symaira_Dev/BuildTargets/symaira-desktop-sec003
RESOURCE_RUSTUP_HOME ?= /Volumes/1TB_NVMe_SN850X/Dev/caches/rustup
RESOURCE_EXE_SUFFIX := $(if $(filter Windows_NT,$(OS)),.exe,)

value-runtime-dirs:
	@mkdir -p "$(VALUE_RUNTIME_ROOT)/home" "$(VALUE_RUNTIME_ROOT)/tmp" "$(VALUE_RUNTIME_ROOT)/xdg-config" "$(VALUE_RUNTIME_ROOT)/xdg-data" "$(VALUE_RUNTIME_ROOT)/xdg-cache" "$(VALUE_RUNTIME_ROOT)/pycache"

# SEC-003: native black-box resource and cleanup evidence for the representative
# Go/Rust binaries. Every generated root and language cache is explicit so a
# local gate cannot write under the developer's home or MacBook filesystem.
resource-stress:
	@mkdir -p "$(RESOURCE_STRESS_ROOT)/home" "$(RESOURCE_STRESS_ROOT)/tmp"
	@mkdir -p "$(RESOURCE_STRESS_ROOT)/bin"
	HOME="$(RESOURCE_STRESS_ROOT)/home" TMPDIR="$(RESOURCE_STRESS_ROOT)/tmp" GOCACHE="$(RESOURCE_STRESS_ROOT)/go-cache" GOMODCACHE="$(RESOURCE_STRESS_ROOT)/go-modcache" GOPATH="$(RESOURCE_STRESS_ROOT)/gopath" \
		go build -ldflags="-X main.version=0.12.2" -o "$(RESOURCE_STRESS_ROOT)/bin/symdesk-go$(RESOURCE_EXE_SUFFIX)" ./cmd/symdesk
	HOME="$(RESOURCE_STRESS_ROOT)/home" TMPDIR="$(RESOURCE_STRESS_ROOT)/tmp" RUSTUP_HOME="$(RESOURCE_RUSTUP_HOME)" CARGO_HOME="$(RESOURCE_STRESS_ROOT)/cargo-home" CARGO_TARGET_DIR="$(RESOURCE_STRESS_ROOT)/cargo-target" \
		SYMDESK_VERSION=0.12.2 $(CARGO) build -p symdesk-cli --locked
	HOME="$(RESOURCE_STRESS_ROOT)/home" TMPDIR="$(RESOURCE_STRESS_ROOT)/tmp" RUSTUP_HOME="$(RESOURCE_RUSTUP_HOME)" CARGO_HOME="$(RESOURCE_STRESS_ROOT)/cargo-home" CARGO_TARGET_DIR="$(RESOURCE_STRESS_ROOT)/cargo-target" \
		$(CARGO) test -p symdesk-cli --locked oversized_response_is_rejected_before_writing
	HOME="$(RESOURCE_STRESS_ROOT)/home" TMPDIR="$(RESOURCE_STRESS_ROOT)/tmp" RUSTUP_HOME="$(RESOURCE_RUSTUP_HOME)" GOCACHE="$(RESOURCE_STRESS_ROOT)/go-cache" GOMODCACHE="$(RESOURCE_STRESS_ROOT)/go-modcache" GOPATH="$(RESOURCE_STRESS_ROOT)/gopath" CARGO_HOME="$(RESOURCE_STRESS_ROOT)/cargo-home" CARGO_TARGET_DIR="$(RESOURCE_STRESS_ROOT)/cargo-target" \
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
	GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/mcpgen

mcp-fixtures-check:
	$(PORTGEN_CHECK_ENV) GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/mcpgen --check

mcp-differential: mcp-fixtures-check
	@mkdir -p bin/port
	GOTOOLCHAIN=go1.26.6 go build -ldflags="-X main.version=0.12.2" -o bin/port/symdesk-go ./cmd/symdesk
	SYMDESK_VERSION=0.12.2 $(CARGO) build -p symdesk-cli --locked
	GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/mcpdiff \
		--left "bin/port/symdesk-go" --right "$(RUST_TARGET_DIR)/debug/symdesk"

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
	$(CARGO) nextest run --workspace --all-features --locked
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
	GOTOOLCHAIN=go1.26.6 go build -ldflags="-X main.version=0.12.2" -o bin/port/symdesk-go ./cmd/symdesk
	GOTOOLCHAIN=go1.26.6 go build -ldflags="-X github.com/danieljustus/symaira-desktop/internal/room/version.Version=0.12.2" -o bin/port/symroom-go ./cmd/symroom
	SYMDESK_VERSION=0.12.2 SYMROOM_VERSION=0.12.2 $(CARGO) build --release --workspace --locked
	GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/diffharness \
		--symdesk-left "bin/port/symdesk-go" --symdesk-right "$(RUST_TARGET_DIR)/release/symdesk" \
		--symroom-left "bin/port/symroom-go" --symroom-right "$(RUST_TARGET_DIR)/release/symroom" \
		--cases "$(PORT_CASES)" --stage version

rust-fuzz-smoke:
	$(CARGO) +$(RUST_NIGHTLY) fuzz run frontmatter -- -runs=$(FUZZ_RUNS) -max_len=65536
	$(CARGO) +$(RUST_NIGHTLY) fuzz run room_event -- -runs=$(FUZZ_RUNS) -max_len=65536

rust-gates: rust-check rust-lint rust-test rust-features rust-coverage rust-security rust-version-contract

clean:
	go clean -cache -testcache
	rm -rf vendor/
