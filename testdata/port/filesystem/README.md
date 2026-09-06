# Filesystem contract fixtures

Filesystem behavior is compared from fresh black-box sandboxes by the Go
harness in `scripts/rust-port/internal/diff`. The complete sandbox is covered,
including HOME, every XDG directory, TMPDIR, and the working tree. Each manifest
records relative path, entry type, permission bits, byte size, SHA-256 for
regular files, and symlink target. Timestamps and owners are deliberately
excluded because the public contract does not fix them.

Deterministic vault-layout and document-pipeline fixtures will be generated here
when VAULT-001 and subsequent store stages start. The RUST-001 harness tests already
prove that content, mode, type, and path drift changes the manifest and fails
comparison. No production vault, token, keychain, or real user state is stored here.

## Isolation boundary

The temporary HOME/XDG tree is deterministic isolation, not an operating-system
sandbox. The harness passes through only the host executable-search variables
(`PATH` and the Windows equivalents), but a case can still launch an installed
program or reach the network if its target command does so. Cases are trusted
repository data. Any slice that can access a keychain, mailbox, AI/OCR service,
remote database, or network must first inject a fake adapter or run under an
explicit platform sandbox; an empty environment variable is not proof that an
OS credential backend was unreachable.
