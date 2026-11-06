// meeting.typ — Meeting Minutes profile.
//
// Lays out meeting minutes cleanly and professionally: title, metadata card
// (date, duration, location, meeting ID), participants list, and custom-styled
// body sections. Ensures compliance with PDF/A-2a and PDF/UA-1 standards.
//
// Contract: apply(meta, doc) where `meta` is the frontmatter (meta.json) and
// `doc` is the cmarker-rendered Markdown body.

#let accent = rgb("#1e3d59")

#let apply(meta, doc) = {
  let title = meta.at("title", default: "")
  let date = meta.at("date", default: "")
  let lang = meta.at("lang", default: "de")
  let meeting-id = meta.at("meeting_id", default: "")
  let participants = meta.at("participants", default: ())
  if participants == none { participants = () }
  let duration = meta.at("duration", default: "")
  let location = meta.at("location", default: "")

  set document(title: title)
  set text(lang: lang, size: 10.5pt)
  set par(justify: true, leading: 0.65em)

  set page(
    paper: "a4",
    margin: (top: 3cm, bottom: 2.5cm, x: 2.5cm),
    numbering: "1",
    number-align: center,
    header: context {
      // Running header with a line (starting on page 2)
      if counter(page).get().first() > 1 {
        set text(size: 8.5pt, fill: luma(40%))
        grid(
          columns: (1fr, auto),
          align: (left, right),
          text(weight: "bold", title),
          if meeting-id != "" { [ID: #meeting-id] } else { date }
        )
        v(-0.4em)
        line(length: 100%, stroke: 0.5pt + luma(70%))
      }
    }
  )

  // Level 1 headings: Accent line below, clean spacing
  show heading.where(level: 1): it => {
    v(0.5em)
    block(
      width: 100%,
      stroke: (bottom: 1.5pt + accent),
      inset: (bottom: 0.3em),
      above: 1.5em,
      below: 0.8em,
      text(fill: accent, size: 14pt, weight: "bold", it.body)
    )
  }

  // Level 2 headings: Subtle grey, no line
  show heading.where(level: 2): it => {
    block(
      above: 1.2em,
      below: 0.6em,
      text(fill: accent.lighten(20%), size: 11.5pt, weight: "bold", it.body)
    )
  }

  // Terms / Description Lists (often used for speaker transcripts: / Speaker: ... )
  // Set them to break cleanly across pages without clipping.
  show terms: set terms(hanging-indent: 1.5em, spacing: 1em)
  show terms.item: it => {
    block(
      width: 100%,
      breakable: true,
      [
        #text(weight: "bold", fill: accent, it.term)
        #h(0.5em)
        #it.description
      ]
    )
  }

  // Document Title
  v(0.5em)
  text(size: 22pt, weight: "bold", fill: accent, title)
  v(0.8em)

  // Metadata block (Date, Duration, Location, ID)
  block(
    fill: rgb("#f8fafc"),
    stroke: 0.5pt + rgb("#e2e8f0"),
    inset: 1.2em,
    radius: 4pt,
    width: 100%,
    [
      #grid(
        columns: (1fr, 1fr),
        row-gutter: 0.8em,
        [*Date:* #if date != "" { date } else { "—" }],
        [*Duration:* #if duration != "" { duration } else { "—" }],
        [*Location:* #if location != "" { location } else { "—" }],
        [*Meeting ID:* #if meeting-id != "" { meeting-id } else { "—" }]
      )
    ]
  )

  // Participants block
  if participants.len() > 0 {
    v(0.8em)
    block(
      fill: rgb("#f8fafc"),
      stroke: 0.5pt + rgb("#e2e8f0"),
      inset: 1.2em,
      radius: 4pt,
      width: 100%,
      [
        #text(weight: "bold", fill: accent, size: 10.5pt)[Participants:]
        #v(0.5em)
        #grid(
          columns: (1fr, 1fr, 1fr),
          row-gutter: 0.6em,
          column-gutter: 1em,
          ..participants.map(p => [- #p])
        )
      ]
    )
  }

  v(1em)

  doc
}
