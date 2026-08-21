---
profile: behoerde
lang: de
title: "Barrierefreiheits-Testdokument"
date: 2026-07-28
sender:
  name: "Stadt Musterstadt — Fachbereich Digitalisierung"
  address:
    - "Rathausplatz 1"
    - "12345 Musterstadt"
recipient:
  name: "Aktenzeichen INTERN"
  address:
    - "Nur für den Dienstgebrauch"
infoblock:
  Unser Zeichen: "DIG-2026-00075"
  Bearbeiter: "A. Beispiel"
betreff: "Testdokument für die CI-Validierung von PDF/UA-1"
pdf:
  standard: [a-2a, ua-1]
---

Dieses Dokument ist eine **Testvorgabe (Fixture)** für die kontinuierliche
Validierung der Barrierefreiheit. Es enthält bewusst Merkmale, die in den
übrigen Beispieldokumenten nicht vorkommen: eine mehrstufige, nicht
überspringende Überschriftenhierarchie und eine Tabelle mit Kopfzeile.

# Sachverhalt

Die bisher validierten Beispieldokumente enthalten weder Tabellen noch eine
mehrstufige Überschriftenstruktur. Die PDF/UA-1-Prüfung in der CI konnte
dadurch bestanden werden, ohne diese Konstrukte tatsächlich zu prüfen. Die
folgende Inline-Gleichung prüft die barrierefreie Ausgabe eines kurzen Ausdrucks:
$E = m c^2$.

Der Block testet zusätzlich eine mehrgliedrige LaTeX-Gleichung:

$$
\int_0^1 x^2 \, dx = \frac{1}{3}
$$

## Feststellung

Für eine belastbare Prüfung sind dokumentierte Merkmale erforderlich, die der
Validator auch auswerten kann.

### Betroffene Merkmale

- Überschriftenhierarchie über mehrere Ebenen (H1 → H2 → H3)
- Tabellen mit Kopfzeile
- Bilder mit Alternativtext (wird an `image(alt:)` weitergereicht)
- Inline- und Block-Mathematik (LaTeX-Quelle wird als PDF/UA-Alternativtext gesetzt)
- Dokumentensprache und -titel (bereits abgedeckt)

![Beispielhafte blaue Beispielgrafik mit weißem Rahmen](logo-example.png)

## Bewertung

Die nachfolgende Tabelle fasst den Prüfumfang zusammen.

| Merkmal | Im Fixture enthalten | Bisher in CI geprüft |
| --- | --- | --- |
| Überschriftenhierarchie (H1–H3) | Ja | Nein |
| Tabelle mit Kopfzeile | Ja | Nein |
| Dokumentensprache (`lang`) | Ja | Ja |
| Dokumententitel (`title`) | Ja | Ja |

# Maßnahme

Dieses Dokument wird in der CI mit dem Profil `behoerde` gerendert und mit
veraPDF gegen PDF/A-2a und PDF/UA-1 validiert. Ein Validierungsfehler gilt als
Befund und wird als separates Issue dokumentiert — die Prüfung wird nicht
stillschweigend grün geschaltet.

Mit freundlichen Grüßen
i. A. A. Beispiel
