# CLI additions: commands the Go oracle never had

## Rule

- The Go recordings under `testdata/port/cli/` (`cobra-help.json`, the command
  tree, the process fixtures) are history. They are never edited or
  regenerated; `make port-contract` fails on any drift.
- New CLI surface is declared in one reviewed file next to the product code,
  `crates/symdesk-cli/assets/cli-additions.json`. Every help document the
  product prints is `recorded document + declared additions`
  (`src/cli_additions.rs`); each addition inserts one list line, all other
  bytes still come from the recording.
- Tests compare the product with the recording *through* the declared
  additions, so drift fails both ways: an undeclared change to a recorded
  document, and a declared addition that changes more than its own line.

## What an addition declares

`parent` (help key of the recorded node: `""` is the root, `meeting` is
`symdesk meeting`), `name`, `group` (heading of the command list in the parent's
document: `Maintenance Commands` at the root, `Available Commands` below it),
`short` (one line) and `help`: `{"source":"symroom"}` (the linked-in journal's
own usage, root `room` only) or `{"source":"text","text":"...\n"}`.

Insertion follows Cobra: alphabetical in the group, name column
`max(11, longest name)` wide, every list in that document re-padded if the new
name is longer. A missing parent or heading, a name already listed in that
document, or a malformed entry is an error (the product panics on first use,
the tests fail first).

Declared today: `room` (root, `Maintenance Commands`, between `retention` and
`sidecar`). `symdesk room <args>` runs before Cobra (`dispatch_room`);
`symdesk help room [words]` runs the same journal usage as
`symdesk room [words] --help`.

## Add a root command

1. Implement it; route it before Cobra if the recorded tree would reject it
   (`dispatch_room`, `backup_cli::intercept`).
2. Add the entry to `cli-additions.json`.
3. Add it to `DECLARED` in `tests/cli_additions.rs`: that list is the reviewer's
   view of every new command. The replay tests apply the file themselves.

## Add a subcommand under a recorded node (`symdesk meeting import`)

Same entry with `"parent":"meeting"`, `"group":"Available Commands"` and a
`text` help. That lists it in `symdesk meeting --help` / `help meeting` and
answers `symdesk help meeting import`. The table does not execute anything: the
worker must also intercept `meeting import ...` (including `--help`) before
`cobra::front`, because the recorded tree answers `unknown command "import"`.
`tests/cli_additions.rs` already exercises this insertion on the real
`meeting` document with a synthetic entry.

## Deliberately not supported

- Editing, replacing, renaming, hiding or reordering a recorded command, flag
  or document. An addition only inserts one line into an existing list.
- Aliases, hidden commands, new flags on recorded nodes, new global flags.
- Completion stays frozen. The generated bash/zsh/fish/powershell scripts are
  Cobra's generic dynamic scripts without a single command name, so there is
  nothing to extend (pinned byte for byte). `__complete` keeps replaying the
  2745 recorded cases, and `Did you mean` suggestions are recorded too.
  Consequence: `room` is listed in help but not offered by TAB yet; extending
  `__complete` needs expected values in `cobra_parse` and is a separate decision.
- A `symroom` help source anywhere but the root `room`.

## Test changes

- `tests/help.rs`, `every_command_help_and_completion_match_go_process`: the
  four cases printing the root document or root usage (`--help`, `help`, no
  arguments, `help not-a-command`) expect the recorded text with the additions
  applied; a counter asserts exactly these four were rewritten. The other 420
  cases and the 424/207 counts are unchanged.
- `tests/cobra_parse.rs`, `load()`: the documents behind `@help:<key>` and
  `@root_usage` get the additions applied (110 root-document and 30 root-usage
  cases); literal text of every case, test count and case count are unchanged.
- New `tests/cli_additions.rs` (outside the frozen manifest): reviewed list,
  no shadowing, rejection of shadowing/malformed entries, insertion under
  `meeting`, byte identity of every untouched key through the process,
  completion scripts frozen, `help room` equals `room --help`.
- `scripts/rust-port/frozen-contract.json`: unchanged; no pinned command gained
  or lost a test, and no recording changed.
