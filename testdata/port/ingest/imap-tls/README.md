# IMAP loopback test certificates

Throw-away, test-only material for the loopback fake IMAP server used by the Go
oracle (`internal/ingest/internal/ingest/mail_port_test.go`) and the Rust replay
(`crates/symdesk-ingest/tests/mail_contracts.rs`).

- `ca.pem` is the trust anchor.
- `server.pem` is a leaf for `IP:127.0.0.1` signed by it (valid until 2126).
- `server.key` is the leaf's PKCS#8 key. The CA key was discarded after signing.

The key protects nothing; it is only ever used on the loopback interface.
