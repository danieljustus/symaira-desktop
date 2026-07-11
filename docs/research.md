Claude Session (24.06.2026; ca. 15:30 Uhr)

Ich: "mein Traum ist es eine App zu haben, die folgende in einer vereint: "Paperless-ngx + Obsidian (inkl. iCloud) + Notion AI + Excel/LibreOffice + VSCode"
ich habe mal zwei projekte in diese Richtung angefangen: "'/Users/daniel/Dev/alte Projekte?/Mosaic.zip'" und "https://github.com/danieljustus/OpenPaiper".

Prüf mal bitte ob sowas Sinn macht für das Symaira Ökosystem (in passender Programmiersprache natürlich) und ob mein Projektidee realistisch ist."

Claude: "Ich schaue mir das erstmal konkret an, bevor ich urteile. Lass mich die vorhandenen Bausteine sammeln: das Symaira-Verzeichnis, die Memory-Dateien zum Ökosystem, das Mosaic-Projekt und OpenPaiper.

Gutes Bild vom Ökosystem. Jetzt die beiden Altprojekte und den ECOSYSTEM-Kontext. Mosaic.zip ist 246 MB — ich liste nur, statt zu entpacken.

Sehr aufschlussreich. OpenPaiper (Rust) deckt schon ingest+OCR+Hybrid-Suche+RAG+Memory-Graph+MCP ab — also exakt das, was symseek/symmemory/symfetch im Symaira-Stack in Go bereits tun. Mosaic ist ein Tauri/React-Notiz-/Projektmanagement-Versuch. Und ECOSYSTEM.md sagt explizit: kein Monorepo, keine harte Kopplung, Rust bewusst ausgeschlossen. Genau diese Spannung muss ich sauber prüfen.

Ich ziehe jetzt die relevanten Artefakte einmalig (Mosaic-Docs aus dem Zip, OpenPaiper-Schlüsseldateien), damit die Analyse auf echtem Code fußt.

OpenPaiper ist deutlich weiter als „angefangen": Rust-Workspace mit 21 Postgres-Migrations (Dokumente, Tags, Chunks, Memories, Klassifikation, Entities/Relations, Chat-Sessions, pgvector), Adaptern für PDF/Excel/DOCX/OCR, axum-Server + MCP. Mosaic ist die andere Hälfte: Tauri+React mit ProseMirror-Editor (Backlinks, Transclusion, Math), Notion-Datenbank-View, Graph-View, Spreadsheet-Editor und AI-Dock. Du hast also beide Hälften des Traums schon gebaut — beide in Rust/Tauri.

Das ist genug Substanz für eine fundierte, mehrperspektivische Bewertung statt einer Bauchmeinung. Ich lasse das jetzt von mehreren Agenten parallel auf echtem Code prüfen — inkl. adversarialer Gegenprobe zur Architektur- und Markt-Realität — und synthetisiere danach.

Schema-Definitionen fehlten (JS hebt `const` nicht hoch). Ich ergänze sie und starte den Lauf neu.

I need to read the script file first.

Now I'll insert the schema definitions before `phase('Recon')`.

Schemas defined. Relaunching the workflow.

Läuft. Sechs Agenten arbeiten jetzt am echten Code (OpenPaiper wird geklont, Mosaic-Quelltext entpackt, Symaira-Repos gelesen), dann die adversariale Verdict-Runde. Ich melde mich mit der Synthese, sobald der Lauf durch ist — du kannst mit `/workflows` live zuschauen.

Der Lauf ist durch (6 Agenten, ~380k Tokens, echte Tests gelaufen — u.a. 193/193 Tests in OpenPaiper grün). Ich lese das vollständige Ergebnis, bevor ich synthetisiere.

Alle sechs Agenten sind durch und konvergieren stark. Ich habe die vollständigen Ergebnisse — hier ist die Synthese.

Kurz vorweg eine wichtige Korrektur am Befund: **Mosaic ist gar nicht Firebase/Cloud-basiert** (das war nur ein übriggebliebenes `firebase-debug.log` von einem CLI-Experiment) — der echte Code ist **lokal SQLite + Ollama**. Damit passt Mosaic philosophisch sogar zu Symaira („local-first, kein Pflicht-Cloud-Account"). Der Editor ist außerdem BlockNote, nicht Tiptap.

Lass mich das Architektur-Bild der Empfehlung visualisieren, dann kommt die Bewertung.

'/Users/daniel/Dev/Symaira Dev/symaira-desk/docs/Architektur-Visualisierung.png'

Hier die Bewertung — die drei Perspektiven (Architektur-Skeptiker, Markt-Realist, konstruktiver Architekt) sind unabhängig zum selben Schluss gekommen, das gibt mir hohe Sicherheit.

## Die zwei Antworten vorweg

**1. Macht sowas Sinn fürs Symaira-Ökosystem?** Die *Fähigkeiten* ja — als *eine monolithische App* nein. Eine „ONE app, die alles vereint" ist das wortwörtliche Gegenteil dessen, was deine eigene ECOSYSTEM.md als Kern festschreibt: *„ausdrücklich NICHT als Monorepo, harte Laufzeit-Kopplung oder globaler Pflicht-Account."* Der Traum ist nicht falsch — er ist nur als **Architektur** formuliert, obwohl er ein **UX-Wunsch** ist („eine Oberfläche", nicht „ein Binary").

**2. Ist die Projektidee realistisch?** Als *Markt-Konkurrent*, der Paperless **und** Obsidian **und** Notion **und** Excel **und** VSCode ersetzt: nein, solo nicht — die zwei Säulen, die Excel und VSCode *definieren* (eine echte Formel-/Recalc-Engine und ein echter Code-Editor mit LSP), stehen bei ~0 % und sind je für sich Mehrjahresprojekte. Als **persönliches, agent-natives Wissens-Cockpit**, das die schweren Teile *integriert statt nachbaut*: ja, sehr realistisch — und für deinen eigenen Workflow nützlicher als jeder Einzel-Konkurrent.

## Was du schon hast (das ist der eigentliche Befund)

Komponiert man die existierenden Assets, kommst du **~45 %** des Traums — aber sehr ungleich verteilt:

| Säule | Abdeckung | Worüber | Lücke |
|---|---|---|---|
| **Paperless** (OCR/Archiv) | 🟡 ~65 % | symseek (Index) + OpenPaiper-Pipeline (Referenz) | OCR fehlt *im Go-Stack* (seek: „no OCR in scope") |
| **Obsidian** (MD/Graph/Sync) | 🟡 ~45 % | symmemory (Graph, Obsidian-Importer, iCloud-Pfad) + Mosaic-UI | iCloud-*Sync*-Engine, In-Policy-Editor |
| **Notion AI** (DB + AI) | 🟡 ~65 % | symmemory (RAG, Entity-Graph, PII) + symseek + symfetch | strukturierte DB-Engine (typed properties/views) |
| **Excel** | 🔴 ~5 % | überall nur Text-Extraktion, Mosaic-Grid zählt Formeln, *berechnet keine* | Formel-Engine = ~100 % to-build |
| **VSCode** | 🔴 ~5 % | vibecoder *orchestriert* Agenten, vault-Plugins *injizieren* nur Secrets | echter Editor/LSP = ~100 % to-build |

Das starke Rückgrat ist die **lokale KI/Wissens-Schicht** (memory + seek + fetch) — die ist heute schon „besser als Notion AI" für deinen eigenen Kram und voll in-policy.

## Der Knackpunkt: Rust

Beide Vorprojekte sind erstaunlich reif — kein Wegwerf-Code:
- **OpenPaiper**: ~22k Zeilen Rust, **193/193 Tests grün** (lokal verifiziert), Produktions-CI, dual Postgres+pgvector/SQLite, echte RRF-Hybrid-Suche, bitemporaler Memory-Store, Knowledge-Graph, vollständige OCR-Ingest-Pipeline, MCP-Server. Die kompletteste Realisierung der Paperless+RAG-Zweidrittel.
- **Mosaic**: ~23,5k Zeilen, alpha, **lokal SQLite+Ollama** (nicht Firebase!), BlockNote-Editor mit Callout/Math/Transclusion/Backlinks, GraphView, Notion-DatabaseView, AiDock-RAG, funktionierende Tesseract-Ingest.

Aber: Beide stehen auf dem **bewusst ausgeschlossenen Stack** (Rust/Tauri — genau wofür OpenShelf gekillt wurde). Ehrliche Reuse-Mathematik: **~0 % Code direkt übernehmbar, ~80 % Design übernehmbar.** Die „differ-clause" (Apps dürfen vom CGO-freien CLI-Stack abweichen) ist eine **Swift-Ausnahme für native Apps, kein Rust-Wiedereinlass**.

**Eine wichtige Ausnahme:** Mosaics **React/TS-Frontend** verletzt die Policy *nicht* (die regelt CLI/MCP/Cloud=Go, native=Swift — verbietet kein Web-UI). Nur **Tauri als Shell** kollidiert. Das Frontend ist also echter, portierbarer Donor-Code.

## Empfehlung: `symaira-desk` (siehe Diagramm oben)

Statt Monolith **ein neues Symaira-Tool** im exakt schon bewährten **vibecoder-Muster** (Go-Binary serviert eingebettete React-SPA über localhost). Es besitzt nur drei Dinge selbst (Vault, SQLite-Index, **die einzig wirklich fehlende In-Policy-Fähigkeit: OCR/Ingest**), komponiert alles andere zur Laufzeit aus seek/memory/fetch/vault, und **delegiert die drei Tech-Fallen statt sie zu bauen**:
- **Tabellen** → Datei in LibreOffice/Excel öffnen, beim Speichern re-ingesten (Post-v1 optional HyperFormula im WebView)
- **Code** → vibecoder + terminal + vault-Editor-Plugins (Symairas Strategie ist *in Editoren reinstecken*, nicht ersetzen — Säule 5 ganz streichen)
- **iCloud-Sync** → Vault im iCloud/Obsidian-Ordner ablegen, das OS synct (keine eigene Sync-Engine)

**Der Differentiator** gegen alle fünf Incumbents: *agent-native + MCP-first*. Dieselbe Go-Service-Schicht ist gleichzeitig CLI, GUI **und** MCP-Server — Mensch und KI-Agent arbeiten auf **einem Markdown-Vault über identische Contracts**. Deine Dokumente werden eine erstklassige Tool-Surface, die jeder Agent lesen/durchsuchen/erinnern/schreiben kann — nicht fünf Silos, die eine KI nur screen-scrapen kann. 100 % local-first, kein Pflicht-Account.

## Die ersten drei Schritte (konkret)

1. **Spine beweisen:** `symaira-desk` als Go-Modul auf corekit anlegen, `web/embed.go` + `internal/server` von **symaira-vibecoder** kopieren, `symdesk serve` hochziehen, das MCP-Server + localhost-HTTP + Platzhalter-SPA aus *einem* Binary liefert. Erst die CLI+MCP+GUI-Achse, dann Features.
2. **Frontend ernten:** `Editor.tsx` (BlockNote), `GraphView.tsx`, `CommandPalette`, `DatabaseView` aus `/tmp/mosaic-src` nach `symaira-desk/web` ziehen, Tauri-`invoke()` → HTTP-`fetch()` tauschen, unter Vite in `dist/` bauen.
3. **OCR/Ingest in Go bauen** (der neue, wertvollste Baustein): Watched-Inbox (fsnotify) → tesseract+pdftoppm-Shellout → Shadow-`.md`+Frontmatter → an seek zum Indexieren. `adapters/ocr.rs` + `pipeline/orchestrator.rs` aus `/tmp/openpaiper-src` als Zeile-für-Zeile-Spec.

---

**Bottom line:** Dein Traum ist zu ~45 % schon gebaut, aber an der falschen Stelle (Rust) und im falschen Format (zwei Monolithen). Realistisch ist nicht „5 Produkte ersetzen", sondern „**eine Schale über deine bereits funktionierenden Go-Kerne, die das Schwere delegiert**" — und das ist sogar das, was ECOSYSTEM.md §1 ausdrücklich segnet.

Soll ich den nächsten Schritt machen — z. B. das `symaira-desk`-Repo-Scaffold (Schritt 1) tatsächlich anlegen, oder ein ADR/Issue-Set für die Entscheidung „Komposition-Shell statt Monolith, Rust verworfen" schreiben?"


Ich: "ok dann lass uns planen welche weitere symaira Bausteine noch fehlen oder was noch bei bestehenden ergänzt werden sollte um am Ende alle zusammen in symaira-desk vereinen zu können."

Claude: "Klarer nächster Schritt. Bevor ich plane, will ich den *aktuellen* MCP-/Capability-Stand jedes bestehenden Kerns sauber inventarisieren — sonst plane ich gegen Annahmen statt gegen echten Code. Ich lasse das tool-für-tool parallel prüfen (was kann der Kern heute, was braucht `symaira-desk` von ihm, was fehlt konkret), plus zwei adversariale Checks: Abhängigkeits-/Reihenfolge und Philosophie-Verstöße (verwandelt eine Ergänzung einen Standalone-Kern in eine desk-Abhängigkeit?). Danach baue ich den Fahrplan.

Läuft — 5 Inventar-Agenten lesen jetzt jeden Kern im echten Code, dann die zwei Critique-Agenten (Reihenfolge + Philosophie-Verstöße). Ich melde mich mit dem konsolidierten Fahrplan, sobald der Lauf durch ist.

The build-plan workflow completed (7 agents, ~524k tokens, all reading real code). The result is large — let me read the full output to get every inventory card and both critiques before synthesizing.

The structured extract is saved. Let me read it fully.

Strong cards — and they surfaced one real disagreement (Notion-DB as its own `symbase` repo vs. folded into desk as `internal/dbviews`). Let me read the two critique agents, who should resolve exactly that.

The sequencer resolved the fork cleanly (fold Notion-DB into desk as `internal/dbviews`, reject `symbase` for v1). Now the philosophy/coupling check.

Vollständiges Bild. Die zwei Critique-Agenten sind sich weitgehend einig; ich habe die eine Sequenzierungs-Spannung aufgelöst (dazu unten). Hier der konsolidierte Bauplan.

## Kernergebnis: nur **2 neue Repos** — alles andere ist corekit-Extraktion + additive Features

Der Traum braucht keinen Wust neuer Tools. Er braucht **eine Schale + einen Scanner**, ein paar geteilte corekit-Bausteine, und kleine, *standalone-sichere* Ergänzungen an dem, was du schon hast.

### A) Neue Bausteine (das, was wirklich fehlt)

| Baustein | Was | Eigenes Repo? | Begründung |
|---|---|---|---|
| **symaira-desk** (`symdesk`) | Die Kompositions-Schale: Markdown-Vault-Contract + **eine** SQLite-Sidecar + embedded React-SPA + Runtime-Komposition | ✅ neues Repo | Ist der neue Kern selbst |
| **symaira-ingest** (`symscan`) | OCR/Ingest: tesseract+pdftoppm-Shellout, fsnotify-Watcher, Worker-Pool, Klassifikation → Markdown+Frontmatter in den Vault | ✅ neues Repo | Eigener Lifecycle (Dauer-Watcher/Worker), eigenständig wertvoll (Paperless-Ersatz „Datei rein → durchsuchbare Notiz"), desk komponiert es per PATH-Probe |
| **Notion-DB** (typed properties/views) | `internal/dbviews` **in desk** — Projektion über Frontmatter → `file_properties` → gespeicherte Views (Filter/Sort) | ❌ in desk gefaltet | **`symbase` für v1 verworfen.** Mosaic beweist: das ist eine *dünne Projektion über Markdown-Frontmatter*, kein zweites DB-Produkt — hat ohne desks Vault keine eigenständige Bedeutung. Formeln/Rollups vertagt |
| **Preview/Thumbnails** + **Sync-Conflict-Helper** | `internal/preview` (pdftoppm-Erstseiten-PNG) + `internal/conflict` (iCloud-„conflicted copy"-Erkennung) | ❌ in desk | UI-gekoppelt, keine eigenständige Bedeutung; `symaira-render` erst bei zweitem Konsumenten |

### B) corekit-Extraktionen (einmal teilen statt 2–3× kopieren)

| Modul | Priorität | Was | Beweis aus dem Code |
|---|---|---|---|
| **ollamakit** | **MUST** | CGO-freier Ollama-Embeddings-Client (LRU-Cache, Dim-Autodetect, local-hash-Fallback) | 2 divergente Kopien existieren (seek 541 Z., memory 228 Z.) |
| **webuikit** | **MUST** | Embedded-SPA-Host: `//go:embed all:dist` + SPA-Fallback-Handler + SSE-Bus + loopback-Bind | vibecoder-Host liegt *unexported* in `internal/server` — desk darf nicht importieren |
| **piikit** | should | PII-Guard (Regex + Luhn + Redact) aus memory `internal/security/pii.go` | self-contained, OCR-Pipeline braucht dieselbe Redaktion |
| **toolprobe** | should | Geteilter Runtime-Detect-Helper (PATH + `--version` + Capability-Ping) | kodifiziert die loose-coupling-Regel einmal |
| **searchkit** | later | Reine BM25+RRF(k=60)-Primitives | seek + memory haben je eigenes RRF |

### C) Ergänzungen an bestehenden Tools (alle additiv, halten das Tool standalone)

| Tool | Ergänzungen |
|---|---|
| **symseek** | `index_content(source,text)` über MCP (wrappt vorhandenen `IndexStdin`) · **JSON statt formatiertem String** über MCP · char/line-Offsets an Chunks (jump-to-source) · path-scoped Suchfilter · `get_document_status` · **stabile Chunk-IDs** (content-hash statt `uuid.New` bei jedem Reindex) |
| **symmemory** | entity↔entity-Relationen (Mini-Triple-Store) · `graph_neighbors` → `{nodes,edges}` für GraphView/Backlinks · as-of-bitemporal-Lesequery |
| **symfetch** | `--format md-frontmatter` (YAML: source_url/title/fetched_at) · reichere Provenienz (author/date/canonical aus vorhandenen JSON-LD/og-Islands) |
| **symvault** | `symvault resolve <ref>` One-Shot-Subcommand · read-secrets-Token-Preset für desk |

> **Wichtig:** Keine dieser Ergänzungen macht ein Tool von desk abhängig — es sind echte Verbesserungen *für das jeweilige Tool selbst*. Das ist die Symaira-native Form.

## Die 4 nicht verhandelbaren Leitplanken (vom Philosophie-Check)

1. **Vault bleibt IMMER Klartext-Markdown** auf der Platte (sonst können Obsidian + iCloud nicht lesen/synchronisieren). symvault ist *nur* der orthogonale **Secrets**-Store. Per-Notiz-Verschlüsselung ist ein *späteres* desk-Opt-in, nie Default.
2. **seek besitzt ALLE Embeddings** für indizierte Inhalte. desk und scan pushen **nur Text** (`index_content`/`index_document`), **nie Vektoren** — sonst triggert seeks fataler mixed-embedding-space-Guard (`retrieval.go:34` killt *alle* Queries). Das entschärft die Falle sofort, ohne ollamakit zu blocken.
3. **desk läuft mit null Geschwistern**: eingebauter FTS5-Fallback über die eigene Sidecar. seek *upgradet*, gatet nie.
4. **scan schreibt Markdown+Frontmatter und hört auf.** Indizieren/Embedden ist seeks Job, wenn seek da ist.

## Fahrplan (MVP → v0.5 → v1)

**MVP — `symdesk` v0.1 (nutzbar ohne jedes Geschwister-Tool)**
- desk-Repo: Go-CLI + `symdesk serve` (vibecoder-Host **kopiert**, nicht importiert) + `symdesk mcp`
- Sidecar via corekit/sqlitekit: `files + file_properties + links + view_presets`
- **Vault-Contract einfrieren** (Ordnerlayout, Frontmatter-Schema, Dateinamen) — alles Nachgelagerte hängt daran
- Minimal-SPA: Vault-Browser + BlockNote-Editor (Mosaic-Donor) + CommandPalette
- Read-Path **ohne seek-Änderung**: `symseek index <file>` bei Save, `symseek search --json` bei Query, naiver Fallback wenn seek fehlt
- `symscan` v0.1: One-Shot-OCR (tesseract+pdftoppm, file-hash-Dedup) → Notiz in Vault

**v0.5 — verlustfreie Komposition + Notion-Layer + Paperless-Daemon**
- `symscan` v0.5: Dauer-Watcher (fsnotify, Debounce) + Worker-Pool (Retry, Idle-Throttle) + Klassifikation
- desk `internal/dbviews` (typed properties + Views; DatabaseView-Donor; Formeln/Rollups vertagt)
- seek **MUST**-Adds: JSON über MCP + `index_content`
- memory-Komposition (GraphView + Backlinks), fetch `--format md-frontmatter`, vault `resolve`
- Preview/Thumbnails (pdftoppm), Spreadsheet-Glue (soffice öffnen → Watcher → reindex)

**v1 — Härtung + geteilte Kerne**
- **corekit/webuikit + ollamakit** extrahieren; desk *und* vibecoder darauf migrieren (killt die Copy-Paste-Drift)
- seek SHOULD-Adds (Offsets, path-scope, stabile Chunk-IDs), memory SHOULD-Adds (entity-edges, graph-endpoint, as-of)
- piikit + toolprobe, Sync-Conflict-Helper, opt-in Per-Notiz-Verschlüsselung

**Die eine Spannung, die ich aufgelöst habe:** Sequencer wollte den Host in der MVP kopieren, Philosophie-Check wollte webuikit *zuerst* extrahieren. Auflösung: **Kopieren ≠ Importieren.** Generischen Host-Code in MVP nach desk *vendoren* ist erlaubt und standalone-sicher (opencode-Handler weglassen); die corekit-Extraktion folgt in v1. ollamakit rückt nach hinten, weil Leitplanke #2 (nur Text pushen) den fatalen mixed-space-Bug schon in der MVP entschärft.

## Drei Annahmen, die der echte Code korrigiert hat

- ❌ **`vault://` existiert nicht.** Das echte Schema ist `op://path/field` bzw. Dot-Notation `path.field` (`symaira-vault/internal/template/resolver.go`). Deine ECOSYSTEM.md §5.1 verspricht `vault://` — das ist Doku-Drift, nicht Code.
- ❌ „memory's migrations framework nach corekit promoten" ist **schon erledigt** — `sqlitekit.Migrate` existiert. desk konsumiert direkt corekit/sqlitekit.
- ℹ️ Entity-Linking sitzt in `internal/db/entities.go:158`, nicht `internal/memory/prep.go`.

---

Ich halte das fest und schlage als nächsten konkreten Schritt vor: **(a)** das `symaira-desk`-Repo-Scaffold (MVP-Critical-Path Schritt 1–2) tatsächlich anlegen, oder **(b)** das Ganze als ADR + GitHub-Issue-Set/Milestones aufschreiben (passt zu deinem `/02-gh-plan`-Flow), damit der Plan versioniert und abarbeitbar ist. Was zuerst?

Ich speichere den Plan noch in die Projekt-Memory, damit er sessionübergreifend steht."