BINARY := symmeet

# Coverage floor enforced by scripts/coverage-report.sh (see `make coverage`).
# Override per invocation: make coverage COVERAGE_FLOOR=60
COVERAGE_FLOOR ?= 55

.PHONY: build test coverage lint clean check-xctest

build:
	swift build

check-xctest:
	@if xcode-select -p 2>/dev/null | grep -q CommandLineTools; then \
		echo "error: 'swift test' requires XCTest, which Command Line Tools does not include." >&2; \
		echo "       Install full Xcode, then: sudo xcode-select -s /Applications/Xcode.app/Contents/Developer" >&2; \
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
