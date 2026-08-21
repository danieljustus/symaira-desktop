// behoerde.typ — Authority letter profile.
//
// Same DIN 5008 geometry as `brief`, but defaults to Form A (the form typically
// used by German authorities) and is rendered with PDF/A-2a + PDF/UA-1 (the
// --pdf-standard flag is supplied by symprint, not the template). Tagged PDF is
// on by default in Typst; the document language set here is required for PDF/UA.
//
// Accessibility duties still owed by Phase 1 hardening: alt text on images,
// table.header rows, and wrapping fold/hole marks in pdf.artifact(). See
// docs/architecture.md.

#import "brief.typ"

#let apply(meta, doc) = {
  let m = meta
  // Authority default: DIN 5008 Form A (user-overridable via `form:`).
  if m.at("form", default: "") == "" {
    m.insert("form", "A")
  }
  // Ensure a language is present for PDF/UA tagging.
  if m.at("lang", default: "") == "" {
    m.insert("lang", "de")
  }
  brief.apply(m, doc)
}
