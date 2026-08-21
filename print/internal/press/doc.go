// Package press is the engine-agnostic document-production core of symprint.
//
// The pipeline is: raw Markdown (+ YAML frontmatter contract) → a selected
// Profile → a typesetting Engine (Typst) → PDF. The package owns the frontmatter
// contract, the built-in profile registry, contract validation, and the Typst
// shell-out. It deliberately has no dependency on cmd/ or mcp/ so it can be
// reused by the CLI, the MCP server, and tests alike (mirrors internal/fritz in
// symaira-fritz).
//
// Design rules:
//   - Frontmatter carries SEMANTIC metadata only; presentation lives in Profiles.
//   - The engine is reached over PATH (exec.LookPath), never linked → CGO stays
//     off and symprint stays standalone. A missing engine yields a clear,
//     actionable install hint instead of a crash.
//   - Output guarantees (PDF/A, PDF/UA, DIN window) are explicit per profile and
//     never silently downgraded.
package press
