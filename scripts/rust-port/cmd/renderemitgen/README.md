# SymDraw SVG output parity

`renderemitgen` calls the production Go `draw.BuildSceneFromIR` and
`emit.EmitSVG`; it does not copy expected outputs from earlier fixtures.
Inputs are deduplicated from the existing DSL and layout corpora, with explicit
validation errors and direct primitive scenes added. `--check` recomputes all
outputs and compares bytes without rewriting the fixture or parser seeds.
The source guard uses the repository's canonical Go oracle identity.

```sh
GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/renderemitgen
GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/renderemitgen --check
make render-emit-differential
```

The fixture has 1,350 cases, including 59 accepted chart inputs spanning all six
chart kinds, empty and ragged series, explicit bounds, colors, titles and legends;
49 cases pin exact lowering errors. All successful SVG
strings compare byte-for-byte, including whitespace, XML escaping, metadata
order, embedded font hashes, linked groups, markers and measured text lengths.
No output normalization is applied. Rounded SVG coordinates, rather than raw
scene-bound JSON floats, are the output contract: existing scene/layout
fixtures retain their own documented FMA/hypotenuse exclusions.

Three input cases are withheld because `axisMin + i*step` has distinct fused
and unfused results, changing visible tick text. `chartFMAAmbiguous` checks both
forms independent of the host. Scatter deliberately retains Go's shadowed
nice-axis behavior (grid uses nice bounds; points use raw bounds).

## Remaining work (not accepted by this gate)

- SVG `TextAsPaths` and glyph outlines: `SvgOptions` deliberately exposes only
  font hash and generator options. No text-as-path parity is claimed.
- Typst serialization/compilation, PDF and raster PNG/GIF emitters are not ported
  here. In particular Go PNG/GIF include pure-Go raster work; their omission is
  remaining implementation, not an assertion that every format requires an
  external tool. Go remains available for these paths.
- No CLI cutover, installed-artifact or cross-platform-native acceptance is
  claimed by this library gate.

## Parser fuzzing

`fuzz/fuzz_targets/draw_parser.rs` exercises the JSON decoder, DSL dispatcher
and successful diagram serialization. Seeds under `fuzz/seeds/draw_parser/`
are validated by the production Go parser and byte-checked by this generator.
The `seeds` location respects the repository's ignored mutable `fuzz/corpus/`.
Copy seeds to an ephemeral corpus before running libFuzzer; never fuzz the
tracked seed directory in place. Use the repository's compiler launcher when
required by the execution environment.

This implementation host has stable and pinned Rust 1.98.0 only; no nightly
is installed. `cargo check --manifest-path fuzz/Cargo.toml --bin draw_parser
--locked` passes; a sanitizer-instrumented build and bounded libFuzzer smoke
remain pending on a nightly-equipped host. The separate fuzz lockfile is
updated for the new local dependency.
