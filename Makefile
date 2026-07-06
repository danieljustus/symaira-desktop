.PHONY: build test lint fmt-check clean

build:
	@mkdir -p bin
	go build -o bin/symdesk ./cmd/symdesk

test:
	CGO_ENABLED=0 go test -race ./...

lint: fmt-check
	go vet ./...

fmt-check:
	@test -z "$$(gofmt -l .)" || (echo "gofmt diff found:" && gofmt -l . && exit 1)

clean:
	go clean -cache -testcache
	rm -rf vendor/
