# Symaira Loom — Produkt-, Architektur- und Validierungsplanung

**Stand: 2026-07-27** · Status: **Produktplanung abgeschlossen, Umsetzung an Gates gebunden.**
Technische Tiefe der Raumschicht: [`../../symaira-room/docs/PLAN.md`](../../symaira-room/docs/PLAN.md).
Arbeitsauftrag, aus dem dieses Dokument hervorgeht: `../../docs/TODO.md` §7.

---

## 1. Produktthese

> **Symaira Loom ist der souveräne Arbeitsraum für Menschen und ihre KI-Agenten:
> lokal, dateibasiert, signiert — mit menschlicher Freigabe für alles, was ein
> Agent tut, und einem Nachweis, der auch später noch stimmt.**

Der Job-to-be-done, präzise formuliert:

*„Ich lasse mehrere KI-Agenten an echten Projektdateien arbeiten. Ich will
(a) hinterher belegen können, wer was getan hat, (b) vorher gefragt werden,
bevor etwas Folgenreiches passiert, und (c) das alles ohne fremde Cloud."*

Loom ist die Produkt- und Markenklammer. Die Substanz liefern die bestehenden
Bausteine; **neu gebaut wird nur die Raumschicht** (`symroom`).

### Was Loom **nicht** ist

- Kein Slack-, Notion-, GitHub- oder Buzz-Klon.
- Kein Team-Chat, kein Git-Host, kein Relay, kein Marketplace, kein Billing im MVP.
- Kein zweiter Tool-Hub und keine zweite Dokumentdatenbank.
- **In Phase 1 kein eigenes Binary und keine eigene App** (§5) — Loom ist zuerst
  ein Produktversprechen mit einer klaren Komposition, nicht ein weiteres Repo
  voller Code.

---

## 2. Auslöser & Wettbewerb: Buzz (Block)

Block hat am 2026-07-21 [Buzz](https://github.com/block/buzz) veröffentlicht
([Ankündigung](https://block.xyz/inside/introducing-buzz-where-humans-and-agents-work-together)).
Recherchestand 2026-07-27:

- **Substrat:** Ein Buzz-Server *ist* ein Nostr-Relay. Jede Nachricht, Reaktion,
  jeder Workflow-Schritt und jedes Git-Ereignis ist ein signiertes Nostr-Event in
  einem gemeinsamen, unveränderlichen Log.
- **Stack:** Rust-Workspace + React; Postgres (Storage + FTS), Redis (Pub/Sub),
  S3/MinIO (Medien); Desktop-App (Tauri), Web-Client, Mobile via Flutter in Arbeit.
- **Funktioniert heute:** Channels, Threads, DMs, Canvases, Medien, Volltextsuche,
  Audit-Log aus signierten Events, Agenten als vollwertige Mitglieder mit eigenen
  Schlüsseln.
- **In Arbeit:** Workflow-Approval-Gates, Git-Hosting/NIP-34-Patches, Push,
  Mobile-Clients.
- **Betrieb:** `docker compose` (Postgres/Redis/MinIO/Caddy), self-hosted oder
  Blocks Managed-Instanz. Apache-2.0, ~1.900 Commits, ~343 offene Issues,
  ausdrücklich „not finished".

### Vergleichsmatrix

| Dimension | **Buzz** | **Symaira Loom** |
|---|---|---|
| Substrat | Nostr-Relay + Postgres/Redis/S3 | **Dateien im Ordner des Nutzers** (JSONL-Segmente), kein Dienst |
| Identität | Nostr-Keys, perspektivisch Web-of-Trust | Ed25519 pro Teilnehmer, Vertrauensanker im Raum-Journal, kein PKI |
| Betrieb | Docker-Compose-Stack, Relay muss laufen | `brew install symroom` — **keine Infrastruktur** |
| Artefakte | Eigene Canvases, Medien, Git-Hosting im Relay | **Referenzen** auf den Markdown-Vault/das Dateisystem; Inhalte bleiben, wo sie sind |
| Kommunikation | Vollwertiger Messenger (Channels, Threads, DMs, Voice) | **Bewusst keiner** — nur Journal-Notizen und Entscheidungen |
| Agentenmodell | Agent = Mitglied mit Schlüssel und Rechten | Agent = Mitglied mit Schlüssel **+ raumbezogenes Brain-Profil** (Capability Shaping) |
| Freigaben | Workflow-Approval-Gates (in Arbeit) | Freigabe auf **Vorgangsebene** mit Scope + TTL; Agenten können *strukturell* nicht freigeben |
| Audit | Signierte Events im Relay-Log | Signierte, pro Autor hash-verkettete Segmente, offline prüfbar (`symroom verify`) |
| Sync | Relay (zentral je Community) | Vorhandener Datei-Sync (iCloud/Syncthing/Git); merge-sicher per Konstruktion |
| Clients | Desktop (Tauri), Web, Mobile (Flutter) | CLI + MCP zuerst; GUI als Hub-Modul, später Apple-native App |
| Reifegrad | Große, finanzierte Codebasis, unfertig | Nichts gebaut; ~12 PT für v0.1.0 der Raumschicht |
| Lizenz | Apache-2.0 | Apache-2.0 |
| Zielgruppe | Ganze Firmen/Communities | Einzelperson mit vielen Agenten → später 2–10 Personen |

### Schlussfolgerung

1. **Frontal konkurrieren wäre ein Fehler.** Buzz gewinnt jede Breitenwette
   (Chat, Voice, Git, Mobile, Workflows) mit Firmenbudget.
2. **Buzz validiert die Kategorie.** Dass Block genau diese Wette eingeht, ist das
   beste verfügbare Signal, dass „Menschen + Agenten im selben Raum mit einer
   Beweisspur" ein echtes Problem ist.
3. **Die Lücke ist die entgegengesetzte Achse:** Buzz ersetzt die Werkzeuge durch
   ein neues Substrat (ein Relay statt Chat + Forge + CI). Loom **ersetzt gar
   nichts** — es legt eine dünne, überprüfbare Buchführung *über* die Werkzeuge,
   die der Nutzer schon hat, ohne Server, ohne Migration, ohne Konto.
4. **Interop statt Wettbewerb ist die Rückfalloption:** Ein Export des
   Raum-Journals als signierte Nostr-Events wäre technisch klein (beide Formate
   sind signierte Ereignisketten). Ausdrücklich **kein** Ziel für v0.1 — aber es
   ist der Grund, das Ereignisformat sauber und signiert zu halten.

---

## 3. Zielgruppe & Job-to-be-done (geschärft)

Die ursprüngliche Hypothese lautete „kleine, privacy-affine Entwicklerteams
(2–10 Personen)". **Das ist für den ersten Schritt zu groß und nicht validierbar.**
Präzisierung:

| Stufe | Nutzer | Warum diese Reihenfolge |
|---|---|---|
| **Z0 (zuerst)** | **Eine Person mit mehreren Agenten** — genau Daniels Alltag: Claude Code, Codex, Cursor, geplante Läufe, ~30 Repos | Multi-Agent/Single-Human ist *heute* real. Es braucht keine zweite Person, keinen Sync, keinen Server — und das Bedürfnis („was hat der Agent nachts getan, und wer hat das erlaubt?") ist unmittelbar spürbar |
| **Z1** | **Zwei Menschen + Agenten an einem Projekt** (Freelancer + Kunde, Zweierteam) | Erster echter Mehrbenutzer-Fall; funktioniert per Ordner-Sync ohne neue Infrastruktur |
| **Z2 (später)** | Kleine, privacy-affine Teams 2–10, DSGVO-sensible Kontexte (Kanzlei, Praxis, Behördenumfeld, Agentur) | Erst nach Z1 sinnvoll; hier entstünden auch die ersten Zahlungsbereitschaften |

**Das ändert die Produktlogik grundlegend:** Loom muss nicht zuerst
Mehrbenutzerfähigkeit beweisen, sondern **Nachvollziehbarkeit und Freigabe für
einen Menschen mit vielen Agenten**. Mehrbenutzer ist dann eine Eigenschaft des
Formats (§ Room-Plan 5.4), kein zusätzliches Produkt.

---

## 4. Produktgrenzen: wer macht was

```
Symaira Loom  (Produkt / Marke / Komposition — kein eigener Code in Phase 1)
│
├── symroom   Raumschicht: Teilnehmer · signiertes Journal · Artefakt-Referenzen · Freigaben   ← NEU
├── symdesk   Dokumente: Markdown-Vault als SSOT, Historie, Ingest, OCR                        (v0.7.1)
├── symbrain  Agenten-Kontext: Profile pro Verbindung, Capability Shaping, Harness-Install     (v0.2.3)
├── symguard  Call-Time-Enforcement: Risiko, allow/ask/deny, Schema-Pinning                    (heute Skelett)
├── symvibe   Ausführung von Agentenläufen                                                     (v0.5.0, kein headless run)
└── symmemory · symseek · symskills · symvault   Wissen, Suche, Fachwissen, Secrets
```

Verbindliche Zuständigkeitsregeln:

| Frage | Zuständig | Nicht zuständig |
|---|---|---|
| Wo liegt der Inhalt eines Dokuments? | **symdesk** | Room speichert nur Referenz + Hash |
| Was darf dieser Agent überhaupt sehen? | **symbrain** (Profil) | Room erzeugt das Profil, wertet es nicht aus |
| Darf dieser einzelne Tool-Call passieren? | **symguard** | Room klassifiziert kein Risiko |
| Ist dieser *Vorgang* bewilligt, und wo steht er? | **symroom** | guard führt kein Vorgangsjournal |
| Wer ist Teilnehmer, und mit welchem Recht? | **symroom** | brain kennt keine Personen |
| Wie startet ein Agentenlauf? | **symroom** (generischer Adapter) → später symvibe | Room baut keinen Modellclient |
| Wo sieht der Mensch das alles? | **Loom-Oberfläche** (Hub-Modul, später App) | keine Web-UI (Ökosystem-Anti-Pattern) |

**Die kritische Kante ist Room ↔ Guard.** Sie ist im Room-Plan §4.1 verbindlich
gezogen: *Guard entscheidet, ob gefragt werden muss; Room ist der Ort, an dem
gefragt und protokolliert wird.* Diese Formulierung gehört zusätzlich in
`symaira-guard/AGENTS.md`, sobald guard real gebaut wird.

---

## 5. Architekturentscheidung

Vier Optionen standen zur Debatte (TODO §7.8): Integration in symdesk,
Erweiterung von symbrain, eigener Dienst/Repo, Buzz-Adapter.

**Entscheidung: eigenes Repo für die Raumschicht (`symaira-room`), Loom als
Produktklammer ohne eigenen Code in Phase 1.** Begründung:

| Option | Bewertung |
|---|---|
| **In symdesk integrieren** | ✗ Desk ist per Beschluss das Markdown-Vault-Produkt für *einen* Menschen; sein Self-Host-Server ist ausdrücklich Ein-Nutzer (ein Bearer-Token). Eine Mehr-Akteur-Ereigniskette dort einzuziehen, verwässert eine bereits getroffene Produktentscheidung |
| **symbrain erweitern** | ✗ Brain ist Kontext *pro Harness-Verbindung*, ausdrücklich speicherlos und ohne Personenbegriff. Ein Journal dort widerspricht seiner Kernaussage |
| **Buzz adaptieren** | ✗ Erzwingt Relay + Postgres + Redis + S3 — das Gegenteil des Versprechens („keine Infrastruktur"). Als *Export-Brücke* später sinnvoll, als Fundament nicht |
| **Eigenes Repo `symaira-room`** | ✓ Anderes Datenmodell, andere Sync-Semantik, anderes Publikum; standalone-first erfüllt; die Repos existieren bereits |

### 5.1 Kein `symloom`-Binary in Phase 1

Das Root-`AGENTS.md` führt `symloom (geplant)`. **Diese Planung empfiehlt,
darauf zu verzichten:** ein reiner Orchestrator-CLI über symroom/symdesk/symbrain
hätte keine eigene Fachlogik und wäre genau die Art Bündel-Tool, die das
Ökosystem an anderer Stelle (Brain: „kein generischer Hub") schon abgelehnt hat.
Der Name bleibt für Phase 2 reserviert. → Folgeaufgabe F-1.

### 5.2 GUI-Staffelung

| Phase | Oberfläche | Begründung |
|---|---|---|
| **P1** | **CLI + MCP + `--json`** (`symroom`) | Der erste Nutzer (Z0) lebt im Terminal und in Agenten-Harnesses. Der Agent nimmt über MCP teil, der Mensch über CLI |
| **P2** | **Hub-Modul `SymroomFeature`** nach dem etablierten Module-Integration-Contract (`symaira-hub/AGENTS.md`) | Kein Strategiebruch, kein neues App-Target, keine neue Signatur/Cask. Journal-Viewer + Freigabe-Sheet + Teilnehmerliste |
| **P3** *(nur nach Gate G3)* | **Eigene App `SymairaLoom.app`** (macOS + iOS, SwiftUI, appkit) | Erst wenn Z1/Z2 validiert sind. Die Ausnahme vom „eine Hub-App"-Grundsatz ist dann genauso begründet wie bei terminal und eraseme: **anderes Publikum** (Team statt Entwickler-Werkzeugkasten), eigener kommerzieller Keil |
| **nie** | Web-UI | symdesk hat das ausdrücklich abgelehnt; Apple-native UX ist ein Differenzierungsmerkmal |

### 5.3 Lizenz

- `symaira-room` und `symaira-loom` sind **public Apache-2.0**. Kein Billing-,
  Tenant- oder Cloud-Code in diesen Repos (Ökosystemregel).
- Es gibt keine Pro-Variante und kein `symaira-loom-pro`. Gehosteter Raum-Sync,
  Push-Benachrichtigungen und Aufbewahrungsgarantien für regulierte Kunden sind
  Nicht-Ziele, keine aufgeschobene Arbeit.

---

## 6. Roadmap mit Gates

Jedes Gate ist eine **explizite Go/No-Go-Entscheidung von Daniel**, kein Ritual.

| Gate | Vorbedingung | Entscheidung |
|---|---|---|
| **G0 — jetzt** | Diese beiden Pläne liegen vor | ✅ Planung abgeschlossen. Keine Implementierung ohne G1 |
| **G1 — Eigenbedarf belegt** | Daniel benennt schriftlich **drei konkrete Vorfälle** der letzten Wochen, bei denen ein Raum-Journal oder eine Freigabe echten Nutzen gehabt hätte (z. B. „welcher Agent hat diese Änderung gemacht?", „ich hätte den nächtlichen Lauf gern vorher gesehen") | Go → `symroom` M1–M3 bauen (~6 PT). Kein Beleg → Plan bleibt liegen, Aufwand geht in `docs/TODO.md` §3–§5 (DaemonKit, Hub-Module, Hub-Distribution) |
| **G2 — Selbstnutzung trägt** | 4 Wochen realer Einsatz im Raum `symaira-dev` (§7). Kriterien in §7.3 | Go → M4–M6 + Hub-Modul (P2). Kein Go → v0.1 einfrieren, als Werkzeug behalten, Loom als Produkt einstellen |
| **G3 — Fremdnutzen belegt** | Mindestens **drei** externe Pilotgespräche, davon **zwei** mit konkretem Anwendungsfall und **eine** Aussage zu Zahlungsbereitschaft | Go → Loom-App (P3) + `symaira-loom-pro` planen. Kein Go → Loom bleibt Einzelplatz-Werkzeug (das ist ein akzeptables Ergebnis, kein Scheitern) |

**Stop-Kriterien (jederzeit, führen sofort zum Abbruch):**

1. Room braucht einen laufenden Dienst, um nützlich zu sein → das Versprechen ist gebrochen.
2. Der Aufwand für v0.1 übersteigt 20 PT (Schätzung: 12).
3. Es entsteht Bedarf nach Threads/Reaktionen/Presence → dann ist es ein Chat, und der Wettbewerb heißt Buzz/Slack; abbrechen statt nachziehen.
4. Room fängt an, Risiken zu klassifizieren oder Calls zu blocken → das ist guard; abbrechen und stattdessen guard bauen.
5. Nach G2 nutzt Daniel es selbst nicht mehr freiwillig.

---

## 7. Validierungs-Experiment

### 7.1 Aufbau (nach G1, parallel zu M4)

- **Raum `symaira-dev`**, Ordner im bestehenden SymDesk-Vault (also automatisch in iCloud).
- Teilnehmer: `daniel-macbook` (owner, Mensch), `claude-code` (agent),
  `codex` (agent), optional `daniel-mini` (zweites Gerät, eigene Identität).
- Artefakte: die 3–5 Repos, an denen gerade real gearbeitet wird.
- Alle Agentenläufe, die länger als „eine Frage" sind, laufen über
  `symroom run request` + Freigabe.

### 7.2 Zu messen (4 Wochen, wöchentlich notiert)

| Kennzahl | Zielwert |
|---|---|
| Läufe über Room angefordert | ≥ 20 |
| davon abgelehnt oder mit geändertem Scope freigegeben | ≥ 2 (sonst ist die Freigabe ein reiner Klick-Reflex) |
| Rückfragen ans Journal („wer hat das gemacht?") | ≥ 5 |
| Mergekonflikte / Datenverluste | **0** |
| `symroom verify`-Befunde ohne echte Manipulation (Fehlalarme) | **0** |
| Zeit vom Anforderung bis Freigabe (Median) | < 5 min bei Anwesenheit |
| Momente, in denen Room im Weg war | notieren, ungewichtet |

### 7.3 Auswertung an G2

**Go**, wenn: Zielwerte erreicht **und** Daniel die Frage „würde ich es
abschalten wollen?" mit Nein beantwortet **und** mindestens einmal eine Freigabe
tatsächlich etwas verhindert hat.

**No-Go**, wenn: das Anfordern als Bürokratie empfunden wird, Läufe an Room
vorbei gestartet werden, oder das Journal in vier Wochen nie gelesen wurde.

### 7.4 Externe Piloten (nach G2, vor G3)

Drei Gespräche, gezielt aus dem realen Umfeld: eine Agentur/Freelancer mit
Kundenprojekten, ein DSGVO-sensibler Kontext (Kanzlei/Praxis/Beratung), ein
technischer Early Adopter aus der Homebrew-/MCP-Ecke. Leitfrage: *„Wie belegst du
heute, was eine KI in deinem Projekt getan hat — und wer es erlaubt hat?"*
Kein Pitch vor der Antwort.

---

## 8. Risiken

| Risiko | Gegenmaßnahme |
|---|---|
| **Buzz frisst die Kategorie** | Nicht auf Breite antworten. Positionierung „kein Server, keine Migration, deine Dateien" schriftlich halten; Nostr-Export als späte Interop-Option offenhalten |
| **Loom bläht sich zum Chat/Team-Produkt auf** | Nicht-Ziele in §1 + Stop-Kriterium 3; `note.posted` bleibt ein Journal-Kind |
| **31. Repo ohne Nutzer** | Gates G1–G3; Room wird erst nach belegtem Eigenbedarf gebaut, und ~12 PT sind ein bewusst kleiner Einsatz |
| **Guard-Vakuum** — Room übernimmt schleichend Guards Aufgabe, weil guard leer ist | Grenze im Room-Plan §4.1 wörtlich; Approval-Backend-Kontrakt (§7.3 dort) friert die Schnittstelle *jetzt* ein, damit guard sie später nur noch benutzen muss |
| **Ökosystem-Strategiebruch durch eine neue App** | P3 ist an G3 gebunden und nur mit der terminal/eraseme-Begründung („anderes Publikum") zulässig |
| **Aufmerksamkeit fehlt** — `docs/TODO.md` hat mit DaemonKit, Hub-Modulen und Hub-Distribution offene, angefangene Arbeit | G1 ist ausdrücklich auch eine Priorisierungsentscheidung gegen diese Punkte. Wer G1 mit Ja beantwortet, verschiebt sie bewusst |
| **Signiertes Format falsch geschnitten** | `schema_version` ab Tag 1, Golden Files, Format vor Abschluss von M3 einfrieren (Room-Plan §11) |

---

## 9. Aufwand

| Posten | Aufwand |
|---|---|
| `symroom` v0.1.0 (M1–M6) | ~12 PT |
| Hub-Modul `SymroomFeature` (P2) | ~2 PT |
| Validierungsphase (nebenbei, 4 Wochen) | ~1 PT Auswertung |
| **Bis Gate G3** | **~15 PT** |
| Loom-App P3 + `loom-pro` (nur nach G3) | nicht geschätzt — eigene Planung |

---

## 10. Folgeaufgaben (cross-repo)

| # | Aufgabe | Repo/Datei |
|---|---|---|
| **F-1** | Inventar-Zeile für `symaira-loom` korrigieren: Binary `symloom` → *„kein Binary in Phase 1 (Produktklammer)"*; `symaira-room` bleibt mit `symroom` | `AGENTS.md` (Root), `ECOSYSTEM.md` §11 |
| **F-2** | Schreibweise `SymRooms` → `SymRoom`/`symroom` vereinheitlichen | `docs/TODO.md` §7 |
| **F-3** | `docs/TODO.md` §7 auf „Planung erledigt, Gates offen" umstellen und die Akzeptanzkriterien gegen diese beiden Pläne abhaken | `docs/TODO.md` |
| **F-4** | Room↔Guard-Grenze übernehmen, sobald guard real gebaut wird | `symaira-guard/AGENTS.md` |
| **F-5** | Upstream-Issue: Brain-Profile aus zusätzlichem Pfad laden (`serve --profile-file`) | `symaira-brain` |
| **F-6** | Beobachten, ob `symvibe` ein headless `run`-Kommando bekommt (dann Room-Adapter) | `symaira-vibecoder` |
| **F-7** | Bei G1 = Go: `AGENTS.md` in `symaira-room` anlegen (Room-Plan R-02) und Repo-Setup (Labels, Milestones, CI, Branch-Protection) über `/02-gh-plan` | `symaira-room` |

---

## 11. Entscheidungen

- **Name:** Symaira Loom (Produkt), SymRoom (Raumschicht) — *2026-07-24*
- **Keine Implementierung vor Planungsabschluss** — *2026-07-24*
- **Architektur:** eigenes Repo `symaira-room`; kein symdesk-/symbrain-Einbau; kein Buzz-Fundament — *2026-07-27*
- **Loom hat in Phase 1 keinen Code und kein Binary**; die Marke trägt die Komposition — *2026-07-27*
- **Zielgruppen-Reihenfolge Z0 → Z1 → Z2**: zuerst eine Person mit vielen Agenten, nicht „Teams 2–10" — *2026-07-27*
- **GUI-Staffelung P1 CLI/MCP → P2 Hub-Modul → P3 eigene App (nur nach G3)**; niemals Web-UI — *2026-07-27*
- **Kein Netzwerkdienst, kein Relay**: Mehrbenutzer über vorhandenen Datei-Sync — *2026-07-27*
- **Freigabe-Grenze:** Room = Vorgang und Protokollort, Guard = einzelner Call — *2026-07-27*
- **Umsetzung ist an die Gates G1–G3 gebunden**, Stop-Kriterien in §6 gelten — *2026-07-27*

---

## 12. Nicht-Ziele (unverändert gültig)

Kein eigener Git-Host · kein vollständiger Chat-Klon · kein eigener Nostr-Relay
ohne Nachfrage · kein Marketplace, kein Billing im MVP · keine autonomen
Schreibrechte für Agenten ohne Freigabe · keine zweite Dokumentdatenbank · kein
zweiter Tool-Hub · keine Web-UI.

---

### Quellen

- Buzz-Repository: <https://github.com/block/buzz>
- Block: *Introducing Buzz — where humans and agents work together*: <https://block.xyz/inside/introducing-buzz-where-humans-and-agents-work-together>
- Ökosystem-Evidenz: lokale Repos, Stand 2026-07-27 (siehe Room-Plan §2)
