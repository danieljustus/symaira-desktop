# symaira-print — Architecture & Roadmap

> Status: **early scaffold, verified end-to-end** against Typst 0.15.0 (2026-06-30).
> Binary `symprint`. Go 1.26.4, CGO-free, Apache-2.0. Part of the Symaira ecosystem.

## 1. Problem & goal

Producing a good-looking PDF from Markdown today means fighting **pandoc + LaTeX**:
multi-GB TeX installs, cryptic cascading errors you never wrote, and many
iterations before the page looks right. That loop is hostile to humans and
near-impossible for AI agents, which can't read a 200-line TeX log and recover.

**Goal:** a tool where the document author writes *semantic* Markdown plus a tiny
frontmatter contract, picks a **named use-case profile** (Behörde, Brief, Report,
Rechnung…), and gets a consistent, beautiful PDF — deterministically, with clear
errors, and identically whether driven by a human, a CLI, or an MCP agent.

Design pillars:

1. **Profiles, not knobs.** The profile owns every visual decision (colours,
   heading sizes, cover page, page numbers, DIN geometry, PDF/A+UA). Documents
   stay clean and portable across profiles.
2. **One small engine, no TeX Live.** A single static engine binary, reached over
   `PATH`, never linked — so `symprint` stays one CGO-free Go binary.
3. **Strict, discoverable contract.** Unknown frontmatter keys fail loudly;
   what you write is what you get.
4. **Compliance built in.** German-authority output (PDF/A + PDF/UA, DIN 5008)
   is a first-class profile, not an afterthought.
5. **Agent-native.** Errors are typed and machine-readable; the same pipeline
   backs the CLI and the MCP server.

## 2. Landscape research (what exists, what we borrow)

We surveyed the Markdown→PDF space across five axes (Typst, CSS/HTML engines,
existing tools, German standards, Go integration). Summary of the decision:

### 2.1 Engine decision matrix

| Engine | Verdict | Why |
|---|---|---|
| **Typst** (shell-out, pinned) | **PRIMARY** | Single Apache-2.0 static binary (14–22 MB), exec'd → build stays CGO-free; in homebrew-core; fast compiles + parseable errors; reproducible output (`SOURCE_DATE_EPOCH`); **native PDF/A + PDF/UA-1 in one flag** (`--pdf-standard a-2a,ua-1`), Tagged PDF by default; ready DIN 5008 templates; clean `set`/`show` styling; `cmarker` gives Markdown with zero extra deps. |
| Pandoc + LaTeX (Eisvogel / KOMA `scrlttr2`) | REJECT as primary | Multi-GB TeX Live; cryptic errors; cross-version reproducibility breaks; brittle accessibility toolchain. Re-imports every frustration the tool exists to remove. Keep `DIN5008A.lco` + Eisvogel only as measurement/naming references; optional `pandoc -t typst` path for power users (still routes through Typst). |
| WeasyPrint (HTML/CSS) | SECONDARY (future) | Native PDF/A + PDF/UA; good preview loop; but Python + cairo/pango deps, harder to bundle than one static binary; weaker paged-media fidelity for DIN geometry. Reserve for a future CSS/web profile. |
| Chromium / Puppeteer | REJECT for output | Incomplete CSS Paged Media (weak running headers, page breaks, TOC, DIN mm geometry); heavy headless browser. Borrow only the layered-CSS UX ideas; optionally an approximate preview. |
| Pure-Go (maroto v2 / gofpdf) | FALLBACK only | Zero external dependency, deterministic; but no DIN window geometry, no PDF/A/UA, and the canonical `fpdf` repos are archived/scattered. Last resort when no engine exists, with an explicit "reduced guarantees" warning. |
| UniPDF | REJECT outright | Proprietary commercial EULA — incompatible with an Apache-2.0 redistributable. Use `pdfcpu` (Apache-2.0) for any PDF manipulation. |

### 2.2 Patterns we adopt from existing tools

- **Named profiles** = Pandoc `--defaults` files, conceptually: one named bundle
  (template + engine options + branding) selected by a single key.
- **Strict, semantic frontmatter** with a documented precedence chain
  (md-to-pdf): `defaults < profile < frontmatter < CLI/MCP`.
- **Flat, prefixed override namespace** (Eisvogel: `header-*`, `footer-*`,
  `titlepage`) for the few visual overrides we expose in documents.
- **Template inheritance** (asciidoctor-pdf `extends`) — `behoerde` extends
  `brief` rather than copying it.
- **Schema-as-source-of-truth** (Quarto) — the frontmatter struct drives both
  validation and the docs.

### 2.3 Things to reuse (borrow list)

| Item | Use | License |
|---|---|---|
| `typst` CLI | primary engine (shell-out) | Apache-2.0 |
| `@preview/cmarker` | Markdown→Typst in-engine (no pandoc) | MIT |
| `@preview/mitex` | LaTeX math inside cmarker's math callback (P3) | Apache-2.0 |
| `Sematre/typst-letter-pro`, `pascal-huber/typst-letter-template` | DIN 5008 geometry reference for `brief`/`behoerde` | MIT |
| `classy-german-invoice` / `invoice-pro` | seed `rechnung` (VAT, GiroCode) | MIT-0 / repo |
| `pdfcpu` | pure-Go post-processing: merge, page numbers, validate (P3) | Apache-2.0 |
| `veraPDF` | CI gate for PDF/A + PDF/UA conformance (P1) | MPLv2/GPLv3 |
| KOMA `DIN5008A.lco` | authoritative mm cross-reference | LPPL |
| `symaira-corekit` | configkit / exitcodes / logkit / mcpserver | Apache-2.0 |

## 3. Architecture

```
                        symprint (single CGO-free Go binary)
 ┌───────────────────────────────────────────────────────────────────────────┐
 │  cmd/symprint           render · profiles · validate · doctor · config · mcp│
 │      │                                                                      │
 │      ▼                                                                      │
 │  internal/press  ── the engine-agnostic core ──────────────────────────────│
 │   frontmatter.go   Split + strict Parse (KnownFields) + Validate            │
 │   profile.go       registry: brief / behoerde / report / rechnung           │
 │   engine.go        DetectTypst (exec.LookPath) → install hint if missing    │
 │   render.go        precedence resolution (override>frontmatter>default)     │
 │   typst.go         build work dir, run `typst compile`, map errors          │
 │      │                                                                      │
 │      ├── internal/assets   go:embed templates/*.typ → materialize per run   │
 │      └── internal/config   configkit (SYMPRINT_* env, TOML, XDG)            │
 │                                                                             │
 │  internal/mcp     corekit mcpserver (stdio): render_pdf, list_profiles, …   │
 └───────────────────────────────────────────────────────────────────────────┘
              │ exec (PATH), never linked
              ▼
        typst 0.15.0  ──  cmarker (MD→Typst)  ──  --pdf-standard a-2a,ua-1  ──▶ PDF
```

### 3.1 Render pipeline (per request)

1. **Parse** — `Split` peels the YAML frontmatter; `Parse` decodes it with
   `KnownFields(true)` so unknown keys are a `*ContractError`.
2. **Select profile** — precedence: `--profile` / MCP arg > frontmatter
   `profile:` > config default.
3. **Validate** — required fields, DIN date format, DIN form, and (for PDF/UA
   profiles) mandatory `lang` + `title`. Errors abort before the engine runs.
4. **Resolve output** — PDF standard and reproducibility resolved by the same
   precedence chain.
5. **Materialize** — a fresh `os.MkdirTemp` work dir gets the embedded templates,
   `meta.json` (frontmatter as JSON), `body.md`, and a generated `main.typ`.
6. **Compile** — `typst compile --root <work> [--pdf-standard …] main.typ out.pdf`
   with stderr captured separately; `SOURCE_DATE_EPOCH=0` when reproducible.
7. **Map result/error** — success → `Result`; failure → `*RenderError{Stage,Hint}`
   with a cleaned, truncated engine diagnostic (never a raw dump).

### 3.2 The generated `main.typ`

```typst
#import "@preview/cmarker:0.1.9"
#import "/templates/<profile>.typ" as profile
#let meta = json("/meta.json")
#show: profile.apply.with(meta)
#cmarker.render(read("/body.md"))
```

Each profile template exposes `apply(meta, doc)`: it consumes the frontmatter
(`meta`) and applies `set`/`show` rules to the cmarker-rendered body (`doc`).
This cleanly separates **data** (meta.json), **content** (body.md), and
**presentation** (the template).

### 3.3 Standalone-first behaviour

- The engine is resolved with `exec.LookPath`. Missing → `*RenderError{Stage:"engine"}`
  carrying a multi-platform install hint; `symprint doctor` reports the same.
- Config follows XDG (`~/.config/symprint/config.toml`, `SYMPRINT_*` env).
- No sibling Symaira binary is required at startup. Optional integrations
  (symvault for secrets, etc.) would be runtime-detected, never linked.

## 4. The Markdown contract

Frontmatter is **semantic metadata only** — presentation lives in the profile.
Precedence, low → high: **built-in defaults < config < profile < frontmatter <
CLI/MCP flags**. Full field reference: [markdown-contract.md](markdown-contract.md).

```yaml
---
profile: behoerde            # selects template + engine opts + PDF/A+UA + Form A
lang: de                     # required for PDF/UA (sets #set text(lang:))
title: "Anhörung nach § 28 VwVfG"
date: 2026-06-30             # TT.MM.JJJJ or JJJJ-MM-TT (leading zeros validated)
sender:    { name: "Stadt Musterstadt — Bauordnungsamt", address: [...] }
recipient: { name: "Frau Erika Mustermann", address: [...] }
infoblock: { Unser Zeichen: "BAU-2026-04711", Bearbeiter: "H. Schmidt" }
betreff: "Anhörung im Verfahren BAU-2026-04711"
pdf: { standard: [a-2a, ua-1] }
---
Sehr geehrte Frau Mustermann, …
```

## 5. Profiles & German standards

Catalog: [profiles.md](profiles.md). The two letter profiles encode **DIN 5008**
geometry (Form A for `behoerde`, Form B for `brief`). All values validated
against KOMA-Script source (LPPL):
[ DIN5008A.lco](https://github.com/KOMA-Script/KOMA-Script/blob/main/DIN5008A.lco) /
[ DINmtext.lco](https://github.com/KOMA-Script/KOMA-Script/blob/main/DINmtext.lco) (Form A) and
[ DIN5008B.lco](https://github.com/KOMA-Script/KOMA-Script/blob/main/DIN5008B.lco) /
[ DIN.lco](https://github.com/KOMA-Script/KOMA-Script/blob/main/DIN.lco) (Form B):

| Element | Form A (Behörde) | Form B (Brief) | Source |
|---|---|---|---|
| Briefkopf height | 27 mm | 45 mm | `toaddrvpos` |
| Anschriftfeld top (ref) | 44.7 mm | 62.7 mm | `toaddrvpos + backaddrheight + specialmailheight` |
| Anschriftfeld size | 45 × 85 mm | 45 × 85 mm | `toaddrheight × toaddrwidth` |
| Infoblock top / left / width | 32 / 125 / 75 mm | 50 / 125 / 75 mm | `locvpos`, `firstheadwidth`, `locwidth` |
| Falzmarken | 87 / 192 mm | 105 / 210 mm | `tfoldmarkvpos` / `bfoldmarkvpos` |
| Lochmarke | 148.5 mm | 148.5 mm | A4 height ÷ 2 |

**Compliance stack (behoerde):**

- **PDF/A-2a** (ISO 19005-2, accessible level) for E-Government / TR-RESISCAN
  archiving.
- **PDF/UA-1** (ISO 14289-1) for BITV 2.0 / EN 301 549 §10 accessibility:
  Typst writes Tagged PDF by default, sets the document language, and validates
  at compile time — it *fails closed* (e.g. headings must start at level 1).
- Combined in one command: `typst compile --pdf-standard a-2a,ua-1`.

> **Author duties Typst can't infer:** image alt text, table header rows, and a
> sensible heading hierarchy. The contract surfaces these; P1 wires alt text from
> Markdown image syntax and marks fold/hole marks as `pdf.artifact()`.

## 6. Verification status (what's proven)

Rendered end-to-end with Typst 0.15.0 on 2026-06-30:

- ✅ `report` → cover page + TOC + running header + page numbers (3 pp, PDF 1.7).
- ✅ `brief` → DIN 5008-style letter (1 p).
- ✅ `rechnung` → data-driven invoice table from `data:` (1 p).
- ✅ `behoerde` → **PDF/A-2a + PDF/UA-1 confirmed**: output contains `pdfaid`
  (part 2), `pdfuaid`, and `StructTreeRoot`. This **answers the research's #1
  open question** — the `a-2a,ua-1` combination really does emit both standards
  on 0.15.0 (issue typst#7183 not reproduced).
- ✅ `--reproducible` → byte-identical output (same SHA-256 across runs).
- ✅ Contract enforcement: unknown keys rejected; missing required fields, bad
  dates, and missing PDF/UA `lang`/`title` reported as validation errors.
- ✅ Engine-missing path is graceful (install hint, no crash).
- ✅ `go vet` clean, `go test ./...` green.

**Not yet validated:** exact DIN 5008 millimetre fidelity (needs the standard +
veraPDF), and PDF/A/UA conformance beyond Typst's own validator (needs veraPDF).

## 7. Roadmap (phases)

### Phase 0 — Scaffold ✅ (done)
Project skeleton on corekit; strict contract + 4-profile registry; Typst
shell-out; CLI (`render/profiles/validate/doctor/config/mcp/version`); MCP
server; embedded templates; reproducible output; tests; CI with a render smoke
test. End-to-end render of all four profiles verified.

### Phase 1 — Compliance hardening (Behörde-grade)
- Validate DIN 5008 geometry against the standard + KOMA `DIN5008A.lco`; lock the
  millimetre constants; promote `brief`/`behoerde` from *scaffold* to *beta*. ✅
- Wire **veraPDF** into CI as a gate for `behoerde` (assert PDF/A-2a + PDF/UA-1). ✅
  Uses `verapdf/cli` Docker image; validates both profiles on every push/PR.
- Accessibility completeness: propagate Markdown image alt text to `image(alt:)`,
  map Markdown tables to `table.header`, wrap fold/hole marks in `pdf.artifact()`,
  ensure logical reading order and metadata title.
- `symprint validate` gains a `--strict` mode that previews accessibility gaps.

### Phase 2 — Determinism & offline
- Embed brand fonts via `go:embed`; pass `--font-path` + `--ignore-system-fonts`
  for machine-independent output; ship a sans face for DIN sub-10pt text. ✅
- **Vendor the `cmarker` package** so the first render needs no network → fully
  standalone. ✅ (`mitex` vendoring follows with the math support work.)
- Pin the Typst version and hash-test sample outputs in CI.

### Phase 3 — Richer documents & post-processing
- LaTeX math via cmarker's math callback → `mitex`, with alt text for PDF/UA.
- `pdfcpu` integration: merge multi-document bundles, stamp page numbers /
  watermarks, structural validate; `symprint merge`, `symprint stamp`.
- Optional `pandoc -t typst` high-fidelity path (auto-detected) for complex
  Markdown (definition lists, nested tables, citations).

### Phase 4 — Use-case depth
- `rechnung`: VAT (USt) math, legally required fields (Rechnungsnummer,
  Leistungsdatum, USt-ID), EPC-QR/GiroCode, and optional PDF/A-3 with a
  ZUGFeRD-style XML attachment.
- More profiles: `protokoll`, `angebot`, `bewerbung`, `cv`, a slide profile.
- User-defined / overlay profiles loaded from `~/.config/symprint/profiles/`
  (the Pandoc-defaults pattern) so teams ship their own branding.

### Phase 5 — Experience & ecosystem
- Fast preview loop (`symprint watch`, wrapping `typst watch`) and a `--preview`
  PNG for agents.
- A `symprint init <profile>` scaffolder that emits a starter `.md`.
- Homebrew formula via the tap (`depends_on "typst"`); editor snippets.
- Optional WeasyPrint/CSS profile for HTML-shaped content.
- No hosted render API and no paid tier: `symprint` ships as one tool, and
  **no commercial code belongs in this repo.**

## 8. Open questions & risks

- **DIN measurements** validated against KOMA-Script source (LPPL) — see §5 table.
  The paid DIN 5008 text is the ultimate authority; KOMA-Script is the best
  freely available cross-reference.
- **Typst is pre-1.0** (v0.15.0); pin engine + package versions — templates can
  drift across minors. PDF/UA-2 is not available yet (planned later 2026).
- **cmarker limits**: no reusable Typst functions / native Typst math from inside
  Markdown; mitigated by the math callback and the optional pandoc path.
- **Accessibility ≠ automatic**: structural PDF/UA can pass while human
  Matterhorn checkpoints (meaningful alt text, reading order) still need author
  input. The tool enforces structure and surfaces the rest.

## 9. References

Typst PDF standards <https://typst.app/docs/reference/pdf/> ·
cmarker <https://typst.app/universe/package/cmarker/> ·
DIN 5008 geometry <https://www.din-5008-richtlinien.de/> ·
PDF/UA + BITV <https://www.bundesfachstelle-barrierefreiheit.de/> ·
veraPDF <https://verapdf.org/> ·
pdfcpu <https://github.com/pdfcpu/pdfcpu> ·
typst-letter-pro <https://github.com/Sematre/typst-letter-pro>.
