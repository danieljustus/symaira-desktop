package security

import (
	"strings"
	"testing"
)

func TestRedact_MasksEmailAndPhone(t *testing.T) {
	in := "conflict on ada@example.com and +1 (555) 123-4567 for person p1"
	out := Redact(in)

	if strings.Contains(out, "ada@example.com") {
		t.Errorf("Redact() did not mask email: %q", out)
	}
	if strings.Contains(out, "555") {
		t.Errorf("Redact() did not mask phone: %q", out)
	}
	if !strings.Contains(out, "[redacted-email]") || !strings.Contains(out, "[redacted-phone]") {
		t.Errorf("Redact() = %q, want redaction markers", out)
	}
	if !strings.Contains(out, "person p1") {
		t.Errorf("Redact() over-redacted non-sensitive text: %q", out)
	}
}

func TestRedact_LeavesPlainTextUntouched(t *testing.T) {
	in := "person not found"
	if out := Redact(in); out != in {
		t.Errorf("Redact(%q) = %q, want unchanged", in, out)
	}
}

// TestRedact_LeavesIPv4AddressesUntouched guards against a false positive
// found while testing the console's bind-error diagnostics: a loopback
// address like 127.0.0.1 matches the phone digit/separator pattern but is
// not contact data — see internal/console.Listen.
func TestRedact_LeavesIPv4AddressesUntouched(t *testing.T) {
	in := "listen tcp 127.0.0.1:8791: bind: address already in use"
	if out := Redact(in); out != in {
		t.Errorf("Redact(%q) = %q, want unchanged (IPv4 address is not phone data)", in, out)
	}
}
