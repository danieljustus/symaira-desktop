# The symprint Markdown contract

A `symprint` document is **Markdown with a leading YAML frontmatter block**:

```markdown
---
profile: report
title: "My document"
---
# Body in Markdown
```

The frontmatter carries **semantic metadata only** — never layout. All
presentation lives in the selected [profile](profiles.md). The contract is
**strict**: an unknown key is an error (so typos fail loudly), and the parser
reports the exact line.

## Precedence

When the same setting can come from several places, higher wins:

```
built-in defaults  <  config (~/.config/symprint)  <  profile  <  frontmatter  <  CLI / MCP flags
```

## Fields

| Key | Type | Profiles | Notes |
|---|---|---|---|
| `profile` | string | all | Selects the profile. May be overridden by `--profile`. |
| `lang` | string | all | Document language (e.g. `de`). **Required** for PDF/UA profiles. |
| `title` | string | all | Document title → PDF metadata. **Required** for `report`, `behoerde`. |
| `subtitle` | string | report | Shown under the title on the cover. |
| `author` | string \| list | report | Scalar or list: `author: Jane` or `author: [Jane, John]`. |
| `date` | string | all | `TT.MM.JJJJ` or `JJJJ-MM-TT` (DIN 5008; leading zeros validated). |
| `keywords` | list | report | PDF metadata keywords. |
| `form` | `A` \| `B` | brief, behoerde | DIN 5008 form. Defaults from the profile. |
| `sender` | `{name, address[]}` | brief, behoerde, rechnung | Return address (Rücksendeangabe). |
| `recipient` | `{name, address[]}` | brief, behoerde, rechnung | Anschriftfeld. **Required** for letters/invoice. |
| `infoblock` | map | brief, behoerde | DIN 5008 Infoblock; key/value lines, ends with Datum. |
| `betreff` | string | brief, behoerde | Subject line (bold). |
| `titlepage` | bool | report | Toggle the cover page (default true). |
| `toc` | bool | report | Toggle the table of contents (default true). |
| `toc-depth` | int | report | TOC heading depth (default 3). |
| `header-left` / `header-right` | string | report | Running-header overrides. |
| `footer-left` / `footer-right` | string | report | Running-footer overrides. |
| `data` | map | rechnung | Structured payload (invoice number, items, currency). |
| `meeting_id` | string | meeting | Identifier for the meeting. |
| `participants` | list | meeting | List of display strings of meeting participants. |
| `duration` | string | meeting | Duration of the meeting (e.g. `45m`, `1h 30m`). |
| `location` | string | meeting | Location of the meeting (e.g. `Room Alpha`, `Discord`). |
| `pdf.standard` | list | all | typst `--pdf-standard` ids, e.g. `[a-2a, ua-1]`. |
| `pdf.reproducible` | bool | all | Force byte-stable output for this document. |

Fields not relevant to the selected profile are accepted but ignored — only
*unknown* keys (typos) are rejected.

## Examples

A minimal report:

```yaml
---
profile: report
lang: de
title: "Quartalsbericht Q2 2026"
author: ["Daniel Justus"]
toc: true
---
```

An authority letter (accessible + archivable):

```yaml
---
profile: behoerde
lang: de
title: "Anhörung nach § 28 VwVfG"
date: 2026-06-30
recipient: { name: "Frau Erika Mustermann", address: ["Musterweg 12", "54321 Beispielstadt"] }
infoblock: { "Unser Zeichen": "BAU-2026-04711" }
betreff: "Anhörung im Verfahren BAU-2026-04711"
pdf: { standard: [a-2a, ua-1] }
---
```

See [`examples/`](../examples/) for one complete document per profile. Validate
any document without rendering:

```bash
symprint validate mydoc.md
```

## Accessibility (PDF/UA)

For `behoerde` profiles (PDF/A-2a + PDF/UA-1), authors are responsible for:

- **Image alt text**: Use `![descriptive alt text](image.png)` syntax. The alt text
  is propagated to Typst's `image(alt:)` and must be meaningful — describe the
  image's content or function, not just its appearance. Decorative images should
  use `alt=""` (empty string).

  Local image paths resolve **relative to the Markdown file** and must stay
  inside its directory tree — absolute paths, `..` traversal and symlinks
  pointing outside are rejected with a clear error. Only the inline syntax
  `![alt](path)` is resolved; reference-style images (`![alt][id]`) and remote
  URLs are not copied into the render. Renders without a filesystem base
  (MCP) cannot resolve local images.

- **Heading hierarchy**: Start with `# Heading 1` and don't skip levels. PDF/UA
  requires a logical heading tree — screen readers use it for navigation.

- **Table headers**: Use Markdown table syntax with a header row:
  ```markdown
  | Column A | Column B |
  |----------|----------|
  | data     | data     |
  ```
  The header row is automatically tagged as `<TH>` in the PDF structure tree.

- **Document title and language**: Always set `title` and `lang` in frontmatter.
  These are required for PDF/UA compliance and appear in PDF metadata.

Validate accessibility gaps with:

```bash
symprint validate --strict mydoc.md
```
