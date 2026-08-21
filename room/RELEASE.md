# Release pipeline

The v0.1 release is a native-binary release only: GoReleaser builds with
`CGO_ENABLED=0` for Darwin, Linux, and Windows on amd64 and arm64 (Windows
arm64 is not emitted until upstream support is required). No Docker image is
built or published in v0.1.

## Validation

Run the same secret-free release build locally with:

```sh
make release-check
make release-dry-run
```

`release-dry-run` uses GoReleaser's snapshot mode and skips signing, SBOM
publication, and GitHub publishing. The `Release` workflow's manual
`dry_run=true` input does the equivalent on GitHub Actions.

## Publishing inputs

A version tag (`vMAJOR.MINOR.PATCH`) runs the guarded publishing path. It uses:

- the workflow `GITHUB_TOKEN` with `contents: write` for the GitHub Release;
- Actions OIDC (`id-token: write`) for keyless cosign signatures; no signing
  secret is required;
- `HOMEBREW_TAP_TOKEN`, available only to the Homebrew workflow's publish step,
  with write access to `danieljustus/homebrew-tap`.

The Homebrew workflow first renders and validates the formula without a secret;
only the release event (or an explicit `dry_run=false` dispatch) attempts to
push the formula.
