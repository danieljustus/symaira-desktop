# CK-SECRET: secretref, log, env adoption review

Scope: whether the local Rust re-implementations of the CoreKit secret-reference
resolver, the slog-style stderr logger and ordered env lookups can be replaced
by `symaira-core-secretref`, `symaira-core-log` and `symaira-core-env`.

- CoreKit revision: `592bebb54080d6b37372e47c461cfa3aeca7d2af` (sources read via
  `git show 592bebb:rust/<crate>/src/lib.rs`, line numbers below refer to that blob).
- Base: `f24377a4`, branch `local/ck-secret`.
- Acceptance rule: stdout, stderr, exit code and error text stay byte-identical
  to the Go-recorded fixtures under `testdata/port/**` (frozen, read-only).

## Result

No site is adoptable at this revision. Every candidate differs observably from
the frozen behavior, so all local code stays and no `Cargo.toml` or source
file changed. The differences are recorded as upstream gaps for CoreKit.

## Adopted sites

None.

## Kept-local sites

### 1. `crates/symdesk-ingest/src/secretref.rs` (caller `mail.rs:329`) vs `symaira-core-secretref`

Fixture evidence (`testdata/port/cli/cli-ingest-bodies-process.json`):

- line 3184/3185: `resolve password_secret for alice: environment variable MAIL_TEST_UNSET is not set (reference env://MAIL_TEST_UNSET)`
- line 3287/3288: `resolve password_secret for alice: resolve <redacted> symvault binary not found on PATH`

Probe of CoreKit `592bebb` (scratch crate with the pinned git dependency, real
output):

| Input | CoreKit result | Local / fixture result |
|---|---|---|
| `env://PROBE_UNSET` | `environment variable PROBE_UNSET is not set` | `environment variable PROBE_UNSET is not set (reference env://PROBE_UNSET)` |
| `env://` | `invalid reference: environment variable name is empty` | `environment variable  is not set (reference env://)` |
| `symvault://a/b`, no `symvault` on PATH | `credential helper command failed: command exited unsuccessfully (status=Some(1))` (or a spawn error) | `resolve symvault://a/b: symvault binary not found on PATH` |
| `keychain://svc/acct/extra` | `invalid reference: invalid keychain reference: expected keychain://service/account` | accepted, account `acct/extra` (local `secretref.rs:39`) |
| helper exits 3 with stderr `boom` | `command exited unsuccessfully (status=Some(3))`, stderr suppressed | `exit status 3: boom` (local `secretref.rs:141-146`) |
| helper times out | `command timed out after 100ms` | `secretref: subprocess timed out: <what> timed out after 5s` (local `secretref.rs:130`) |
| invalid symvault path | `invalid reference: invalid symvault credential path: ...` | `resolve <ref>: invalid symvault credential path: ...` |

Differences by location:

- CoreKit `lib.rs:386` `EnvironmentUnset { name }` / `lib.rs:440` message has no
  `(reference ...)` suffix; local `secretref.rs:55` and the env fixture require it.
- CoreKit has no `look_path("symvault")` pre-check and no
  `symvault binary not found on PATH` message (`lib.rs:160` `Spawn`); local
  `secretref.rs:79-82,181`, fixture line 3287.
- CoreKit never prefixes errors with `resolve <reference>:`; local
  `secretref.rs:30-47` does (the mail caller adds `resolve password_secret for ...`
  on top).
- CoreKit suppresses helper stderr (`lib.rs:319`); the local Go-parity text
  appends it (`secretref.rs:141-146`).
- CoreKit timeout text embeds a `Duration` debug value (`lib.rs:161`); local uses
  `... timed out after 5s` (`secretref.rs:128-131`).
- CoreKit rejects `/` in the keychain account and control characters in
  service/account (`lib.rs:59,142`); local accepts them (`secretref.rs:39-47`).
- Non-contract differences (not blocking by themselves): CoreKit starts the
  helper in its own process group and caps output at 64 KiB (`lib.rs:185,240`).

A thin adapter cannot close this: the `(reference ...)` suffix, the PATH
pre-check text and the stderr detail are not recoverable from `ResolveError`
without re-implementing the resolver, which would leave a private copy behind.

### 2. `crates/symdesk-service/src/secrets.rs` and `symdesk-cli/src/ai_secrets.rs`

Out of the owned write set and not a CoreKit resolver duplicate: it ports the
Go `internal/secrets.ResolveKey` (`op://`, Keychain, `SYMDESK_LLM_API_KEY`) and
returns an empty string for every failure. `secretref` syntax differs
(`symvault://`, `keychain://`). Not a candidate.

### 3. Slog text emitters vs `symaira-core-log`

Sites: `symdesk-cli/src/ingest_common.rs:195-220`, `paperless_cli.rs:324-353`,
`conflict_cli.rs:25-70`. The Go entry point installs
`logkit.New(os.Stderr, slog.LevelInfo, "text")` (`cmd/symdesk/main.go:119`).

Probe of CoreKit `592bebb` (real output, host zone CET):

```
time=2026-11-12T12:23:14.845Z level=INFO msg=plain
time=2026-11-12T12:23:14.845Z level=INFO msg="nelx"        <- input "nel\u{85}x": C1 control emitted raw
time=2026-11-12T12:23:14.845Z level=INFO msg=zwsp<U+200B>x  <- not quoted
time=2026-11-12T12:23:14.845Z level=INFO msg=bad<U+FEFF>bom <- not quoted
```

Differences:

- Timestamp: `Logger::new` always formats UTC with a `Z` suffix
  (`log lib.rs:395-420`, `TimestampPolicy::Current`). Go's slog text handler and
  the local emitters print the host-local offset (`+01:00` in Europe/Berlin;
  `TZ=UTC` gives `Z`), see `crates/symdesk-cli/src/main.rs:81-87` and the
  `[offset_hour sign:mandatory]:[offset_minute]` formatting in the three sites.
  Same instant, different bytes whenever the zone is not UTC.
- Quoting: `text_string` / `go_text_quote` (`log lib.rs:299-336`) only escape
  ASCII controls and DEL. Go's `strconv.Quote` (used by slog) also quotes
  non-printable runes (U+200B, U+FEFF, ...) and writes C1 controls as `\u0085`.
  The local sites call `go_quote` (`symdesk-core/src/config.rs:785-810`,
  `symdesk-cli/src/cobra/quote.rs`), which follows `strconv.Quote`.
- Attribute values (`conflict_cli.rs:55-60`): local quotes a path only when it
  has whitespace; CoreKit `text_string` also quotes `"`, `\` and `=`. Output
  differs for such paths (local is the frozen behavior here).
- `Logger::log_at` could carry a locally computed timestamp, but its doc
  (`log lib.rs:164-166`) marks it as not a production clock policy, it would
  still need the local timestamp code, and it inherits the quoting gap, so it
  removes nothing worth the dependency.

The fixtures normalize `time=$TIME` (cli-ingest-bodies-process.json:3185), so
they do not catch the zone and quoting differences; adoption would silently
change stderr for non-UTC hosts and for messages with non-printable runes.

### 4. Ordered env lookups vs `symaira-core-env`

No real duplicate exists. `git grep` over `crates/**/*.rs` finds no
`getenv(primary, legacy...)` style chain: `PAPERLESS_URL`, `PAPERLESS_TOKEN`
(`symdesk-ingest/src/paperless.rs:160,165`), `SYMDESK_VAULT`, `SYMDESK_LLM_API_KEY`,
`SYMDESK_SIDECAR` are single-name lookups, and `retrieval_config.rs` takes an
explicit `BTreeMap` environment snapshot, not process-env aliases.
`symaira-core-env::getenv` therefore has no call site to replace here.

## Upstream gaps (for symaira-corekit)

1. `symaira-core-secretref`: Go-parity error text is missing (`(reference ...)`
   suffix, `resolve <ref>:` prefix, `symvault binary not found on PATH`, helper
   stderr detail, `exit status N`, timeout wording); keychain account/control
   char validation is stricter than the Go behavior recorded in the desktop fixtures.
2. `symaira-core-log`: no local-time (host offset, `TZ=UTC` aware) timestamp
   policy in `Logger::new`; text quoting is not `strconv.Quote` compatible for
   non-printable runes and C1 controls.

## Checks run

- `git rev-parse HEAD` = `f24377a4...`, `merge-base --is-ancestor f24377a4 HEAD` ok, `dev-external --status` healthy.
- Scratch probe crate (pinned CoreKit git dependencies, outside the repository):
  `cargo run` produced the CoreKit outputs quoted above.
- No Rust source or manifest changed, so no crate test or clippy run applies;
  the coordinator's full workspace gate remains pending.
