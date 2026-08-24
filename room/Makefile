VERSION ?= dev
LDFLAGS := -X github.com/danieljustus/symaira-room/internal/version.Version=$(VERSION)

.PHONY: build test test-race lint fmt-check coverage coverage-check clean

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

# The symroom binary is built and published by the repository root
# .goreleaser.yml since the repo consolidation; this module keeps no release
# config of its own. Use the root `goreleaser` targets instead.

clean:
	rm -rf bin/
