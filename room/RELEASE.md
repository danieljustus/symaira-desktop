# Release pipeline

Since the repo consolidation, `symroom` is no longer released from this
module. It is built and published by the repository root
[`.goreleaser.yml`](../.goreleaser.yml) alongside `symdesk`, driven by the
root [`Release` workflow](../.github/workflows/release.yml). This module keeps
no GoReleaser config and no release targets of its own.

`symroom` is the one absorbed tool that still ships an installable binary; the
Homebrew formula `symroom` is deprecated in favour of `symdesk`, but the binary
is still emitted so existing installations keep working.

## Validation

Build and test the module locally with:

```sh
make build
make test
```

Release validation (`goreleaser check`, snapshot dry runs, signing, SBOM,
Homebrew) happens at the repository root — see the root release workflow.
