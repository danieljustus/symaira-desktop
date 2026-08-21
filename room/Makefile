VERSION ?= dev
LDFLAGS := -X github.com/danieljustus/symaira-room/internal/version.Version=$(VERSION)

.PHONY: build test test-race lint fmt-check coverage coverage-check release-check release-dry-run clean

CGO_ENABLED ?= 0

build:
	CGO_ENABLED=$(CGO_ENABLED) go build -ldflags "$(LDFLAGS)" -o bin/symroom ./cmd/symroom

test:
	go test -v ./...

test-race:
	go test -race -v ./...

coverage:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

coverage-check:
	@THRESH=$${COVERAGE_THRESHOLD:-60}; \
	go test -coverprofile=coverage.out ./... >/dev/null; \
	COVERAGE=$$(go tool cover -func=coverage.out | awk '/^total:/ {gsub("%","",$$3); print $$3}'); \
	echo "Coverage: $$COVERAGE% (threshold: $$THRESH%)"; \
	awk "BEGIN {exit !($$COVERAGE >= $$THRESH)}" || { echo "Coverage below threshold" >&2; exit 1; }

lint:
	gofmt -s -w .
	go vet ./...

fmt-check:
	@test -z $$(gofmt -l .) || (echo "Unformatted files found:" && gofmt -l . && exit 1)

release-check:
	@test -f .goreleaser.yaml
	@command -v goreleaser >/dev/null || (echo "goreleaser is required for release-check" >&2 && exit 1)
	goreleaser check

release-dry-run:
	@test -f .goreleaser.yaml
	@command -v goreleaser >/dev/null || (echo "goreleaser is required for release-dry-run" >&2 && exit 1)
	goreleaser release --snapshot --clean --skip=sign,sbom,publish

clean:
	rm -rf bin/
