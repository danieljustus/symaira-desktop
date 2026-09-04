# ADR 0001: Externe Datensätze bekommen ein `dataset`-Primitive in symdesk — Brain speichert nicht, Brain gibt frei

> **Status**: **Angenommen** — D1–D7 entschieden am 2026-09-03. Die offenen Punkte
> O1–O3 werden bei der Umsetzung der jeweiligen Schritte beantwortet, nicht vorab.
>
> **Date**: 2026-09-03
> **Scope**: `internal/dataset` (neu), `internal/sidecar`, `internal/dbviews`, `internal/service/views.go`, `internal/ingest`, `internal/tools`, `VAULT.md`; nachgelagert `symaira-brain/internal/policy` (nur als Konsument)
> **Verwandt**: [`VAULT.md`](../../VAULT.md) §3, §5, §12 · [`symaira-brain/docs/adr/0001-brain-als-mcp-kontrollpunkt.md`](https://github.com/danieljustus/symaira-brain/blob/main/docs/adr/0001-brain-als-mcp-kontrollpunkt.md) D2/D3/D4 · [`symaira-brain/docs/ARCHITEKTUR.md`](https://github.com/danieljustus/symaira-brain/blob/main/docs/ARCHITEKTUR.md) §1, §3.2 · `../../../docs/repo-konsolidierung.md`

## Context

An das Ökosystem wird zunehmend strukturierte persönliche Fremddaten herangetragen:
Bankkonten über PSD2-APIs, Depot- und Kursverläufe als CSV, Amazon-Bestellhistorien
als CSV-Export, künftig Energie-, Fitness- und Verbrauchsdaten. Der Wunsch lautet:
*nicht nur den API-Key in einem Tresor ablegen, sondern die Daten selbst nutzbar
machen.*

Die Zugangsdaten-Hälfte ist gelöst — `symvault` mit `symvault://`-Referenzen und
`request_credential`. Die Live-Verbindung ist ebenfalls gelöst, aber außerhalb dieses
Ökosystems: ein lokaler, lesender Enable-Banking-MCP mit eigenem Security-Contract.
**Ungelöst ist die dritte Hälfte: die Daten.** Es gibt im gesamten Ökosystem keinen
Ort, an dem eine Tabelle mit 8.000 Zeilen liegen, typisiert bleiben und aggregiert
abgefragt werden kann.

Die Ausgangsfrage war eine Platzierungsfrage: **Brain oder Desk?** Sie ist nicht
unabhängig davon beantwortbar, was ein „Datensatz" im Vault-Vertrag überhaupt sein
darf — deshalb entscheidet diese ADR beides zusammen.

### Constraints (nicht verhandelbar)

1. **Brain ist speicherlos.** `symaira-brain/docs/ARCHITEKTUR.md` §1: *„Kein Speicher.
   Brain persistiert selbst keine Memories und keine Secrets."* Und E1 begrenzt Brain
   auf die Zustands-Cores. Ein Finanz-Core wäre der fünfte und bräuchte eine eigene
   Brain-ADR gegen §1.
2. **Markdown bleibt SSOT.** `VAULT.md` §12: *„Markdown is the single source of truth
   and there is no hidden database."* Ein Tabellen-Store darf diesen Satz nicht falsch
   machen.
3. **Kein neues Repo.** Die Konsolidierung ging 27 → 11 (`docs/repo-konsolidierung.md`).
   Ein `symaira-finance` wäre eine Rolle rückwärts, und Finanzen sind ohnehin nur einer
   von mehreren Datensatz-Typen (F7).
4. **Standalone-first.** Kein Compile-Time-Import zwischen Geschwister-Repos; Kopplung
   nur zur Laufzeit über MCP oder Subprozess.
5. **Der Vault liegt im Klartext auf Platte** und wird bei vielen Setups über iCloud
   oder Dropbox gesynct. Kontoumsätze sind eine andere Sensitivitätsklasse als Notizen.

## Verified Findings

Alles Folgende wurde am 2026-09-03 am Code geprüft, nicht aus der Dokumentation
übernommen.

### F1 — Eine CSV ist heute ein Dokument, kein Datensatz

`internal/ingest/internal/ingest/ingest.go:80` schickt `KindCSV` gemeinsam mit
`KindText` und `KindMarkdown` durch `extract.ReadTextKind`. Eine Bestellhistorie landet
damit als Fließtext im Notizkörper und im Volltextindex: keine Spalten, keine Typen,
keine Summe über ein Quartal, dafür maximales Suchrauschen. `documentformat` kennt
`KindCSV` und `KindXLSX` (`internal/documentformat/formats.go:19,24`) — aber nur als
Text-Lieferanten.

### F2 — Bases sind Views über Notizen, nicht über Zeilen

`Service.ViewsExec` (`internal/service/views.go:273`) löst über
`resolveViewDocuments` erst eine Dokumentliste auf und liest dann pro Dokument
`DB.GetProperties(d.Path)`. **Eine Zeile ist eine Notiz.** Eine Bank-CSV mit 8.000
Buchungen würde 8.000 Markdown-Dateien brauchen, um in einer Base sichtbar zu werden.

Gleichzeitig ist `resolveViewDocuments` ein sauberer Präfix-Switch über
`view.Source` — `""`, `tag:`, `notebook:`, sonst Ordnerpfad
(`internal/service/views.go:190–270`, dokumentiert an `dbviews.View.Source`,
`internal/dbviews/views.go:79`). Das ist eine offene Erweiterungsstelle, kein Umbau.

Ebenso vorhanden und wiederverwendbar: `dbviews.PropertyConfig`
(`internal/dbviews/views.go:89`) mit `type`/`label`/`options`/`default` und die
Layouts `table|board|calendar|gallery|timeline|list` samt `Filters`, `FilterGroup`,
`Sorts`, `Computed` (Rollups). **Die Tabellen-UI existiert bereits** — ihr fehlt nur
eine Zeilenquelle, die keine Notiz ist.

### F3 — Der Sidecar ist der bereits akzeptierte Ort für abgeleiteten Zustand

`sidecar.VaultDir` (`internal/sidecar/db.go:24`) ist ausdrücklich *„the canonical
per-vault directory used for rebuildable sidecar state"*, pro Vault über einen
SHA-256-Digest isoliert, auf `corekit/sqlitekit` aufgebaut, mit zehn versionierten
Migrationen (`internal/sidecar/migrations/001…010`). `OpenForVault` begründet die
Isolation explizit damit, dass ein Vault keine Zeilen eines anderen sehen darf.

Der Retrieval-Index nutzt genau diesen Trick bereits: er ist Zustand, der nicht SSOT
ist, weil er jederzeit aus den Vault-Dateien neu gebaut werden kann. Constraint 2
bleibt dadurch wahr.

### F4 — Der Präzedenzfall steht schon im Repo

`internal/contacts` macht exakt das hier vorgeschlagene Muster und ist akzeptiert:
externe strukturierte Daten (`internal/contacts/internal/domain/importer/csv.go`,
`vcard.go`) → getypter Store → eigene SQLite-Migrationen
(`internal/contacts/internal/storage/sqlite/migrations/0001…0007`, darunter
`0005_import.sql`) → Service- und MCP-Oberfläche. „Strukturierte Fremddaten in symdesk"
ist damit keine neue These, sondern eine bestehende, die bisher nur einen einzigen
Datentyp bedient.

### F5 — Der Vault kann die Rohdatei bereits sicher aufnehmen

`VAULT.md` §5 „Vault Asset Writer" (`symdesk asset store`, MCP `desk_asset_store`,
`vault.StoreAsset`): auf den Vault-Root begrenzt, `..` abgewiesen, kollisionssichere
Benennung `base-2.ext`, Basisnamen-Sanitisierung, atomare Writes über Temp+Rename.
Für die Rohdatei muss nichts gebaut werden.

### F6 — Brain kann symdesk **heute schon** gaten; ihm fehlt nur das Signal

Die frühere Annahme, `profile.Servers` sei ein festes Vier-Felder-Struct, ist überholt.
Nach Brain-ADR 0001 (D2/D4) ist `profile.Servers` ein
`map[string]ServerConfig` (`symaira-brain/internal/profile/profile.go:67`): jeder Alias
mit `command`/`args` oder `url` ist ein Foreign Server mit `access = "read"|"write"`,
`tools_allow`/`tools_deny`, `tools_read`/`tools_write` und Audit-Eintrag
(`symaira-brain/internal/policy/foreign.go`). **Ein `symdesk mcp` unter einem
Brain-Profil ist ohne eine Zeile neuen Brain-Code möglich.**

Der Haken sitzt auf unserer Seite. `policy.EvaluateForeign` klassifiziert ein Tool
nach: expliziter `tools_read`/`tools_write`-Eintrag → sonst `readOnlyHint: true` →
sonst `default_write`. `corekit/mcpserver` unterstützt die Annotation seit Langem und
warnt in genau diesem Sinn (`mcpserver/mcpserver.go:77`: *„Every registered tool should
set ReadOnlyHint explicitly: a missing hint is read by downstream consumers as
'write', the least-trusted classification"*). symdesk kennt die Information sogar
selbst — `tools.Tool.ReadOnly` (`internal/tools/tools.go:37`) steuert bereits
`Registry.Enabled(allowWrite)` — **emittiert sie aber nicht als
`annotations.readOnlyHint`.** Eine Suche über `internal/mcp/` findet keinen einzigen
Treffer.

Konsequenz heute: Brain stuft *jedes* der 31 `desk_*`-Tools als schreibend ein. Ein
Profil mit `access = "read"` versteckt damit auch `desk_search` und `desk_ls` — die
Freigabe ist faktisch alles-oder-nichts, und der feingranulare Weg, den diese ADR für
Datensätze braucht, ist blockiert.

### F7 — Es gibt mehr als einen Produzenten

Die Ökosystem-Regel lautet, erst bei bewiesenem zweitem Konsumenten zu abstrahieren.
Sie ist erfüllt: `internal/contacts` (getypte Fremddaten, F4), die Traffic-/DSL-Reihen
aus `symfritz`, und die geplante Computer-Activity-History sind drei bestehende
Kandidaten für dasselbe Primitive. Finanzdaten sind der vierte, nicht der einzige.

## Entscheidungen

**D1 — Der Speicherort ist symdesk, nicht Brain und nicht symmemory.**
Desk besitzt bereits Ingest, Watcher, Rules, Asset-Storage, Sidecar, Retention, Export,
Permissions, die Tabellen-UI und den menschlichen Konsumenten. Brain scheidet über
Constraint 1 aus; `symmemory` scheidet fachlich aus (siehe A1).

**D2 — SSOT bleibt die Datei; die Tabelle ist abgeleitet.**
Die Rohdatei liegt als gewöhnliches Vault-Asset unter `datasets/<slug>/<YYYY-MM-DD>.csv`
(F5). Die Zeilen werden in den Sidecar materialisiert und sind aus der Datei jederzeit
neu erzeugbar (F3). Löscht man den Sidecar, geht nichts verloren. Constraint 2 bleibt
wörtlich wahr.

**D3 — Das Handle ist eine Notiz.**
`datasets/<slug>.md` mit `type: dataset` trägt Quelle, Spaltenschema, Refresh-Kommando,
abgedeckten Zeitraum und Sensitivitätsklasse im Frontmatter. Das Spaltenschema
verwendet `dbviews.PropertyConfig` unverändert weiter (F2) — kein zweites Typsystem.
Dadurch ist ein Datensatz wikilink-bar, taggbar, in Notebooks referenzierbar, von
Retention-Regeln selektierbar und im Backup enthalten, ohne Sonderweg.

**D4 — Views bekommen eine neue Quelle `dataset:<slug>`.**
Erweiterung des Präfix-Switch in `resolveViewDocuments`/`ViewsExec` (F2), sodass eine
Zeile eine Datensatzzeile sein darf statt einer Notiz. `table`, `timeline`, `calendar`,
Filter, Sorts und `Computed`-Rollups gelten unverändert.

**D5 — Produzent und Speicher werden getrennt.**
Ein Connector (Enable-Banking-MCP, Broker-Export, künftig `symfritz`) liefert Zeilen an
`dataset sync` und persistiert **nicht selbst**. CSV-Drop in einen beobachteten Ordner
und API-Sync landen in derselben Tabelle mit derselben Provenance. Ein Speichermodell,
viele Lieferanten — das ist der eigentliche Hebel dieser ADR.

**D6 — Brains Rolle ist ausschließlich das Exposure-Gate.**
Kein vierter Core, keine neue Brain-ADR. Der bestehende Foreign-Server-Weg (F6) trägt
den Fall vollständig: ein Profil `finanzen` sieht `desk_dataset_*` lesend, das
Arbeitsprofil sieht es gar nicht. Die einzige Bringschuld liegt bei symdesk — die
`readOnlyHint`-Annotation (F6). Sie ist unabhängig vom Datensatz-Thema wertvoll und
wird deshalb als eigenständiger, vorgezogener Schritt geführt.

**D7 — Kein neues Repository und keine neue Binary.**
Constraint 3.

## Verworfene Alternativen

| # | Alternative | Warum verworfen |
|---|---|---|
| A1 | Zeilen in `symmemory` ablegen | Das ist ein *semantischer* Store mit Embeddings, Scopes und staged/promote-Review. 8.000 fast identische Buchungszeilen als Vektoren zerstören die Recall-Qualität für die Fakten, für die der Store gebaut wurde. Zusätzlich: Brain ist speicherlos (Constraint 1) |
| A2 | Ein vierter/fünfter Zustands-Core in Brain | Verletzt ARCHITEKTUR.md §1 und E1 direkt. Und §3.2 ordnet den Fall bereits zu: *„Feature mit menschlicher Oberfläche → Desktop"* — Finanzdaten werden primär vom Menschen gelesen |
| A3 | Neues Repo `symaira-finance` | Constraint 3; und F7 zeigt, dass Finanzen nur ein Datensatztyp von mehreren sind. Ein finanzspezifisches Repo würde die Abstraktion an der falschen Stelle festnageln |
| A4 | CSV weiter als Dokument behandeln (Status quo) | F1. Der Status quo ist die schlechteste Option: er verliert die Struktur *und* verschmutzt den Volltextindex |
| A5 | Rohes SQL über ein MCP-Tool freigeben | Nicht auditierbar, nicht begrenzbar, und Brains Foreign-Filter könnte ein `dataset_query`-Tool mit beliebigem SQL nicht sinnvoll als lesend klassifizieren. Stattdessen gebundene Filter/Aggregate mit Row-Cap |

## Konsequenzen

**Positiv**
- Bank-, Depot- und Bestelldaten werden aggregierbar, ohne dass ein neues Produkt
  entsteht: ein Paket, eine View-Quelle, wenige MCP-Tools.
- Der Enable-Banking-MCP muss nichts über Speicherung wissen und bleibt lesend (D5).
- Brain wird endlich für das benutzt, wofür es gebaut wurde, statt für Speicherung.
- Die `readOnlyHint`-Lücke (F6) wird nebenbei geschlossen und verbessert *jede*
  Brain-Freigabe von symdesk, nicht nur Datensätze.

**Kosten und Risiken**
- `VAULT.md` braucht einen neuen Abschnitt und einen `contract_version`-Bump (5 → 6).
  Der Swift-Client muss den neuen `type: dataset` tolerieren, bevor er ihn darstellt.
- **Identitätsdrift.** Desk ist der Markdown-Vault-Workspace. Ein Tabellen-Store zieht
  in Richtung „Alles-Speicher" — das ist die These, an der `symaira-hub` gestorben ist.
  D2 ist genau deshalb so geschnitten, dass ein Datensatz ein *abgeleiteter Index über
  Vault-Dateien* bleibt und nicht ein zweiter Wahrheitsort wird. Diese Grenze ist die
  Abnahmebedingung für jeden Folgeschritt.
- **Sensitivität.** Constraint 5. `internal/retention` und `internal/permissions`
  existieren, sind aber optional. Für Datensätze müssen Sensitivitätsklasse und
  Aufbewahrung verpflichtend werden, nicht empfohlen.

## Offene Fragen

**O1 — Persistenz gegen die Masking-Zusage.**
Der Enable-Banking-Betrieb sagt heute ausdrücklich zu, dass rohe Transaktionshistorien
nicht persistiert werden, IBANs maskiert und Gegenkonten weggelassen werden. Lokale
Persistenz hebt diese Zusage auf. Das ist eine bewusste Entscheidung, keine
Nebenwirkung — sie muss vor dem ersten Bank-Sync getroffen und im Dataset-Schema
festgehalten werden (welche Felder überhaupt gespeichert werden dürfen). Bis dahin ist
der CSV-Weg der einzige erprobte.

**O2 — Verschlüsselung at rest.**
Ob ein als `sensitive` markierter Datensatz im Sidecar verschlüsselt liegen muss, oder
ob Dateisystem-Verschlüsselung plus Ausschluss aus dem Sync genügt, ist offen. Die
Antwort hängt an O1.

**O3 — Zeilenidentität und Dedupe.**
Kontoumsätze werden nachträglich korrigiert, und CSV-Exporte überlappen sich in ihren
Zeiträumen. Ein Sync muss idempotent sein. Ob der Schlüssel aus einem stabilen
Provider-Feld kommt oder aus einem Zeilen-Hash abgeleitet wird, entscheidet sich am
ersten realen Datensatz — nicht am Reißbrett.
