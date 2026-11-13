#!/usr/bin/env bash
# Installs the tools the render contracts measure with: Poppler, qpdf and the
# pinned Typst. CI jobs call this; scripts/rust-port/local-gate/Dockerfile
# installs the same set for the local Linux gate (keep the Typst pin in sync).
set -euo pipefail

TYPST_VERSION=0.15.1
TYPST_SHA256_ARM64=5aa8d74a3d906e60ea12a66ac2f37f8eef1b14cbad7182a745e393a10c23dcee
TYPST_SHA256_AMD64=a6d077d0a95eed5a2eba715b2dae06be954f624ccbf85758a03f389ded33118c
TYPST_SHA256_DARWIN_ARM64=48f62ed034aa3a7978309579ac6ca00045e2ef0da73114e8af27cfd8e74dc05a

case "$(uname -s)" in
  Linux)
    sudo apt-get update
    sudo apt-get install -y --no-install-recommends poppler-utils qpdf xz-utils curl ca-certificates
    case "$(uname -m)" in
      aarch64) target=aarch64; sum="$TYPST_SHA256_ARM64" ;;
      x86_64) target=x86_64; sum="$TYPST_SHA256_AMD64" ;;
      *) echo "install-render-tools: unsupported architecture $(uname -m)" >&2; exit 1 ;;
    esac
    archive="$(mktemp)"
    curl -fsSL -o "$archive" \
      "https://github.com/typst/typst/releases/download/v${TYPST_VERSION}/typst-${target}-unknown-linux-musl.tar.xz"
    echo "${sum}  ${archive}" | sha256sum -c -
    # /usr/bin keeps typst on the reduced PATH of the frozen-contract replays.
    sudo tar -C /usr/bin --strip-components=1 -xJf "$archive" "typst-${target}-unknown-linux-musl/typst"
    rm -f "$archive"
    ;;
  Darwin)
    # The self-hosted runner keeps these installed; a hosted runner gets
    # Poppler and qpdf from Homebrew and the pinned Typst release binary.
    for entry in pdfinfo:poppler qpdf:qpdf; do
      command -v "${entry%%:*}" >/dev/null || brew install "${entry##*:}"
    done
    if ! typst --version 2>/dev/null | grep -q "typst ${TYPST_VERSION}"; then
      [ "$(uname -m)" = arm64 ] || { echo "install-render-tools: unsupported architecture $(uname -m)" >&2; exit 1; }
      archive="$(mktemp)"
      curl -fsSL -o "$archive" \
        "https://github.com/typst/typst/releases/download/v${TYPST_VERSION}/typst-aarch64-apple-darwin.tar.xz"
      echo "${TYPST_SHA256_DARWIN_ARM64}  ${archive}" | shasum -a 256 -c -
      sudo mkdir -p /usr/local/bin
      sudo tar -C /usr/local/bin --strip-components=1 -xJf "$archive" typst-aarch64-apple-darwin/typst
      rm -f "$archive"
    fi
    ;;
  *)
    echo "install-render-tools: unsupported OS $(uname -s)" >&2
    exit 1
    ;;
esac

for tool in pdfinfo pdftoppm qpdf typst; do
  command -v "$tool" >/dev/null || { echo "install-render-tools: $tool missing" >&2; exit 1; }
done
typst --version | grep -q "typst ${TYPST_VERSION}" \
  || { echo "install-render-tools: need typst ${TYPST_VERSION}, found $(typst --version)" >&2; exit 1; }
