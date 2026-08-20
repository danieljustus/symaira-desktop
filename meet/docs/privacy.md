# Privacy and local-first operation

`symmeet` is local-first in its first stable release. Meeting audio, transcript artifacts,
speaker labels, and derived Markdown stay on the device by default. The product
does not include telemetry, cloud transcription, accounts, or remote diagnostic
uploads.

Recording must not start automatically. A capture integration must obtain a
fresh interactive authorization for every recording session. The record includes
the operator attestation, notice time, session scope, and expiry; it fails
closed if expired, reused, bound to another session, or carried across a process
restart.

Local model downloads are not meeting-content transmission. Downloading a model
from a user-selected source fetches model bytes to the local cache; it does not
send meeting audio, transcript text, participant names, calendar information,
or artifact files to that source.

Retention policies can keep artifacts, clean derived artifacts after a date, or
clean them after a confirmed export. Cleanup removes derived text, normalized
media caches, and the model-job state together. Partial failures remain visible
and retryable. Moving a meeting to local trash is reversible; permanent
deletion requires an explicit command or reviewed UI action.

Structured logs retain only event/status and opaque meeting or job identifiers.
They redact transcript text, names, calendar details, provider credentials, and
absolute user paths. Encryption at rest is a future option; it is not
claimed or silently improvised here.
