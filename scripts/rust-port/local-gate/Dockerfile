# Native Linux (arm64 on Apple Silicon) image for scripts/rust-port/local-gate.sh.
# Mirrors the ubuntu Rust jobs in .github/workflows/ci.yml; keep pins in sync.
# No Go toolchain (RUST-019): the Go tree is retired.
# ponytail: same Ubuntu 24.04 as ubuntu-latest, but arm64 instead of x86_64;
# the single CI run of the closing push remains the x86_64 evidence.
FROM ubuntu:24.04

ARG NIGHTLY=nightly-2026-09-03
# Pinned to the typst the macOS host runs (the press render oracle records it).
ARG TYPST_VERSION=0.15.1
ARG TYPST_SHA256_ARM64=5aa8d74a3d906e60ea12a66ac2f37f8eef1b14cbad7182a745e393a10c23dcee
ARG TYPST_SHA256_AMD64=a6d077d0a95eed5a2eba715b2dae06be954f624ccbf85758a03f389ded33118c

# Git from the git-core PPA: worktrees of this repository use the
# `relativeworktrees` extension, which needs git >= 2.48.
RUN apt-get update \
 && apt-get install -y --no-install-recommends software-properties-common gpg-agent \
 && add-apt-repository -y ppa:git-core/ppa \
 && apt-get install -y --no-install-recommends git perl python3 sudo ca-certificates curl \
      build-essential pkg-config poppler-utils qpdf sqlite3 \
 && rm -rf /var/lib/apt/lists/*

RUN userdel -r ubuntu 2>/dev/null; useradd -m -u 1000 -s /bin/bash dev \
 && echo 'dev ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/dev

USER dev
ENV PATH=/home/dev/.cargo/bin:$PATH \
    CARGO_HOME=/home/dev/.cargo \
    RUSTUP_HOME=/home/dev/.rustup

RUN curl -fsSL https://sh.rustup.rs | sh -s -- -y --profile minimal --default-toolchain 1.98.0 \
      --component clippy,rustfmt,llvm-tools-preview \
 && rustup toolchain install "${NIGHTLY}" --profile minimal --component miri,rust-src \
 && curl -fsSL https://raw.githubusercontent.com/cargo-bins/cargo-binstall/main/install-from-binstall-release.sh | bash \
 && cargo binstall --no-confirm --locked \
      cargo-nextest@0.9.143 cargo-deny@0.20.2 cargo-audit@0.22.2 \
      cargo-hack@0.6.45 cargo-llvm-cov@0.9.0 cargo-fuzz@0.13.2 \
 && git config --global safe.directory '*' \
 && git --version \
 && mkdir -p /home/dev/.cargo/registry /home/dev/target

# Typst for the press render differential (RENDER-002). Last layer, so changing
# the pin does not rebuild the toolchain layers above. It goes to /usr/bin so
# it stays on the reduced PATH of the frozen-contract replays.
RUN sudo apt-get update && sudo apt-get install -y --no-install-recommends xz-utils \
 && sudo rm -rf /var/lib/apt/lists/* \
 && arch="$(dpkg --print-architecture)" \
 && case "$arch" in arm64) target=aarch64; sum="$TYPST_SHA256_ARM64";; amd64) target=x86_64; sum="$TYPST_SHA256_AMD64";; *) echo "unsupported arch $arch" >&2; exit 1;; esac \
 && curl -fsSL -o /tmp/typst.tar.xz "https://github.com/typst/typst/releases/download/v${TYPST_VERSION}/typst-${target}-unknown-linux-musl.tar.xz" \
 && echo "${sum}  /tmp/typst.tar.xz" | sha256sum -c - \
 && sudo tar -C /usr/bin --strip-components=1 -xJf /tmp/typst.tar.xz "typst-${target}-unknown-linux-musl/typst" \
 && rm /tmp/typst.tar.xz \
 && typst --version | grep -q "typst ${TYPST_VERSION}"

# musl C toolchain for `make release-contract`: Linux release binaries are
# static musl builds and ring/libsqlite3-sys compile C for that target
# (release.yml installs the same package). Separate last layer on purpose.
RUN sudo apt-get update && sudo apt-get install -y --no-install-recommends musl-tools \
 && sudo rm -rf /var/lib/apt/lists/* \
 && command -v "$(uname -m)-linux-musl-gcc"
