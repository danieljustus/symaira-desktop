.PHONY: build test lint fmt-check benchmark-large docker-build clean

VERSION ?= $(shell git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//')
LDFLAGS = -X main.version=$(if $(VERSION),$(VERSION),(devel))

build:
	@mkdir -p bin
	go build -ldflags="$(LDFLAGS)" -o bin/symdesk ./cmd/symdesk

test:
	CGO_ENABLED=0 go test -race ./...

lint: fmt-check
	go vet ./...

fmt-check:
	@test -z "$$(gofmt -l .)" || (echo "gofmt diff found:" && gofmt -l . && exit 1)

benchmark-large:
	go test -run '^$$' -bench BenchmarkLargeVaultIndexAndSearch -benchtime=1x ./internal/demo
	go test -run '^$$' -bench BenchmarkGraphLargeVaultWithEntities -benchtime=1x ./internal/service

docker-build:
	docker build -t symaira-desktop:dev .

clean:
	go clean -cache -testcache
	rm -rf vendor/
