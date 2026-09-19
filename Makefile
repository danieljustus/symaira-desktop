.PHONY: build test lint fmt-check font-guard corekit-guard boundary-guard nested-version-guard release-signing-guard vuln benchmark-large docker-build clean port-fixtures-generate port-fixtures-check core-fixtures-generate core-fixtures-check core-differential vault-fixtures-generate vault-fixtures-check vault-read-differential frontmatter-write-differential sidecar-fixtures-generate sidecar-fixtures-check sidecar-differential sidecar-roundtrip differential-go-selftest port-contract vault-write-differential vault-write-fixtures-generate rust-build rust-check rust-lint rust-test rust-features rust-coverage rust-security rust-version-contract rust-fuzz-smoke rust-gates value-001-validate representative-fixtures-generate representative-fixtures-check representative-differential http-differential mcp-fixtures-generate mcp-fixtures-check mcp-differential resource-stress value-001

VERSION ?= $(shell git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//')
LDFLAGS = -X main.version=$(if $(VERSION),$(VERSION),(devel))
ROOM_LDFLAGS = -X github.com/danieljustus/symaira-desktop/internal/room/version.Version=$(if $(VERSION),$(VERSION),(dev))
CARGO ?= cargo
# Keep differential artifacts in the candidate's isolated Cargo target tree.
RUST_TARGET_DIR ?= $(if $(CARGO_TARGET_DIR),$(CARGO_TARGET_DIR),target)
# Command-line make variables are not inherited by recipes. When callers
# select an isolated Cargo target tree, pass it through to Cargo as well as
# using it for the differential binary path below.
ifneq ($(origin CARGO_TARGET_DIR),undefined)
export CARGO_TARGET_DIR
endif
PORT_ORACLE_COMMIT ?= 745c08e8144971c61133c5d0e5d61c7ce405aad2
PORT_ORACLE_RELEASE ?= post-v0.12.2-security-880
PORT_CASES ?= testdata/port/cli/cases.json
RUST_NIGHTLY ?= nightly-2026-09-03
FUZZ_RUNS ?= 10000

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
	GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/configgen --check
	GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/coregen --check
	GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/querygen --check

core-differential: core-fixtures-check
	$(CARGO) test -p symdesk-core --all-features --locked

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
	GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/vaultgen --check
	GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/vaultfsgen --check
	GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/typedvaultgen --check
	GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/service -run TestVaultResolutionInventory
	GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/health -run TestHealthLinkResolutionInventory
	GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/notebook -run TestNotebookParseInventory
	GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/retrieval/internal/engine -run TestSearchMetadataInventory
	GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/vault -run TestMobileWriterFixture

vault-read-differential: vault-fixtures-check
	$(CARGO) test -p symdesk-vault --all-features --locked

frontmatter-write-differential:
	GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/vaultwritegen --check
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

sidecar-fixtures-generate:
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/sidecar -run TestPortSidecarContract
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/sidecar -run TestPortSidecarLifecycleContract

sidecar-fixtures-check:
	GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/sidecar -run 'TestPortSidecar(Contract|LifecycleContract)'

sidecar-differential: sidecar-fixtures-check
	SIDECAR_NATIVE=0 GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/sidecar-roundtrip
	$(CARGO) test -p symdesk-index --all-features --locked

# Full local/native gate. Lock and permission semantics are deliberately not
# part of the routine Ubuntu PR lane; native CI invokes this target directly.
sidecar-roundtrip:
	SIDECAR_NATIVE=1 GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/sidecar-roundtrip

port-fixtures-generate: core-fixtures-generate vault-fixtures-generate sidecar-fixtures-generate
	GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/portgen \
		--oracle-commit $(PORT_ORACLE_COMMIT) \
		--oracle-release $(PORT_ORACLE_RELEASE)

port-fixtures-check: core-fixtures-check vault-fixtures-check sidecar-fixtures-check
	GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/portgen --check

differential-go-selftest:
	@mkdir -p bin
	GOTOOLCHAIN=go1.26.6 go build -ldflags="$(LDFLAGS)" -o bin/symdesk ./cmd/symdesk
	GOTOOLCHAIN=go1.26.6 go build -ldflags="$(ROOM_LDFLAGS)" -o bin/symroom ./cmd/symroom
	GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/diffharness \
		--allow-same-binary \
		--symdesk-left "bin/symdesk" --symdesk-right "bin/symdesk" \
		--symroom-left "bin/symroom" --symroom-right "bin/symroom" \
		--cases "$(PORT_CASES)"

port-contract: port-fixtures-check differential-go-selftest sidecar-differential

representative-fixtures-generate:
	GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/representativegen

representative-fixtures-check:
	GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/representativegen --check

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
	GOTOOLCHAIN=go1.26.6 go run ./scripts/rust-port/cmd/mcpgen --check

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

rust-gates: rust-check rust-lint rust-test rust-features rust-coverage rust-security rust-version-contract

clean:
	go clean -cache -testcache
	rm -rf vendor/
