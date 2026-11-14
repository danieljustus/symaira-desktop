# DIST-003 public readback and installed-release smoke: v0.13.2

Release: <https://github.com/danieljustus/symaira-desktop/releases/tag/v0.13.2>
(tag `v0.13.2` on `1d70bc5c`, release workflow run 43568149485, all jobs green).
Read back on 2026-11-14 from a fresh download of every release asset; nothing was
built locally. This closes DIST-003. The Rust-only decision of 2026-10-30 replaced
the planned dual-binary prerelease with the stable Rust-only release line
(v0.13.0 CLI, v0.13.1 CLI, v0.13.2 CLI + app); v0.13.2 is the first release whose
complete asset set (CLI archives, macOS app, Homebrew formula and cask, container
image) was published.

## Trust chain

| Check | Command | Result |
|---|---|---|
| Cosign signature and Fulcio certificate of every archive and `checksums.txt` | `cosign verify-blob <asset> --signature <asset>.sig --certificate <asset>.pem --certificate-identity-regexp 'https://github.com/danieljustus/symaira-desktop/.github/workflows/release.yml@refs/tags/v.*' --certificate-oidc-issuer https://token.actions.githubusercontent.com` (cosign v3.1.4) | 4/4 verified: darwin_arm64, linux_amd64, linux_arm64, checksums.txt |
| Archive checksums | `shasum -a 256 -c checksums.txt` | 3/3 OK |
| macOS DMG | `xcrun stapler validate`, `spctl -a -t open --context context:primary-signature` | stapled; accepted, `Notarized Developer ID`, Developer ID Application: Daniel Justus (M4744F3TAA) |
| macOS app (`SymDesk-0.13.2.zip`) | `codesign --verify --deep --strict`, `spctl -a -t exec` | valid; accepted, `Notarized Developer ID`; `CFBundleShortVersionString` 0.13.2 |
| darwin CLI binary | `codesign -dvvv symdesk` | Developer ID Application (M4744F3TAA), hardened runtime, secure timestamp; same authority chain as the v0.12.2 Go binary |
| Homebrew cask `symdesk-app` | tap readback | version 0.13.2, `sha256` = DMG SHA-256 `7d60465340f61bcec5dbeb4ab6c19274057ec40088f1b6d0bda9fb2b5614ddea` |

SBOM and build provenance attestations: not part of the release contract. The Go
oracle release v0.12.2 shipped none either (its assets are the same
archive + `.sig`/`.pem` + `checksums.txt` + DMG/zip set), so the trust chain is
unchanged by the port.

## Installed-release smoke

The same script (`version`, `note new`, `index`, `search`, `backlinks`, `ls`,
`doctor`, stdio MCP `initialize` + `tools/list` with JSON-RPC-only stdout, and a
SymRoom identity/init/note/journal round trip) ran against the published binaries
in an isolated HOME/XDG root:

| Platform | Binary | Result |
|---|---|---|
| macOS arm64 (macOS 27.2, native) | `symaira-desktop_0.13.2_darwin_arm64.tar.gz` | 16/16 PASS |
| Linux arm64 (Debian stable container, native arm64) | `symaira-desktop_0.13.2_linux_arm64.tar.gz` | 16/16 PASS |
| Linux amd64 (Debian stable container, emulated) | `symaira-desktop_0.13.2_linux_amd64.tar.gz` | 16/16 PASS |
| Server image | `ghcr.io/danieljustus/symaira-desktop:0.13.2`, `scripts/release/container-smoke.sh` | PASS (version, auth, restart persistence, Home Assistant options) |

For the four commands whose arguments the first draft of the script got wrong,
the Go v0.12.2 binary was run on the same vault and produced the same output as
Rust (`backlinks` takes the link target, `note new` needs `--title`).
