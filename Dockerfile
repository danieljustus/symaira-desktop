# syntax=docker/dockerfile:1.7
ARG GO_VERSION=1.26.6
ARG VERSION=dev
FROM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src
# The absorbed tools are nested modules the root go.mod reaches through
# `replace ./print`, `./relate`, `./seek`. Their go.mod/go.sum must be in
# place before `go mod download` can resolve the module graph, so this stage
# copies them alongside the root manifests rather than only with the sources.
COPY go.mod go.sum ./
COPY print/go.mod print/go.sum ./print/
COPY relate/go.mod relate/go.sum ./relate/
COPY seek/go.mod seek/go.sum ./seek/
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/symdesk ./cmd/symdesk

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
