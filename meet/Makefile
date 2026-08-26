BINARY := symmeet

# Coverage floor enforced by scripts/coverage-report.sh (see `make coverage`).
# Override per invocation: make coverage COVERAGE_FLOOR=60
COVERAGE_FLOOR ?= 55

# Resolve a full Xcode so `swift test` gets XCTest even while xcode-select
# points at Command Line Tools (see scripts/xcode-developer-dir.sh for the
# resolution order). The result is exported as DEVELOPER_DIR for every recipe
# in this Makefile; the machine-wide xcode-select setting is never touched.
XCODE_DEVELOPER_DIR := $(shell scripts/xcode-developer-dir.sh)
ifneq ($(strip $(XCODE_DEVELOPER_DIR)),)
export DEVELOPER_DIR := $(XCODE_DEVELOPER_DIR)
endif

.PHONY: build test coverage lint clean check-xctest

build:
	swift build

check-xctest:
	@if [ -z "$(XCODE_DEVELOPER_DIR)" ] && xcode-select -p 2>/dev/null | grep -q CommandLineTools; then \
		echo "error: 'swift test' requires XCTest, which Command Line Tools does not include." >&2; \
		echo "       No full Xcode was found. Install Xcode into /Applications," >&2; \
		echo "       or point DEVELOPER_DIR at an existing installation, e.g.:" >&2; \
		echo "         export DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer" >&2; \
		echo "       Alternative: CI is the canonical test gate. The CI workflow runs the tests" >&2; \
		echo "       with coverage on macos-15 and uploads 'coverage-report' (coverage.lcov)" >&2; \
		echo "       as a workflow artifact on every run." >&2; \
		exit 1; \
	fi

test: check-xctest
	swift test

coverage: check-xctest
	swift test --enable-code-coverage
	COVERAGE_FLOOR="$(COVERAGE_FLOOR)" scripts/coverage-report.sh .build/debug/codecov coverage.lcov

lint:
	swift format lint --recursive Sources Tests

clean:
	swift package clean
	rm -rf .build
