.PHONY: build test lint fmt-check benchmark-large clean

build:
	@mkdir -p bin
	go build -o bin/symdesk ./cmd/symdesk

test:
	CGO_ENABLED=0 go test -race ./...

lint: fmt-check
	go vet ./...

fmt-check:
	@test -z "$$(gofmt -l .)" || (echo "gofmt diff found:" && gofmt -l . && exit 1)

benchmark-large:
	go test -run '^$$' -bench BenchmarkLargeVaultIndexAndSearch -benchtime=1x ./internal/demo

clean:
	go clean -cache -testcache
	rm -rf vendor/
