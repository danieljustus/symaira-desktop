# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- SECURITY.md: replace dead PVR disclosure link with maintainer
  contact (#29)

### Changed

- README: expanded with English product description and
  CI/license/release badges (#30)
- CodeQL workflow: gate analyze on code scanning availability
  (#25), probe fails closed on unexpected errors (#26)

## [v0.1.0] - 2026-08-07

Initial release of Symaira Loom: product and brand hub for the
sovereign, file-based workspace where humans and AI agents
collaborate — signed, local, and with human approval for
consequential actions. The repository is deliberately code-free
(implementation lives in symaira-room).

### Added

- README and product branding assets, including the social
  preview image (#10)
- CI workflow with markdown lint, link check, CodeQL and
  Dependabot config (#17)
- Community files: LICENSE, SECURITY.md, issue forms and
  PR template (#19)
- Changelog tracking release history (#21)

### Changed

- Bump actions/checkout from v4 to v7 (#18)
- Bump github/codeql-action from v3 to v4 (#20)

[Unreleased]: https://github.com/danieljustus/symaira-loom/compare/v0.1.0...HEAD
[v0.1.0]: https://github.com/danieljustus/symaira-loom/releases/tag/v0.1.0
