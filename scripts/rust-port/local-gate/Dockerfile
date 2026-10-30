# Native Linux (arm64 on Apple Silicon) image for scripts/rust-port/local-gate.sh.
# Mirrors the ubuntu Rust jobs in .github/workflows/ci.yml; keep pins in sync.
# ponytail: same Ubuntu 24.04 as ubuntu-latest, but arm64 instead of x86_64;
# the single CI run of the closing push remains the x86_64 evidence.
FROM ubuntu:24.04

ARG GO_VERSION=1.26.10
ARG NIGHTLY=nightly-2026-09-03

# Git from the git-core PPA: worktrees of this repository use the
# `relativeworktrees` extension, which needs git >= 2.48.
RUN apt-get update \
 && apt-get install -y --no-install-recommends software-properties-common gpg-agent \
 && add-apt-repository -y ppa:git-core/ppa \
 && apt-get install -y --no-install-recommends git perl python3 sudo ca-certificates curl \
      build-essential pkg-config poppler-utils qpdf sqlite3 \
 && rm -rf /var/lib/apt/lists/*

RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-$(dpkg --print-architecture).tar.gz" \
    | tar -C /usr/local -xz

RUN userdel -r ubuntu 2>/dev/null; useradd -m -u 1000 -s /bin/bash dev \
 && echo 'dev ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/dev

USER dev
ENV PATH=/usr/local/go/bin:/home/dev/go/bin:/home/dev/.cargo/bin:$PATH \
    CARGO_HOME=/home/dev/.cargo \
    RUSTUP_HOME=/home/dev/.rustup \
    GOTOOLCHAIN=local

RUN curl -fsSL https://sh.rustup.rs | sh -s -- -y --profile minimal --default-toolchain 1.98.0 \
      --component clippy,rustfmt,llvm-tools-preview \
 && rustup toolchain install "${NIGHTLY}" --profile minimal --component miri,rust-src \
 && curl -fsSL https://raw.githubusercontent.com/cargo-bins/cargo-binstall/main/install-from-binstall-release.sh | bash \
 && cargo binstall --no-confirm --locked \
      cargo-nextest@0.9.143 cargo-deny@0.20.2 cargo-audit@0.22.2 \
      cargo-hack@0.6.45 cargo-llvm-cov@0.9.0 cargo-fuzz@0.13.2 \
 && git config --global safe.directory '*' \
 && git --version \
 && mkdir -p /home/dev/.cargo/registry /home/dev/go /home/dev/target
