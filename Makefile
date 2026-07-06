.PHONY: build build-cli build-sidecar test lint fmt-check clean

build: build-cli build-sidecar

build-cli:
	@mkdir -p bin
	go build -o bin/symdesk ./cmd/symdesk

build-sidecar:
	@chmod +x build-sidecar.sh
	./build-sidecar.sh

test:
	CGO_ENABLED=0 go test -race ./...

lint: fmt-check
	go vet ./...

fmt-check:
	@test -z "$$(gofmt -l .)" || (echo "gofmt diff found:" && gofmt -l . && exit 1)

clean:
	go clean -cache -testcache
	rm -rf vendor/
