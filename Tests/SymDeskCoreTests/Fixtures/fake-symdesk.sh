#!/bin/sh
# Fake `symdesk` binary for LocalDeskTransport contract tests. Dispatches on
# the first argument(s) to simulate the local CLI surface deterministically,
# without spawning a real symdesk process. Not a symdesk reimplementation —
# just enough behavior for the transport contract tests to drive.
set -eu

case "$1" in
  version)
    echo '{"version":"1.0.0","schema_version":1}'
    exit 0
    ;;
  ok)
    echo '{"result":"ok"}'
    exit 0
    ;;
  fail)
    echo "boom" >&2
    exit 1
    ;;
  echo-stdin)
    cat
    exit 0
    ;;
  stream)
    echo '{"type":"answer","text":"first"}'
    echo '{"type":"done"}'
    exit 0
    ;;
  slow-stream)
    echo $$ > "$3"
    echo "first"
    exec sleep 30
    ;;
  ingest)
    case "$2" in
      jobs)
        case "$*" in
          *BADJSON*)
            echo 'not json'
            ;;
          *--limit*)
            echo '{"jobs":[{"id":"1","document_id":1,"kind":"ocr","status":"pending","attempts":0,"created_at":"now","updated_at":"now","source_path":"a.pdf"}],"total":101,"limit":100,"offset":100}'
            ;;
          *)
            echo '[{"id":"1","document_id":1,"kind":"ocr","status":"pending","attempts":0,"created_at":"now","updated_at":"now","source_path":"a.pdf"}]'
            ;;
        esac
        exit 0
        ;;
      retry)
        case "$*" in
          *FAIL*)
            echo "retry failed" >&2
            exit 1
            ;;
          *)
            exit 0
            ;;
        esac
        ;;
      *)
        case "$*" in
          *BADJSON*)
            echo 'not json'
            ;;
          *)
            echo '{"path":"vault/ingested.md"}'
            ;;
        esac
        exit 0
        ;;
    esac
    ;;
  *)
    echo "unknown command: $1" >&2
    exit 2
    ;;
esac
