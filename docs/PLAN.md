# symdesk — Phasenplan für die Umsetzung

> **Zweck dieses Dokuments:** Vollständiger, abarbeitbarer Bauplan für `symaira-desktop`. Geschrieben so, dass eine KI (oder ein Mensch) **ohne weiteren Konversations-Kontext** Phase für Phase umsetzen kann. Jede Phase hat Ziel, Aufgaben, Definition of Done (DoD) mit Prüfkommandos und endet mit Commit + Push + grüner CI, bevor die nächste Phase beginnt.
>
> Hintergrund/Begründungen: [`ARCHITECTURE.md`](./ARCHITECTURE.md) (dieselbe `docs/`, gitignored). Bei Widerspruch gilt: dieses Dokument für das *Was/Wann*, ARCHITECTURE.md für das *Warum*.

**Stand:** 2026-07-06 · **Repo:** `/Users/daniel/Dev/Symaira Dev/symaira-desktop` = `github.com/danieljustus/symaira-desktop` · **Lizenz:** Apache-2.0

---

## 0. Kontext in 10 Zeilen (für die abarbeitende KI)

- `symdesk` ist die **Kompositions-Schale** des Symaira-Ökosystems: eine local-first Arbeitsoberfläche über **einem Plain-Markdown-Vault** (Obsidian-kompatibler Ordner, iCloud-syncbar).
- **Zwei Artefakte, ein Repo:** Go-Binary `symdesk` (CLI + MCP-Server + Service-Schicht, CGO-frei) und native SwiftUI-macOS-App `SymDesk.app` (auf [`symaira-appkit`](https://github.com/danieljustus/symaira-appkit), exakt gepinnt).
- **Keine Web-UI.** Die früher geplante React-SPA ist verworfen (Entscheidung 2026-07-06).
- Die App spricht mit dem Core **nur** über Subprozesse: kurze `symdesk … --json`-Calls (appkit-`CLIRunner`) plus **einen** langlaufenden `symdesk events --json`-NDJSON-Stream. Kein HTTP, kein Socket.
- Der Core **komponiert** Geschwister-Tools zur Laufzeit per PATH-Probe (nie Compile-Time-Import): `symseek` (Suche), `symmemory` (RAG/Graph), `symfetch` (Web→MD), `symvault` (Secrets, `op://`-Schema), `symingest` (OCR/Ingest, hat eigenes `mcp`).
- **Fundament:** `github.com/danieljustus/symaira-corekit` (`mcpserver`, `sqlitekit`, `configkit`, `fsutil`, `exitcodes`, `logkit`, `updatecheck`, `versionkit`); SQLite via `modernc.org/sqlite`.
- **Vorbilder im Nachbarordner** `/Users/daniel/Dev/Symaira Dev/`: `symaira-tune` (Core+MCP+CLI+App in einem Repo, XcodeGen), `symaira-terminal` (Cask/Notarisierung), `symaira-seek` (Go-Core-Konventionen).
- Alle GitHub-Artefakte (Commits, PRs, Issues, Releases) auf **Englisch**, content-focused, keine internen Workflow-/Skill-Namen.
- Nichts committen, was Secrets, `.env*`, private Dokumente oder generierte Artefakte enthält; `docs/` dieses Repos bleibt gitignored.
- Konvention: Conventional Commits (`feat:`, `fix:`, `docs:`, `chore:`), SemVer, Branch `main`.

## 0b. Die 4 Leitplanken (nicht verhandelbar, gelten in jeder Phase)

1. **Vault = IMMER Klartext-Markdown** auf Platte. Keine Verschlüsselung, kein Binärformat, keine DB als Quelle. Die SQLite-Sidecar ist Cache/Index und jederzeit aus den Dateien rekonstruierbar.
2. **`symseek` besitzt ALLE Embeddings.** `symdesk` pusht nur Text, nie Vektoren (seeks mixed-embedding-space-Guard killt sonst alle Queries).
3. **`symdesk` läuft mit null Geschwistern**: eingebauter FTS5-Fallback über die eigene Sidecar. Geschwister-Tools *upgraden* Fähigkeiten, gaten sie nie.
4. **Die App bleibt dünn:** alle Vault-/Fach-Logik im Go-Core; die App rendert und ruft `--json`-Kommandos. Geteiltes UI-Fundament nur als exakt gepinnte appkit-Version, nie Copy-Paste.

---

## Phase 1 — Go-Spine: CLI + MCP + Handshake

**Ziel:** Ein baubares Binary, das die drei Anschlussflächen beweist, bevor irgendein Feature existiert.

**Aufgaben:**
1. `go mod init github.com/danieljustus/symaira-desktop`; Go-Version und Layout an `symaira-seek`/`symaira-ingest` orientieren (`cmd/symdesk/main.go`, `internal/…`). CGO-frei.
2. `go get github.com/danieljustus/symaira-corekit@latest`.
3. Cobra-CLI mit Kommandos: `version`, `mcp`, `doctor` (Stub). Logging via `slog` auf **stderr** (Zero-Stdio-Pollution im MCP-Modus).
4. `symdesk version --json` → `{"version":"0.1.0-dev","schema_version":1}` (corekit/versionkit falls passend). **symdesk ist der erste Core, der den appkit-ToolDetector-Handshake wirklich erfüllt — das ist Absicht.**
5. `symdesk mcp` startet einen stdio-MCP-Server via `corekit/mcpserver` mit einem einzigen Tool `desk_status` (gibt Version + Vault-Pfad-Konfig zurück).
6. Config via `corekit/configkit`: `~/.config/symdesk/config.toml`, Env-Prefix `SYMDESK_` (`SYMDESK_VAULT` zuerst).
7. CI-Workflow `.github/workflows/ci.yml` nach seek/ingest-Vorbild: `go vet`, `golangci-lint` (falls im Ökosystem üblich), `go build`, `go test ./...`.
8. Makefile mit `build`, `test`, `lint` (Vorbild tune/operate).

**DoD:**
```bash
go build ./... && go test ./...
./symdesk version --json   # valides JSON mit schema_version 1
printf '' | ./symdesk mcp  # startet, keine Ausgabe auf stdout außer JSON-RPC
```
CI grün auf main.

---

## Phase 2 — Vault-Contract einfrieren + Sidecar

**Ziel:** Der Vertrag, an dem alles Nachgelagerte hängt, steht schriftlich und maschinell geprüft.

**Aufgaben:**
1. **`VAULT.md` im Repo-Root (committed, English):** Ordnerlayout, Datei-Benennung, Frontmatter-Schema (Pflicht: `title`, `created`, `tags`; plus die Felder, die `symingest` v0.6+ schreibt — **vorher in `../symaira-ingest` nachsehen** (`internal/writer`), der Contract MUSS dessen Frontmatter akzeptieren), Wikilink-Semantik `[[…]]`, Umgang mit Anhängen/Assets. Version des Contracts im Dokument (`contract_version: 1`).
2. `internal/vault`: Vault-Root-Auflösung (Flag > `SYMDESK_VAULT` > Config), Walker (Markdown-Dateien, Ignore-Regeln für `.obsidian/`, `.trash/` etc.), Frontmatter-Parser (YAML), Wikilink-Extraktion.
3. `internal/sidecar` via `corekit/sqlitekit` (WAL), Pfad `~/.local/share/symdesk/sidecar.db`, Schema:
   ```sql
   files(id, path, sha256, title, modified_at, indexed_at)
   file_properties(file_id, key, value, value_type)
   links(from_path, to_path, kind)
   view_presets(id, name, filter_json, sort_json, columns_json)
   ```
   plus FTS5-Tabelle über (title, body) für Leitplanke 3.
4. `symdesk index [path]` — voller Scan bzw. Einzeldatei; idempotent (sha256-Vergleich); `symdesk doctor` prüft Vault-Erreichbarkeit, Contract-Konformität (Frontmatter parsebar), Sidecar-Integrität.
5. Golden-Tests: Beispiel-Vault unter `testdata/vault/` (inkl. einer von symingest erzeugten Beispieldatei).

**DoD:** `symdesk index && symdesk doctor` grün auf `testdata/vault`; Sidecar löschen + neu indizieren ergibt identischen Zustand (SSOT-Beweis); Tests decken Parser+Walker; CI grün.

---

## Phase 3 — Lese-/Schreib-API des Core (CLI + MCP + Events)

**Ziel:** Alles, was die App später braucht, existiert als `--json`-Kommando und als MCP-Tool — **eine** Service-Schicht (`internal/service`), zwei dünne Adapter.

**Aufgaben:**
1. Kommandos (alle mit `--json`):
   - `symdesk ls [--dir …]` — Dateiliste aus der Sidecar (Pfad, Titel, modified).
   - `symdesk search <query>` — FTS5-Fallback jetzt; seek-Upgrade kommt in Phase 4. Ergebnis: Pfad, Titel, Snippet, Score.
   - `symdesk props <file>` / `symdesk backlinks <file>` — Frontmatter-Projektion bzw. `links`-Kanten.
   - `symdesk note new --title …` / `symdesk note move` — Contract-konforme Erzeugung/Umbenennung (Schreibpfad durch den Core, damit Benennung+Frontmatter stimmen).
2. `symdesk events --json`: langlaufender Prozess, fsnotify-Watcher auf dem Vault, NDJSON auf stdout: `{"event":"file_changed|file_added|file_removed|index_updated","path":…,"ts":…}`. Debounce; bei Änderung automatisch inkrementell indizieren. Sauberes Beenden bei SIGTERM/stdin-close.
3. MCP-Tools spiegeln dieselbe Service-Schicht: `desk_search`, `desk_ls`, `desk_props`, `desk_backlinks`, `desk_note_new`.
4. Exit-Codes via `corekit/exitcodes`; Fehler als JSON auf stdout (`{"error":…}`) bei `--json`.

**DoD:** Roundtrip-Test: `note new` → `events` meldet → `search` findet → `backlinks` nach Wikilink-Edit korrekt. `symdesk events --json &` überlebt 1000 schnelle Dateiänderungen ohne Leak (Test mit Beispiel-Vault). CI grün.

---

## Phase 4 — Komposition der Geschwister (Graceful Degradation)

**Ziel:** Installierte Geschwister upgraden symdesk automatisch; fehlende ändern nichts am Verhalten außer der Qualität.

**Aufgaben:**
1. `internal/compose`: PATH-Probe + `version`-Aufruf **immer mit Timeout** (Muster: appkit-`ToolDetector`, aber Go-seitig; falls `corekit/toolprobe` inzwischen existiert, das nehmen). Ergebnis cachen, `symdesk doctor` zeigt gefundene Tools + Versionen.
2. **seek:** bei Index-Updates zusätzlich `symseek index <file>` (nur Text! Leitplanke 2); `symdesk search` nutzt `symseek search … --json` wenn vorhanden, sonst FTS5. Ausgabeformat beider Pfade identisch.
3. **symingest:** `symdesk ingest <file>` delegiert an `symingest ingest`; Events des Watchers erkennen von symingest geschriebene Notizen (normaler `file_added`-Pfad).
4. **symfetch:** `symdesk clip <url>` → `symfetch` → Contract-konforme Notiz (Frontmatter ggf. nachziehen, solange `--format md-frontmatter` upstream fehlt).
5. **symmemory:** `symdesk related <file>` / Graph-Daten für die App (`graph --json`: nodes+edges aus `links`-Tabelle, angereichert um memory-Entities wenn verfügbar).
6. **symvault:** `internal/secrets`: LLM-Keys via `symvault` (`op://`-Referenzen — **`vault://` existiert nicht**); ohne symvault kein Fehler, Feature einfach abwesend (App hat Keychain-Fallback).
7. README-Abschnitt „Composition": Tabelle Tool → Fähigkeit → Fallback.

**DoD:** Testmatrix mit und ohne PATH-Tools (Tests manipulieren PATH): identische Kommandos, degradierte aber korrekte Ergebnisse. Manuell gegen die echten installierten Tools verifizieren (symseek 2.2+, symingest 0.6+). CI grün.

---

## Phase 5 — `SymDesk.app`: Scaffold + Anschluss

**Ziel:** Native App-Hülle, die den Core findet und live sieht.

**Aufgaben:**
1. `project.yml` (XcodeGen) nach Vorbild `../symaira-tune` bzw. `../symaira-terminal`; App-Target `SymDesk` (macOS 14+, Swift 6 strict concurrency). Kein Mac App Store; Distribution später als notarisiertes DMG/Cask.
2. `symaira-appkit` **exakt** pinnen (`.package(url:…, exact: "x.y.z")` — aktuellste Version nehmen; lokale Entwicklung darf `path:` nutzen, gemergter Stand muss auf einen Tag zeigen).
3. Core-Anbindung: `SymairaToolRegistry`/`BinaryLocator` zum Auffinden von `symdesk` (dazu **appkit-PR: `symdesk`-Eintrag in die Registry**, `mcpArgs: ["mcp"]` — inkl. Test-Update `testRegistryContainsAllKnownTools`); Aufrufe via `SymairaCLIRunner` (`runDecoding`, snake_case). `requireSchemaVersion(1)` gegen `symdesk version --json`.
4. Events: ein langlaufender `Process` für `symdesk events --json`, Zeilen als `AsyncSequence` in einen `@Observable`-Store.
5. UI v0: Sidebar (Vault-Baum aus `ls --json`), Datei-Auswahl zeigt rohen Markdown-Inhalt (direktes File-Read), Statusleiste zeigt Core-Version + erkannte Geschwister (`doctor --json`). `SymairaTheme` anwenden.
6. Onboarding-Fallback: Core nicht gefunden → Hinweis + Homebrew-Kommando (Muster aus appkit/Registry).
7. App-Tests (XCTest) für JSON-Decoding + Events-Parsing; CI-Job für `xcodebuild`/`swift build` der App ergänzen (Toolchain-Hinweis: lokal `DEVELOPER_DIR=/Applications/Xcode-beta.app/Contents/Developer` für Tests).

**DoD:** App startet, zeigt echten Vault, externe Dateiänderung (z. B. `touch`/Edit in anderem Editor) erscheint ≤2 s via Events in der Sidebar. CI grün (Go + Swift).

---

## Phase 6 — Editor + Command Palette (MVP-Nutzbarkeit)

**Ziel:** Tägliche Notiz-Arbeit wird möglich; UX-Referenz ist Mosaic (nur als Vorlage, kein Code-Port).

**Aufgaben:**
1. **Source-Mode-Markdown-Editor** (bewusst kein Block-/WYSIWYG-Editor — v1+-Thema): NSTextView-Wrapper mit Markdown-Syntax-Highlight, `[[`-Wikilink-Autocomplete (Kandidaten aus `ls --json`), Cmd-Klick auf Wikilink öffnet Ziel.
2. Speichern: atomarer Write (temp+rename via Core oder direkt — Benennungs-/Frontmatter-Änderungen immer durch `symdesk note …`); nach Save triggert der Watcher das Reindex (kein Extra-Call nötig).
3. Optionales Preview-Pane (gerenderter Markdown, read-only; einfache native Lösung, kein WebView-Zwang).
4. **Command Palette** (Cmd-K): Datei-Sprung (fuzzy über `ls`), Volltext (`search --json`), Aktionen (Neue Notiz, Clip URL, Ingest-Datei).
5. Backlinks-Panel (`backlinks --json`) am Editor.
6. Konflikt-Hinweis: iCloud-Konfliktdateien (`* 2.md`, `*conflicted copy*`) erkennen und anzeigen (Auflösung = spätere Phase).

**DoD:** Selbst-Dogfooding-Szenario läuft: Notiz anlegen → verlinken → suchen → Backlink sehen, alles in der App; parallel bleibt derselbe Vault in Obsidian öffenbar und konsistent. CI grün.

---

## Phase 7 — dbviews + Graph (die Notion/Obsidian-Schicht)

**Ziel:** Strukturierte Sichten und Graph — die beiden Features, die den „eine Oberfläche"-Traum sichtbar machen.

**Aufgaben:**
1. `internal/dbviews` (Go): typed properties als Projektion Frontmatter → `file_properties` → gespeicherte Views (`view_presets`); Kommandos `symdesk views list|get|save … --json` + MCP-Pendants. Keine Formeln/Rollups (vertagt).
2. App: native `Table`-View über eine gespeicherte View (Spalten/Filter/Sort aus JSON), Inline-Edit einzelner Properties (schreibt Frontmatter via Core).
3. GraphView: `Canvas`/SpriteKit-Force-Layout über `graph --json` (Phase 4); Klick öffnet Notiz; Filter auf Tag/Ordner.
4. Performance-Gate: 5.000-Dateien-Testvault (Generator-Script unter `testdata/`), Index < 30 s kalt, Suche < 200 ms, Graph rendert flüssig für den gefilterten Sichtbereich.

**DoD:** Eine gespeicherte View („Rechnungen 2026: status != bezahlt") und der Graph funktionieren auf dem 5.000er-Vault innerhalb der Gates. CI grün.

---

## Phase 8 — AI-Dock + Ingest-Rundlauf (Komposition sichtbar machen)

**Ziel:** Der Papierkram-Workflow („Scan → durchsuchbare, verlinkte Notiz") und ein erster AI-Assistent in der App.

**Aufgaben:**
1. **Ingest-Inbox:** konfigurierbarer Inbox-Ordner; App zeigt Queue (`symingest jobs`, falls verfügbar) bzw. beobachtet neue Notizen; Drag&Drop von PDF/Bild in die App → `symdesk ingest`.
2. **AI-Dock (Panel):** Chat über den eigenen Vault — Kontext via `search`/`related` (RAG-Zusammenbau im Core: `symdesk ask --json` streamt Antwort-Chunks als NDJSON); LLM-Key via `internal/secrets` (symvault → appkit-Keychain-Fallback); lokales Modell via Ollama, falls konfiguriert. Provider-Wahl im Core, nicht in der App.
3. `symfetch`-Clip aus der Palette inkl. AI-Zusammenfassung als Frontmatter-Feld (optional).
4. Alle AI-Features sind **optional + degradierbar** (kein Key → Dock erklärt, was fehlt).

**DoD:** End-to-End-Demo: PDF in Inbox → OCR-Notiz erscheint → in Suche/Graph sichtbar → AI-Dock beantwortet eine Frage mit Zitat/Verweis auf die Notiz. CI grün.

---

## Phase 9 — Härtung + Release v0.1.0

**Ziel:** Erste öffentliche Version, installierbar wie die Geschwister.

**Aufgaben:**
1. `symdesk doctor` produktionsreif (Vault, Sidecar, Geschwister, Konflikte, Contract-Version); README (English) vollständig: Install, Quickstart, Composition-Tabelle, Screenshots.
2. Sicherheitspass: keine Secrets in Logs, Events-Stream leakt keine Dateiinhalte, Pfad-Traversal-Checks an allen Datei-Kommandos.
3. Packaging: Homebrew-Formula für `symdesk` (Vorbild Geschwister im `../homebrew-tap`), App als notarisiertes DMG/Cask (Vorbild terminal/tune) — Signing/Notarisierung erfordert Daniels Zertifikate → als **manueller Schritt markieren**, nicht blind automatisieren.
4. CHANGELOG.md, Version 0.1.0, Tag + GitHub-Release mit Assets; appkit-Registry-Eintrag für `symdesk` muss zu diesem Zeitpunkt released sein (Phase 5, appkit-PR + appkit-Patch-Release).
5. ECOSYSTEM.md / Root-Inventar im Eltern-Ordner um symaira-desktop-Status ergänzen (nur wenn dort bereits Muster dafür existiert).

**DoD:** `brew install danieljustus/tap/symdesk` (bzw. lokales Formula-Audit) + App-DMG startklar; alle vorigen Phase-DoDs weiterhin grün; Release veröffentlicht.

---

## Arbeitsregeln für die abarbeitende KI

1. **Eine Phase pro Arbeitszyklus.** Phase abschließen (DoD + CI grün + gepusht), dann erst weiter. Phasen nicht mischen.
2. **Vor jeder Phase kurz verifizieren**, dass Annahmen noch stimmen (Versionen der Geschwister, appkit-Registry-Stand, corekit-APIs) — dieses Dokument ist vom 2026-07-06.
3. **Bei Contract-Fragen** (Vault-Layout, Frontmatter) entscheidet Kompatibilität mit `symingest`-Output + Obsidian-Öffnbarkeit; im Zweifel konservativ und dokumentieren in `VAULT.md`.
4. **Scope-Disziplin:** Nichts nachbauen, was ein Geschwister-Tool kann (Leitplanken!). Neue Ideen als GitHub-Issue notieren statt einbauen.
5. **Blocker** (fehlende Zertifikate, upstream-Bugs in Geschwistern, unklare Design-Entscheidungen mit Nutzer-Impact): stoppen und an Daniel eskalieren statt raten. Upstream-Wünsche (z. B. `symfetch --format md-frontmatter`, seek `index_content` über MCP) als Issues im jeweiligen Repo anlegen (English).
6. **Tests gehören zur Phase**, nicht ans Ende; jede Phase erweitert CI.
