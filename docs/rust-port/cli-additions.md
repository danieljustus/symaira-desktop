# CLI additions: commands the Go oracle never had

## Rule

- The Go recordings under `testdata/port/cli/` (`cobra-help.json`, the command
  tree, the process fixtures) are history. They are never edited or
  regenerated; `make port-contract` fails on any drift.
- New CLI surface is declared in one reviewed file,
  `crates/symdesk-cli/assets/cli-additions.json`, next to the product code.
  Every help document the product prints is `frozen document + declared
  additions` (`crates/symdesk-cli/src/cli_additions.rs`); every byte outside
  the inserted lines still comes from the recording.
- Tests compare the product with the recording *through* the declared
  additions, so drift fails in both directions: an undeclared change to a
  frozen document, and a declared addition that touches more than its own line.

## What an addition declares

`parent` (help key of the frozen node: `""` is the root, `meeting` is
`symdesk meeting`), `name`, `group` (the heading of the command list in the
parent's document, e.g. `Maintenance Commands` at the root, `Available
Commands` below it), `short` (one line) and `help`: `{"source":"symroom"}`
(the journal's own usage, produced by the linked-in symroom) or
`{"source":"text","text":"..."}` (the document itself).

The insertion keeps Cobra's rules: alphabetical inside the group, the name
column is `max(11, longest name)` wide and every list in that document is
re-padded if the new name is longer. A missing parent, heading, a name that
already exists in the document, or a malformed entry is an error (the product
panics on first use; the tests catch it first).

Currently declared: `room` (root, `Maintenance Commands`, between `retention`
and `sidecar`). `symdesk room <args>` itself is dispatched in `main` before any
Cobra parsing (commit 430fc7da); `symdesk help room [words]` runs the same
journal usage as `symdesk room [words] --help`.

## Add a root command

1. Implement it, and route it before Cobra if the frozen tree would reject it
   (`dispatch_room`, `backup_cli::intercept` are the models).
2. Add the entry to `cli-additions.json`.
3. Add it to `DECLARED` in `tests/cli_additions.rs` (that list is the reviewer's
   view of every new command) and, if the product listing changes, nothing else:
   the replay tests apply the file themselves.

## Add a subcommand under a frozen node (`symdesk meeting import`)

Same entry with `"parent":"meeting"`, `"group":"Available Commands"` and a
`text` help. This lists it in `symdesk meeting --help` / `help meeting` and
answers `symdesk help meeting import`. Not done by the table, the new worker
must do it: execution and `symdesk meeting import --help`, because the frozen
tree answers `unknown command "import"`; intercept the invocation before
`cobra::front` like `dispatch_room`. `tests/cli_additions.rs` already exercises
this insertion on the real `meeting` document with a synthetic entry.

## Deliberately not supported

- Editing, replacing, renaming, hiding or reordering a frozen command, flag or
  document; an addition may only insert one line per command into an existing list.
- Aliases, hidden commands, new flags on frozen nodes, new global flags.
- Completion. The generated bash/zsh/fish/powershell scripts are Cobra's generic
  dynamic scripts and contain no command names, so there is nothing to extend;
  they stay byte-identical (pinned by a test). `__complete` keeps replaying the
  2745 recorded Go cases, and the `Did you mean` suggestions of unknown
  commands are frozen as well. Consequence: `room` is listed in help but not
  yet offered by TAB. Extending `__complete` needs expected values in
  `cobra_parse`; a separate decision.
- A `symroom` help source anywhere but the root.

## Test changes

- `tests/help.rs` `every_command_help_and_completion_match_go_process`: the four
  cases that print the root document (`--help`, `help`, no arguments, `help
  not-a-command` via `root_usage`) expect the recorded text with the declared
  additions applied; a counter asserts that exactly these four were rewritten.
  The other 420 cases and all counts are unchanged.
- `tests/cobra_parse.rs` `load()`: the `@help:` / `@root_usage` documents the 19484
  recorded cases resolve to get the additions applied. Number of tests and the
  cases are unchanged; only the root document (and root usage) differ.
- New `tests/cli_additions.rs` (not part of the frozen manifest): line-level
  oracle for every document, no-addition keys byte-equal, completion scripts
  frozen, shadowing rejected, `DECLARED` list.
- `scripts/rust-port/frozen-contract.json`: unchanged. No pinned command gained or
  lost a test.
