// rechnung.typ — German invoice profile (SCAFFOLD).
//
// Renders a recipient block and a line-item table from the frontmatter `data:`
// object (meta.data). VAT (USt) math, legally required fields, and an optional
// EPC-QR/GiroCode payment code are Phase 4 work — this template only proves the
// data-driven path (Typst reads structured data straight from meta.json).
//
// Expected data shape:
//   data:
//     number: "2026-0042"
//     items:
//       - { description: "Leistung A", qty: 2, unit_price: 50.0 }
//     currency: "EUR"

// money formats an amount with two decimals and the currency suffix.
#let money(value, currency) = {
  let cents = calc.round(value * 100)
  let euros = calc.floor(cents / 100)
  let rem = calc.rem(cents, 100)
  let pad = if rem < 10 { "0" } else { "" }
  str(euros) + "," + pad + str(rem) + " " + currency
}

#let apply(meta, doc) = {
  let lang = meta.at("lang", default: "de")
  let recipient = meta.at("recipient", default: (:))
  let data = meta.at("data", default: (:))
  let items = data.at("items", default: ())
  let currency = data.at("currency", default: "EUR")
  let number = data.at("number", default: "")

  set document(title: "Rechnung " + number)
  set text(lang: lang, size: 11pt)
  set page(paper: "a4", margin: (x: 25mm, top: 45mm, bottom: 20mm), numbering: "1")

  // Recipient (windowed-envelope position is a Phase 1 refinement).
  block({
    recipient.at("name", default: "")
    for l in recipient.at("address", default: ()) {
      linebreak()
      l
    }
  })
  v(2em)

  if number != "" {
    text(size: 16pt, weight: "bold", "Rechnung " + number)
    v(1em)
  }

  // Line items.
  let total = 0.0
  let rows = ()
  for it in items {
    let qty = it.at("qty", default: 1)
    let price = it.at("unit_price", default: 0.0)
    let line-total = qty * price
    total = total + line-total
    rows.push((it.at("description", default: ""), str(qty), money(price, currency), money(line-total, currency)))
  }

  table(
    columns: (1fr, auto, auto, auto),
    align: (left, right, right, right),
    stroke: 0.5pt + luma(70%),
    table.header([*Beschreibung*], [*Menge*], [*Einzelpreis*], [*Gesamt*]),
    ..rows.flatten().map(c => [#c]),
  )
  v(0.6em)
  align(right, text(weight: "bold", "Summe: " + money(total, currency)))

  if doc != none { v(1.5em); doc }
}
