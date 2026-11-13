# syntax=docker/dockerfile:1.7
# Rust server image (DIST-004). The build stage runs natively per platform
# (.github/workflows/container.yml builds each architecture on its own runner).
ARG RUST_VERSION=1.98.0
FROM rust:${RUST_VERSION}-bookworm AS build
ARG RUST_VERSION
ARG VERSION=dev
WORKDIR /src
COPY . .
# RUSTUP_TOOLCHAIN overrides rust-toolchain.toml so the image's toolchain is
# used as is instead of downloading the lint components the file lists.
RUN --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/src/target \
    RUSTUP_TOOLCHAIN=${RUST_VERSION} SYMDESK_VERSION=${VERSION} \
      cargo build --release --locked -p symdesk-cli --bin symdesk \
    && mkdir -p /out && cp target/release/symdesk /out/symdesk \
    && /out/symdesk version

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl gosu poppler-utils tesseract-ocr tesseract-ocr-deu tesseract-ocr-eng \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 10001 --create-home --home-dir /home/symdesk symdesk \
    && mkdir -p /data/vault /data/state \
    && chown -R symdesk:symdesk /data
COPY --from=build /out/symdesk /usr/local/bin/symdesk
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint
ENV SYMDESK_VAULT=/data/vault \
    SYMDESK_SERVER_LISTEN=0.0.0.0:8787 \
    XDG_DATA_HOME=/data/state \
    XDG_CONFIG_HOME=/data/state/config
VOLUME ["/data"]
EXPOSE 8787
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD curl --fail --silent http://127.0.0.1:8787/healthz >/dev/null || exit 1
ENTRYPOINT ["docker-entrypoint"]
CMD ["serve"]
