// report.typ — Report / Bericht profile.
//
// Demonstrates the features symprint promises: cover page (Deckblatt), automatic
// table of contents, running header with a rule, page numbers (Seitenzahlen),
// coloured + sized headings (Überschriftgröße/Farben). Pure set/show rules — no
// LaTeX package soup.
//
// Contract: apply(meta, doc) where `meta` is the frontmatter (meta.json) and
// `doc` is the cmarker-rendered Markdown body.

#let accent = rgb("#1f4e79")

#let apply(meta, doc) = {
  let title = meta.at("title", default: "")
  let subtitle = meta.at("subtitle", default: "")
  let authors = meta.at("author", default: ())
  if authors == none { authors = () }
  let date = meta.at("date", default: "")
  let lang = meta.at("lang", default: "de")
  let want-cover = meta.at("titlepage", default: true)
  let want-toc = meta.at("toc", default: true)
  let toc-depth = meta.at("toc_depth", default: 3)
  let header-left = meta.at("header_left", default: title)
  let header-right = meta.at("header_right", default: "")

  set document(title: title, author: authors)
  set text(lang: lang, size: 11pt)
  set par(justify: true, leading: 0.65em)

  set page(
    paper: "a4",
    margin: (top: 2.5cm, bottom: 2.5cm, x: 2.5cm),
    numbering: "1",
    number-align: center,
    header: context {
      // No header on the cover (page 1).
      if counter(page).get().first() > 1 {
        set text(size: 9pt, fill: luma(40%))
        grid(columns: (1fr, auto), align: (left, right), header-left, header-right)
        v(-0.4em)
        line(length: 100%, stroke: 0.5pt + luma(70%))
      }
    },
  )

  // Coloured, sized headings.
  set heading(numbering: "1.1")
  show heading.where(level: 1): it => {
    set text(size: 17pt, fill: accent, weight: "bold")
    block(above: 1.4em, below: 0.7em, it)
  }
  show heading.where(level: 2): set text(size: 13pt, fill: accent.darken(10%))
  show heading.where(level: 3): set text(size: 11.5pt, fill: luma(25%))

  // Cover page.
  if want-cover and title != "" {
    page(numbering: none, header: none, {
      set align(center + horizon)
      block({
        text(size: 28pt, weight: "bold", fill: accent, title)
        if subtitle != "" {
          v(0.5em)
          text(size: 15pt, fill: luma(35%), subtitle)
        }
        v(1.5em)
        line(length: 40%, stroke: 1pt + accent)
        v(1.5em)
        if authors.len() > 0 { text(size: 13pt, authors.join(", ")) }
        if date != "" {
          v(0.4em)
          text(size: 11pt, fill: luma(40%), date)
        }
      })
    })
  }

  // Table of contents.
  if want-toc {
    show outline.entry.where(level: 1): set text(weight: "bold")
    outline(title: "Inhaltsverzeichnis", depth: toc-depth, indent: 1em)
    pagebreak()
  }

  doc
}
