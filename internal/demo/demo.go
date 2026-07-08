// Package demo provides a built-in demo vault for symdesk.
// It materializes ~10 realistic example documents, 3–5 knowledge notes,
// and 3 saved views into a directory suitable for first-run exploration.
//
// All names are obviously fictional (Max Mustermann, Musterfirma GmbH,
// Beispielstadt). No real personal data is included.
package demo

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/danieljustus/symaira-desktop/internal/dbviews"
)

// documents is the set of 10 demo documents with contract-v2 frontmatter.
// Documents 1 and 8 are a near-duplicate pair (consecutive monthly invoices
// from the same correspondent) so that symdesk similar finds them.
// Document 4 has low confidence (30) and document 10 is missing document_date,
// ensuring the review queue is non-empty.
var documents = []struct {
	name string // filename inside the vault (no extension)
	md   string // full markdown content with frontmatter
	pdf  string // title for the synthetic PDF
}{
	{
		name: "Rechnung_Musterfirma_2026-01",
		pdf:  "Rechnung Musterfirma Januar 2026",
		md: `---
title: "Rechnung Musterfirma Januar 2026"
document_type: "invoice"
document_date: "2026-01-15"
correspondent: "Musterfirma GmbH"
person: "Max Mustermann"
status: "open"
due_date: "2026-02-15"
confidence: 95
tags:
  - rechnung
  - 2026
---

# Rechnung Musterfirma GmbH – Januar 2026

**Rechnungsnr.:** MF-2026-001
**Kunde:** Max Mustermann, Musterstraße 1, 12345 Beispielstadt

| Position | Beschreibung | Betrag |
|----------|-------------|--------|
| 1 | Webhosting Basic (12 Monate) | 120,00 € |
| 2 | Domainregistrierung example.de | 15,00 € |
| 3 | SSL-Zertifikat | 25,00 € |

**Nettobetrag:** 160,00 €
**MwSt. 19%:** 30,40 €
**Gesamtbetrag:** 190,40 €

**Zahlungsziel:** 15.02.2026
**Verweis:** [[Musterfirma GmbH]]

Webhosting Basic Monatsrechnung für Max Mustermann von Musterfirma GmbH.
Domainregistrierung example.de und SSL-Zertifikat sind im Paket enthalten.
Gesamtbetrag 190,40 Euro inklusive MwSt. 19 Prozent.
Zahlungsziel 15. Februar 2026 für die Rechnung MF-2026-001.
`,
	},
	{
		name: "Kontoauszug_Sparkasse_2026-01",
		pdf:  "Kontoauszug Sparkasse Januar 2026",
		md: `---
title: "Kontoauszug Sparkasse Januar 2026"
document_type: "bank_statement"
document_date: "2026-01-31"
correspondent: "Sparkasse Beispielstadt"
person: "Max Mustermann"
status: "paid"
confidence: 92
tags:
  - kontoauszug
  - 2026
---

# Kontoauszug – Sparkasse Beispielstadt

**Kontonummer:** DE89 3704 0044 0532 0130 00
**Zeitraum:** 01.01.2026 – 31.01.2026

| Datum | Buchungstext | Betrag |
|-------|-------------|--------|
| 02.01. | Gehalt Musterfirma GmbH | +3.200,00 € |
| 05.0 | Miete Wohnung | -850,00 € |
| 10.01. | Versicherung AG | -120,00 € |
| 15.01. | Lebensmittel | -234,56 € |
| 20.01. | Strom Stadtwerke | -95,00 € |

**Kontostand:** 2.150,44 €

**Hinweis:** Dies ist ein simulierter Kontoauszug zu Demonstrationszwecken.
`,
	},
	{
		name: "Versicherungspolice_Erika",
		pdf:  "Versicherungspolice Erika Mustermann",
		md: `---
title: "Versicherungspolice Erika Mustermann"
document_type: "insurance_policy"
document_date: "2025-06-01"
correspondent: "Versicherung AG"
person: "Erika Mustermann"
status: "done"
due_date: "2026-06-01"
confidence: 88
tags:
  - versicherung
  - police
---

# Versicherungspolice – Versicherung AG

**Police-Nr.:** VAG-2025-78901
**Versicherungsnehmer:** Erika Mustermann
**Versicherungsart:** Private Krankenversicherung
**Beginn:** 01.06.2025
**Laufzeit:** 12 Monate
**Monatlicher Beitrag:** 380,00 €

**Leistungen:**
- Arztbesuche: 100% Erstattung
- Krankenhausaufenthalt: 100% Erstattung
- Zahnbehandlung: 80% Erstattung

**Verweis:** [[Versicherung AG]]
`,
	},
	{
		name: "Steuerbescheid_Max_2025",
		pdf:  "Steuerbescheid Max Mustermann 2025",
		md: `---
title: "Steuerbescheid Max Mustermann 2025"
document_type: "tax_assessment"
document_date: "2026-03-10"
correspondent: "Finanzamt Beispielstadt"
person: "Max Mustermann"
status: "needs_review"
confidence: 30
tags:
  - steuer
  - bescheid
  - 2025
---

# Steuerbescheid 2025 – Finanzamt Beispielstadt

**Aktenzeichen:** FA-BES-2025-4567
**Steuerpflichtiger:** Max Mustermann, Musterstraße 1, 12345 Beispielstadt

**Veranlagungsjahr:** 2025
**Steuerart:** Einkommensteuer

| Position | Betrag |
|----------|--------|
| Gesamtbetrag der Einkünfte | 48.500,00 € |
| Zu versteuerndes Einkommen | 42.200,00 € |
| Einkommensteuer | 8.840,00 € |
| Solidaritätszuschlag | 486,20 € |
| Kirchensteuer | 795,60 € |

**Nachzahlung:** 1.250,00 €

**Hinweis:** Niedrige Confidence (30%) – manuelle Prüfung empfohlen.
Die extrahierten Daten könnten unvollständig sein.

**Verweis:** [[Finanzamt Beispielstadt]]
`,
	},
	{
		name: "Arztbrief_Dr_Beispiel",
		pdf:  "Arztbrief Dr. med. Beispiel",
		md: `---
title: "Arztbrief Dr. med. Beispiel"
document_type: "medical_letter"
document_date: "2026-02-20"
correspondent: "Dr. med. Beispiel"
person: "Erika Mustermann"
status: "done"
confidence: 91
tags:
  - arztbrief
  - gesundheit
---

# Arztbrief – Dr. med. Beispiel

**Patient:** Erika Mustermann
**Geburtsdatum:** 15.03.1985
**Untersuchungsdatum:** 20.02.2026

**Befund:**
Routineuntersuchung ohne pathologische Befunde. Blutdruck 120/80 mmHg,
Gewicht stabil, Blutwerte im Normbereich.

**Empfehlung:**
Nächste Kontrolle in 12 Monaten.

**Arzt:** Dr. med. Beispiel, Praxis Musterweg 10, 12345 Beispielstadt
`,
	},
	{
		name: "Vertrag_Dienstleister",
		pdf:  "Vertrag Dienstleister GmbH",
		md: `---
title: "Vertrag Dienstleister GmbH"
document_type: "contract"
document_date: "2025-11-01"
correspondent: "Dienstleister GmbH"
person: "Max Mustermann"
status: "submitted"
due_date: "2026-11-01"
confidence: 87
tags:
  - vertrag
  - dienstleistung
---

# Dienstleistungsvertrag – Dienstleister GmbH

**Vertragsnr.:** DG-2025-123
**Vertragspartner:** Max Mustermann
**Anbieter:** Dienstleister GmbH, Geschäftsweg 5, 12345 Beispielstadt

**Vertragsgegenstand:** Wartung und Support für IT-Infrastruktur

**Laufzeit:** 01.11.2025 – 01.11.2026
**Monatlich kündbar nach Erstlaufzeit**

**Kosten:**
- Monatliche Pauschale: 250,00 € netto
- Reaktionszeit: 4 Stunden (werktags)

**Verweis:** [[Dienstleister GmbH]]
`,
	},
	{
		name: "Gehaltsabrechnung_Max_2026-01",
		pdf:  "Gehaltsabrechnung Max Januar 2026",
		md: `---
title: "Gehaltsabrechnung Max Januar 2026"
document_type: "payslip"
document_date: "2026-01-31"
correspondent: "Musterfirma GmbH"
person: "Max Mustermann"
status: "paid"
confidence: 94
tags:
  - gehaltsabrechnung
  - 2026
---

# Gehaltsabrechnung – Januar 2026

**Arbeitgeber:** Musterfirma GmbH
**Arbeitnehmer:** Max Mustermann
**Abrechnungszeitraum:** 01.01.2026 – 31.01.2026

| Position | Betrag |
|----------|--------|
| Bruttogehalt | 4.200,00 € |
| Lohnsteuer | -680,00 € |
| Solidaritätszuschlag | -37,40 € |
| Kirchensteuer | -61,20 € |
| Krankenversicherung | -330,00 € |
| Rentenversicherung | -365,40 € |
| Arbeitslosenversicherung | -50,40 € |
| **Nettogehalt** | **2.675,60 €** |

**Verweis:** [[Musterfirma GmbH]]
`,
	},
	{
		name: "Rechnung_Musterfirma_2026-02",
		pdf:  "Rechnung Musterfirma Februar 2026",
		md: `---
title: "Rechnung Musterfirma Februar 2026"
document_type: "invoice"
document_date: "2026-02-15"
correspondent: "Musterfirma GmbH"
person: "Max Mustermann"
status: "open"
due_date: "2026-03-15"
confidence: 96
tags:
  - rechnung
  - 2026
---

# Rechnung Musterfirma GmbH – Februar 2026

**Rechnungsnr.:** MF-2026-002
**Kunde:** Max Mustermann, Musterstraße 1, 12345 Beispielstadt

| Position | Beschreibung | Betrag |
|----------|-------------|--------|
| 1 | Webhosting Basic (12 Monate) | 120,00 € |
| 2 | Domainregistrierung example.de | 15,00 € |
| 3 | SSL-Zertifikat | 25,00 € |

**Nettobetrag:** 160,00 €
**MwSt. 19%:** 30,40 €
**Gesamtbetrag:** 190,40 €

**Zahlungsziel:** 15.03.2026
**Verweis:** [[Musterfirma GmbH]]

Webhosting Basic Monatsrechnung für Max Mustermann von Musterfirma GmbH.
Domainregistrierung example.de und SSL-Zertifikat sind im Paket enthalten.
Gesamtbetrag 190,40 Euro inklusive MwSt. 19 Prozent.
Zahlungsziel 15. März 2026 für die Rechnung MF-2026-002.
`,
	},
	{
		name: "Stromrechnung_Stadtwerke",
		pdf:  "Stromrechnung Stadtwerke",
		md: `---
title: "Stromrechnung Stadtwerke"
document_type: "invoice"
document_date: "2026-01-20"
correspondent: "Stadtwerke Beispielstadt"
person: "Erika Mustermann"
status: "open"
due_date: "2026-02-10"
confidence: 89
tags:
  - strom
  - rechnung
---

# Stromrechnung – Stadtwerke Beispielstadt

**Kundenkonto:** SW-987654
**Zählerstand:** 12.450 kWh

**Abrechnungszeitraum:** 01.01.2025 – 31.12.2025

| Position | Betrag |
|----------|--------|
| Grundpreis | 120,00 € |
| Arbeitspreis (3.200 kWh × 0,32 €) | 1.024,00 € |
| Netto | 1.144,00 € |
| MwSt. 19% | 217,36 € |
| **Gesamtbetrag** | **1.361,36 €** |

**Zahlungsziel:** 10.02.2026
**Verweis:** [[Stadtwerke Beispielstadt]]
`,
	},
	{
		// Missing document_date → triggers review queue.
		name: "Brief_Versicherung_Erika",
		pdf:  "Brief Versicherung AG an Erika",
		md: `---
title: "Brief Versicherung AG an Erika"
document_type: "letter"
correspondent: "Versicherung AG"
person: "Erika Mustermann"
status: "waiting_for_reply"
confidence: 72
tags:
  - versicherung
  - brief
---

# Schreiben der Versicherung AG

**An:** Erika Mustermann
**Betreff:** Aktualisierung der Versicherungspolice

Sehr geehrte Frau Mustermann,

hiermit bestätigen wir den Eingang Ihrer Police-Aktualisierung.
Wir benötigen noch folgende Unterlagen:

1. Kopie des aktuellen Einkommensnachweises
2. Ausgefülltes Formular „Lebenslage ändern"

Bitte senden Sie diese Unterlagen bis zum 31.03.2026 an uns.

Mit freundlichen Grüßen
Versicherung AG

**Verweis:** [[Versicherung AG]]
`,
	},
}

// notes is the set of 3–5 knowledge notes with wikilinks to documents.
var notes = []struct {
	name string
	md   string
}{
	{
		name: "Musterfirma_GmbH",
		md: `---
title: "Musterfirma GmbH"
created: "2026-01-01T00:00:00Z"
tags:
  - korrespondent
  - unternehmen
---

# Musterfirma GmbH

**Branche:** IT-Dienstleistungen & Webhosting
**Anschrift:** Firmenweg 42, 12345 Beispielstadt
**Kontakt:** info@musterfirma.de

## Verknüpfte Dokumente

- [[Rechnung Musterfirma Januar 2026]] – Webhosting-Rechnung
- [[Rechnung Musterfirma Februar 2026]] – Folgerechnung
- [[Gehaltsabrechnung Max Januar 2026]] – Arbeitgeber

## Notizen

Regelmäßiger Rechnungsempfänger. Die monatlichen Rechnungen für
Webhosting und Domain betragen ca. 190 € netto.
`,
	},
	{
		name: "Sparkasse_Beispielstadt",
		md: `---
title: "Sparkasse Beispielstadt"
created: "2026-01-01T00:00:00Z"
tags:
  - korrespondent
  - bank
---

# Sparkasse Beispielstadt

**Art:** Sparkasse
**BLZ:** 370 400 44
**Kontonummer:** 0532 0130 00

## Verknüpfte Dokumente

- [[Kontoauszug Sparkasse Januar 2026]] – Monatlicher Kontoauszug

## Notizen

Hauptgirokonto für den Haushalt. Gehaltseingänge und Daueraufände
werden über dieses Konto abgewickelt.
`,
	},
	{
		name: "Versicherung_AG",
		md: `---
title: "Versicherung AG"
created: "2026-01-01T00:00:00Z"
tags:
  - korrespondent
  - versicherung
---

# Versicherung AG

**Branche:** Private Krankenversicherung
**Kundennummer:** VAG-998877

## Verknüpfte Dokumente

- [[Versicherungspolice Erika Mustermann]] – Aktive Police
- [[Brief Versicherung AG an Erika]] – Aktualisierung angefordert

## Notizen

Erika Mustermann ist seit Juni 2025 versichert. Die jährliche
Vertragsverlängerung steht im Juni 2026 an.
`,
	},
	{
		name: "Hausstand_Mustermann",
		md: `---
title: "Hausstand Mustermann"
created: "2026-01-01T00:00:00Z"
tags:
  - projekt
  - haushalt
---

# Hausstand Mustermann

**Haushaltsmitglieder:**
- [[Max Mustermann]] – Hauptberuflich tätig bei Musterfirma GmbH
- [[Erika Mustermann]] – Versichert bei Versicherung AG

## Monatliche Fixkosten

| Kostenart | Betrag | Anbieter |
|-----------|--------|----------|
| Miete | 850 € | Vermieter |
| Strom | 95 € | [[Stadtwerke Beispielstadt]] |
| Internet/Hosting | 190 € | Musterfirma GmbH |
| Versicherung | 380 € | Versicherung AG |

## Steuern

- [[Steuerbescheid Max Mustermann 2025]] – Nachzahlung 1.250 €
- Steuererklärung 2026 in Vorbereitung
`,
	},
	{
		name: "Steuererklaerung_2026",
		md: `---
title: "Steuererklärung 2026"
created: "2026-01-01T00:00:00Z"
tags:
  - projekt
  - steuer
---

# Steuererklärung 2026

**Status:** In Vorbereitung
**Frist:** 31.07.2027

## Benötigte Unterlagen

- [[Gehaltsabrechnung Max Januar 2026]] – Einkommensnachweis
- [[Steuerbescheid Max Mustermann 2025]] – Vorjahr
- [[Kontoauszug Sparkasse Januar 2026]] – Bankbelege

## Notizen

Die Steuererklärung für 2026 ist ähnlich aufgebaut wie 2025.
Wichtig: Homeoffice-Pauschale und Werbungskosten dokumentieren.
`,
	},
}

// demoViews returns the 3 saved views for the demo vault.
func demoViews() []dbviews.View {
	return []dbviews.View{
		{
			ID:   "demo_open_invoices",
			Name: "Open invoices",
			Filters: []dbviews.Filter{
				{Key: "document_type", Operator: "equals", Value: "invoice"},
				{Key: "status", Operator: "equals", Value: "open"},
			},
			Sorts:   []dbviews.Sort{{Key: "document_date", Ascending: false}},
			Columns: []string{"_title", "document_date", "correspondent", "due_date", "confidence"},
		},
		{
			ID:   "demo_tax_2026",
			Name: "Tax 2026",
			Filters: []dbviews.Filter{
				{Key: "document_type", Operator: "equals", Value: "tax_assessment"},
			},
			Sorts:   []dbviews.Sort{{Key: "document_date", Ascending: false}},
			Columns: []string{"_title", "document_date", "person", "status", "confidence"},
		},
		{
			ID:   "demo_needs_review",
			Name: "Needs review",
			Filters: []dbviews.Filter{
				{Key: "status", Operator: "equals", Value: "needs_review"},
			},
			Sorts:   []dbviews.Sort{{Key: "confidence", Ascending: true}},
			Columns: []string{"_title", "document_type", "document_date", "confidence"},
		},
	}
}

// Init materialises the demo vault into dir.
// It refuses to overwrite a non-empty directory.
// It is idempotent: an empty directory is filled; a non-empty one is rejected.
func Init(dir string) error {
	// Resolve to absolute path.
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	// Check non-empty.
	entries, err := os.ReadDir(absDir)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read directory: %w", err)
	}
	if err == nil && len(entries) > 0 {
		return fmt.Errorf("refusing to overwrite non-empty directory: %s", absDir)
	}

	// Create directory structure.
	dirs := []string{
		absDir,
		filepath.Join(absDir, "documents"),
		filepath.Join(absDir, "notes"),
		filepath.Join(absDir, ".symdesk"),
		filepath.Join(absDir, "pdfs"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("create directory %s: %w", d, err)
		}
	}

	// Write documents.
	for _, doc := range documents {
		mdPath := filepath.Join(absDir, "documents", doc.name+".md")
		if err := os.WriteFile(mdPath, []byte(doc.md), 0644); err != nil {
			return fmt.Errorf("write document %s: %w", doc.name, err)
		}

		// Generate synthetic PDF.
		pdfData := generatePDF(doc.pdf, extractPDFLines(doc.md))
		pdfPath := filepath.Join(absDir, "pdfs", doc.name+".pdf")
		if err := os.WriteFile(pdfPath, pdfData, 0644); err != nil {
			return fmt.Errorf("write pdf %s: %w", doc.name, err)
		}
	}

	// Write knowledge notes.
	for _, note := range notes {
		notePath := filepath.Join(absDir, "notes", note.name+".md")
		if err := os.WriteFile(notePath, []byte(note.md), 0644); err != nil {
			return fmt.Errorf("write note %s: %w", note.name, err)
		}
	}

	// Write saved views.
	views := demoViews()
	viewsJSON, err := json.MarshalIndent(views, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal views: %w", err)
	}
	viewsPath := filepath.Join(absDir, ".symdesk", "views.json")
	if err := os.WriteFile(viewsPath, viewsJSON, 0644); err != nil {
		return fmt.Errorf("write views: %w", err)
	}

	return nil
}

// extractPDFLines pulls the body text from a markdown document for the PDF.
func extractPDFLines(md string) []string {
	lines := splitLines(md)
	var body []string
	inFrontmatter := false
	frontmatterCount := 0

	for _, line := range lines {
		trimmed := trimRight(line, "\r\n")
		if trimmed == "---" {
			frontmatterCount++
			if frontmatterCount <= 2 {
				inFrontmatter = frontmatterCount == 1
				continue
			}
		}
		if inFrontmatter {
			continue
		}
		if trimmed != "" {
			body = append(body, trimmed)
		}
	}
	return body
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func trimRight(s, cutset string) string {
	for len(s) > 0 && containsByte(cutset, s[len(s)-1]) {
		s = s[:len(s)-1]
	}
	return s
}

func containsByte(s string, c byte) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return true
		}
	}
	return false
}
