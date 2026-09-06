.PHONY: build test lint fmt-check font-guard corekit-guard boundary-guard nested-version-guard release-signing-guard vuln benchmark-large docker-build clean port-fixtures-generate port-fixtures-check core-fixtures-generate core-fixtures-check core-differential vault-fixtures-generate vault-fixtures-check vault-read-differential sidecar-fixtures-generate sidecar-fixtures-check sidecar-differential differential-go-selftest port-contract rust-build rust-check rust-lint rust-test rust-features rust-coverage rust-security rust-version-contract rust-fuzz-smoke rust-gates

VERSION ?= $(shell git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//')
LDFLAGS = -X main.version=$(if $(VERSION),$(VERSION),(devel))
ROOM_LDFLAGS = -X github.com/danieljustus/symaira-desktop/internal/room/version.Version=$(if $(VERSION),$(VERSION),(dev))
CARGO ?= cargo
PORT_ORACLE_COMMIT ?= ae86331930fdfa2b128b68ae5af7437091b9949a
PORT_ORACLE_RELEASE ?= v0.12.2
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

sidecar-fixtures-generate:
	PORT_GENERATE=1 GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/sidecar -run TestPortSidecarContract

sidecar-fixtures-check:
	GOTOOLCHAIN=go1.26.6 go test -count=1 ./internal/sidecar -run TestPortSidecarContract

sidecar-differential: sidecar-fixtures-check
	$(CARGO) test -p symdesk-index --all-features --locked

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
		--symdesk-left bin/symdesk --symdesk-right bin/symdesk \
		--symroom-left bin/symroom --symroom-right bin/symroom \
		--cases $(PORT_CASES)

port-contract: port-fixtures-check differential-go-selftest

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
		--symdesk-left bin/port/symdesk-go --symdesk-right target/release/symdesk \
		--symroom-left bin/port/symroom-go --symroom-right target/release/symroom \
		--cases $(PORT_CASES) --stage version

rust-fuzz-smoke:
	$(CARGO) +$(RUST_NIGHTLY) fuzz run frontmatter -- -runs=$(FUZZ_RUNS) -max_len=65536

rust-gates: rust-check rust-lint rust-test rust-features rust-coverage rust-security rust-version-contract

clean:
	go clean -cache -testcache
	rm -rf vendor/
