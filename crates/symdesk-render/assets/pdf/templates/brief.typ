// brief.typ — DIN 5008 letter (Geschäftsbrief). Default Form B.
//
// DIN 5008 geometry validated against KOMA-Script source (LPPL):
//   Form A: KOMA DIN5008A.lco + DINmtext.lco
//   Form B: KOMA DIN5008B.lco + DIN.lco
//   Source: https://github.com/KOMA-Script/KOMA-Script
//
//   Form A: Briefkopf 27 mm, Anschriftfeld ref 44.7 mm, Infoblock top 32 mm,
//           Falzmarken 87/192 mm
//   Form B: Briefkopf 45 mm, Anschriftfeld ref 62.7 mm, Infoblock top 50 mm,
//           Falzmarken 105/210 mm
//   Common: Anschriftfeld 45×85 mm; Lochmarke 148.5 mm; left margin 25 mm.
//
// Contract: apply(meta, doc).

#let apply(meta, doc) = {
  let form = meta.at("form", default: "B")
  let isA = form == "A"
  let addr-top = if isA { 44.7mm } else { 62.7mm }
  let info-top = if isA { 32mm } else { 50mm }
  let falz1 = if isA { 87mm } else { 105mm }
  let falz2 = if isA { 192mm } else { 210mm }

  let lang = meta.at("lang", default: "de")
  let sender = meta.at("sender", default: (:))
  let recipient = meta.at("recipient", default: (:))
  let infoblock = meta.at("infoblock", default: (:))
  if sender == none { sender = (:) }
  if recipient == none { recipient = (:) }
  if infoblock == none { infoblock = (:) }
  let betreff = meta.at("betreff", default: "")
  let date = meta.at("date", default: "")
  let title = meta.at("title", default: "")
  if title == "" { title = betreff }
  if title == "" { title = "Brief" }

  set document(title: title, author: sender.at("name", default: ""))
  set text(lang: lang, size: 11pt)
  set par(leading: 0.6em)

  set page(
    paper: "a4",
    margin: (left: 25mm, right: 20mm, top: 0mm, bottom: 20mm),
    numbering: none,
    // Fold + hole marks are page artifacts — not document content, but printing
    // aids. Wrapped in pdf.artifact(kind: "page") for PDF/UA compliance so
    // assistive technology ignores them in the reading order.
    background: {
      pdf.artifact(kind: "page")[
        #place(top + left, dx: 5mm, dy: 148.5mm, line(length: 4mm, stroke: 0.3pt))
        #place(top + left, dx: 5mm, dy: falz1, line(length: 4mm, stroke: 0.3pt))
        #place(top + left, dx: 5mm, dy: falz2, line(length: 4mm, stroke: 0.3pt))
      ]
    },
  )

  // Anschriftfeld (45×85 mm) positioned from the top edge so it lines up with a
  // DIN lang window envelope. Placed absolutely; body flow starts afterwards.
  place(top + left, dy: addr-top, box(width: 85mm, height: 45mm, {
    // Zusatz-/Vermerkzone: small return address (Rücksendeangabe).
    if sender.at("name", default: "") != "" {
      text(size: 7pt, sender.name)
      v(1mm)
      line(length: 80mm, stroke: 0.3pt + luma(60%))
      v(2mm)
    }
    // Anschriftzone: recipient.
    set text(size: 11pt)
    recipient.at("name", default: "")
    for l in recipient.at("address", default: ()) {
      linebreak()
      l
    }
  }))

  // Infoblock (right column): label/value lines ending in Datum.
  place(top + left, dx: 100mm, dy: info-top, box(width: 75mm, {
    set text(size: 9pt)
    for (k, v) in infoblock.pairs() {
      [#k: #v]
      linebreak()
    }
    if date != "" [ Datum: #date ]
  }))

  // Push the running text below the letterhead zone (place() does not advance
  // layout), then the subject and body.
  v(addr-top + 45mm + 12mm)

  if betreff != "" {
    text(weight: "bold", betreff)
    v(1.2em)
  }

  doc
}
