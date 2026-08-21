# Profiles

A **profile** is a named use-case bundle: it fixes the template, engine options,
page geometry, and output guarantees. Selecting one (`profile:` in frontmatter or
`--profile`) is the only visual decision a document makes. List them at runtime:

```bash
symprint profiles            # table
symprint profiles behoerde   # detail + capabilities + required fields
```

| Profile | Stability | Engine | DIN form | PDF standard | Required fields |
|---|---|---|---|---|---|
| `brief` | scaffold | typst | B | — (tagged) | `recipient` |
| `behoerde` | scaffold | typst | A | `a-2a`, `ua-1` | `recipient`, `title`, `lang` |
| `report` | beta | typst | — | — (tagged) | `title` |
| `rechnung` | scaffold | typst | B | — (tagged) | `recipient`, `data` |
| `meeting` | beta | typst | — | `a-2a`, `ua-1` | `title`, `lang`, `date` |

*Stability:* `scaffold` = template present, geometry/compliance not yet validated;
`beta` = working and exercised; `stable` = validated + locked.

## brief — DIN 5008 letter (Form B)

General German business letter. Lays out the Anschriftfeld (45 × 85 mm, aligned
to a DIN lang window), a right-hand Infoblock, the subject line, and fold/hole
marks in the left margin. Output is a tagged PDF; no archival requirement.

## behoerde — Authority letter (Form A) + PDF/A + PDF/UA

Same DIN 5008 geometry as `brief` but **Form A** by default and rendered as
**PDF/A-2a + PDF/UA-1** — accessible, tagged, and archivable for E-Government /
BITV 2.0. Typst validates accessibility at compile time and **fails closed**
(e.g. the first heading must be level 1), so a non-conformant document is
rejected rather than silently shipped. Requires `lang` and `title` (PDF/UA).

> Form A vs B is a soft convention; override with `form: B` if your authority
> uses Form B.

## report — Report / Bericht

Multi-page report with a **cover page** (title, subtitle, authors, date), an
automatic **table of contents**, a running **header** with a rule, **page
numbers**, and coloured, sized headings. Toggle parts via `titlepage`, `toc`,
`toc-depth`, and the `header-*` / `footer-*` overrides.

## rechnung — German invoice (data-driven) *(scaffold)*

Renders a recipient block and a line-item table from a `data:` payload
(`number`, `currency`, `items[]` with `description`/`qty`/`unit_price`) and a
computed total. VAT (USt) math, legally required fields, and EPC-QR/GiroCode are
[Phase 4](architecture.md#7-roadmap-phases).

```yaml
data:
  number: "2026-0042"
  currency: "EUR"
  items:
    - { description: "Beratung (Stunden)", qty: 3, unit_price: 120.0 }
```

## meeting — Meeting Minutes + PDF/A + PDF/UA

Turns reviewed SymMeet/SymDesk Markdown documents into polished, accessible (PDF/UA-1), and archivable (PDF/A-2a) meeting minutes. Automatically generates a clean, structured metadata block at the top including the date, meeting ID, duration, and location, as well as a multi-column participant block. Ensures transcript speaker-timestamp lines and action-item checkboxes render elegantly and flow naturally across page breaks. Requires `title`, `lang`, and `date`.

## Adding a profile

1. Add a `*.typ` template under `internal/assets/templates/` exposing
   `apply(meta, doc)`.
2. Register it in `internal/press/profile.go` with its template, engine, DIN
   form, `PDFStandard`, and `RequiredFields`.
3. Add an example to `examples/` (CI renders them) and document any new
   frontmatter field in [markdown-contract.md](markdown-contract.md).

Profiles **declare their guarantees** via `PDFStandard` so `Capability()` and
accessibility validation stay truthful.
