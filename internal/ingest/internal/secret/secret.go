// Package secret classifies secret configuration strings for the ingest
// pipeline. Resolution itself lives in the shared
// github.com/danieljustus/symaira-corekit/secretref package; this package
// only keeps the scheme check the mail poller needs for its plaintext
// warning.
//
// Supported schemes:
//   - symvault://<ref>
//   - env://<var_name>
//   - keychain://<service>/<account>
//
// A value without a scheme is a bare reference: secretref resolves it as an
// environment variable name, never as a literal password.
package secret

import "strings"

// IsPlaintext reports whether s carries none of the symvault://, env:// or
// keychain:// schemes. With the shared secretref resolver such a bare value
// is treated as an environment variable name, so a literal password in the
// config no longer resolves as a password at all.
func IsPlaintext(s string) bool {
	return !strings.HasPrefix(s, "env://") &&
		!strings.HasPrefix(s, "symvault://") &&
		!strings.HasPrefix(s, "keychain://")
}
