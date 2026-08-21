package security

import "regexp"

var (
	emailPattern = regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`)
	phonePattern = regexp.MustCompile(`\+?\d[\d\-\s().]{7,}\d`)
	ipv4Pattern  = regexp.MustCompile(`^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}$`)
)

// Redact masks email- and phone-like substrings in s. It is applied as a
// last line of defense at the CLI/MCP/console error-output boundary (see
// internal/cli.Run, internal/mcp.errorToResponse, internal/console.writeErr)
// so a stray contact value can never reach logs, errors or doctor output
// even if a call site forgot to keep it out — see the data-ownership
// boundary in ARCHITECTURE.md and docs/PRIVACY.md.
func Redact(s string) string {
	s = emailPattern.ReplaceAllString(s, "[redacted-email]")
	s = phonePattern.ReplaceAllStringFunc(s, func(match string) string {
		// A dotted-quad IPv4 address (as seen in "bind: address already in
		// use" style errors from the console's network listener) matches
		// the phone digit/separator pattern above but is not contact data —
		// leave it untouched instead of obscuring genuine diagnostics.
		if ipv4Pattern.MatchString(match) {
			return match
		}
		return "[redacted-phone]"
	})
	return s
}
