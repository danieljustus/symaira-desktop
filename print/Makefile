GO ?= go
BINARY_NAME = symprint

# Resolve active developer directory to Xcode if command line tools are active but Xcode is available
export DEVELOPER_DIR := $(shell if [ -d "/Applications/Xcode-beta.app" ]; then echo "/Applications/Xcode-beta.app/Contents/Developer"; elif [ -d "/Applications/Xcode.app" ]; then echo "/Applications/Xcode.app/Contents/Developer"; fi)

# version is a package-level var in `main`, so inject into main.version
# (matches .goreleaser.yml). Injecting into the full import path silently no-ops.
VERSION_PKG = main

.PHONY: all
all: build test

.PHONY: build
build:
	CGO_ENABLED=0 $(GO) build -ldflags "-s -w -X main.version=dev" -o $(BINARY_NAME) ./cmd/symprint

.PHONY: build-version
build-version:
	CGO_ENABLED=0 $(GO) build -ldflags "-s -w -X $(VERSION_PKG).version=$(VERSION)" -o $(BINARY_NAME) ./cmd/symprint

.PHONY: test
test:
	CGO_ENABLED=0 $(GO) test ./...

.PHONY: test-verbose
test-verbose:
	CGO_ENABLED=0 $(GO) test -v ./...

.PHONY: test-race
test-race:
	$(GO) test -race ./...

.PHONY: lint
lint:
	$(GO) fmt ./...
	CGO_ENABLED=0 $(GO) vet ./...

.PHONY: clean
clean:
	rm -f $(BINARY_NAME)
	rm -rf dist/

.PHONY: install
install:
	CGO_ENABLED=0 $(GO) install -ldflags "-s -w -X $(VERSION_PKG).version=dev" ./cmd/symprint

# Regenerate testdata/example-hashes.sha256, the hashed reference outputs
# that CI verifies on every push (render-regression gate). Run this after any
# intentional template, profile or example change and review the diff.
# IMPORTANT: hashes are only stable with the same Typst version CI pins
# (typst-version in .github/workflows/ci.yml). Check `typst --version` first.
.PHONY: hashes
hashes: build
	@tmp=$$(mktemp -d) ; \
	trap 'rm -rf "$$tmp"' EXIT ; \
	for src in examples/*.md ; do \
		name=$$(basename "$$src" .md) ; \
		./$(BINARY_NAME) render "$$src" --reproducible -o "$$tmp/$$name.pdf" >/dev/null || exit 1 ; \
	done ; \
	mkdir -p testdata ; \
	( cd "$$tmp" && \
		if command -v sha256sum >/dev/null 2>&1 ; then sha256sum *.pdf ; \
		else shasum -a 256 *.pdf ; fi ) > testdata/example-hashes.sha256 ; \
	echo "wrote testdata/example-hashes.sha256"

# Render the example documents (requires `typst` on PATH).
.PHONY: examples
examples: build
	./$(BINARY_NAME) render examples/report.md -o dist/report.pdf
	./$(BINARY_NAME) render examples/behoerde.md -o dist/behoerde.pdf
	./$(BINARY_NAME) render examples/meeting.md -o dist/meeting.pdf

# Validate behoerde PDF/A-2a + PDF/UA-1 conformance (requires `verapdf` on PATH
# or Docker). Usage: make verapdf  or  make verapdf DOCKER=1
.PHONY: verapdf
verapdf: examples
	@echo "==> Validating behoerde.pdf against PDF/A-2a…"
ifeq ($(DOCKER),1)
	docker run --rm -v "$(CURDIR):/data" verapdf/cli -f 2a --format text /data/dist/behoerde.pdf | grep -q 'isCompliant="true"'
else
	verapdf -f 2a --format text dist/behoerde.pdf | grep -q 'isCompliant="true"'
endif
	@echo "==> Validating behoerde.pdf against PDF/UA-1…"
ifeq ($(DOCKER),1)
	docker run --rm -v "$(CURDIR):/data" verapdf/cli -f ua1 --format text /data/dist/behoerde.pdf | grep -q 'isCompliant="true"'
else
	verapdf -f ua1 --format text dist/behoerde.pdf | grep -q 'isCompliant="true"'
endif
	@echo "==> veraPDF: all checks passed."

.PHONY: client-gen
client-gen:
	cd client && xcodegen generate --spec project.yml

.PHONY: client-build
client-build: build client-gen
	xcodebuild -project client/Symprint.xcodeproj -scheme Symprint -configuration Release -derivedDataPath client/build

.PHONY: client-dmg
client-dmg: client-build
	./scripts/package-dmg.sh client/build/Build/Products/Release/Symprint.app dist/Symprint.dmg
