.PHONY: build test lint fmt-check font-guard corekit-guard boundary-guard nested-version-guard benchmark-large docker-build clean

VERSION ?= $(shell git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//')
LDFLAGS = -X main.version=$(if $(VERSION),$(VERSION),(devel))

build:
	@mkdir -p bin
	go build -ldflags="$(LDFLAGS)" -o bin/symdesk ./cmd/symdesk

test:
	CGO_ENABLED=0 go test -race ./...

lint: fmt-check corekit-guard boundary-guard nested-version-guard
	go vet ./...

fmt-check:
	@test -z "$$(gofmt -l .)" || (echo "gofmt diff found:" && gofmt -l . && exit 1)

# Issue #526: corekit dependency pin must stay aligned across all 6 modules.
corekit-guard:
	@./scripts/check-corekit-pins.sh

# Issue #536: packages outside permitted facades must not import absorbed library internals.
boundary-guard:
	@./scripts/check-module-boundaries.sh

# Issue #535: Go version directive and shared dependency versions must stay aligned across root and nested modules.
nested-version-guard:
	@./scripts/check-nested-versions.sh

# Issue #352: macOS app text must use .symairaText(role) instead of inline
# .font(.caption/.headline/...) literals so Dynamic Type scales.
font-guard:
	@HITS="$$(grep -rnE '\.font\(\.(caption|caption2|headline|callout|subheadline|title|title2|title3|body|largeTitle)' Sources/SymDeskApp/ || true)"; \
	if [ -n "$$HITS" ]; then echo "$$HITS"; echo "inline role font literals found — use .symairaText(role) (issue #352)"; exit 1; fi

benchmark-large:
	go test -run '^$$' -bench BenchmarkLargeVaultIndexAndSearch -benchtime=1x ./internal/demo
	go test -run '^$$' -bench BenchmarkGraphLargeVaultWithEntities -benchtime=1x ./internal/service

docker-build:
	docker build -t symaira-desktop:dev .

clean:
	go clean -cache -testcache
	rm -rf vendor/
