# Current-build native version records (#938)

`SYMDESK_NATIVE_RECORD` names a **new evidence directory** consumed by
`scripts/rust-port/native-record.py`, via `make native-record`. It is a harness
contract; the shipped CLI does not read this variable or write recording files.
An absent variable or existing directory fails rather than reusing prior samples.

The target builds the current Rust source with the current `VERSION` from Make
(the repository release tag by default), then invokes that binary three times
with `version --json`. Each invocation has a different empty HOME, USERPROFILE,
XDG config/data/cache and TMPDIR/TMP/TEMP, and an empty working directory.
`NATIVE_RECORD_TEMP_ROOT` must name an existing directory outside `/private/tmp`.
No historical invocation, fixture, `.state/pass`, or user vault is consulted.

Each sample must exit successfully, emit one valid JSON document with the
current build version and schema version 1, and leave stderr empty. The harness
retains stdout/stderr verbatim, plus a JSON record of platform, architecture,
UTC time, binary SHA-256 and all three parsed documents. It verifies that the
binary did not change during sampling. Harness stdout is JSON; failures go to
stderr. Failed samples retain diagnostic files without a success record.

```sh
SYMDESK_NATIVE_RECORD="$PWD/native-evidence-new" \
NATIVE_RECORD_TEMP_ROOT="$PWD" make native-record
```

Native CI runs this target on both macOS architectures and uploads the evidence.
Linux checks exercise the recording and failure controls but do not certify
macOS execution. This is version-output evidence, not a performance or cutover
acceptance gate; historical VALUE reports retain their original conclusions.
