package contact

import "testing"

func TestNormalize_DispatchesByContactPointKind(t *testing.T) {
	tests := []struct {
		name string
		kind ContactPointKind
		raw  string
		want string
	}{
		{name: "email", kind: ContactPointEmail, raw: " Ada@Example.COM ", want: "ada@example.com"},
		{name: "phone", kind: ContactPointPhone, raw: "+1 (555) 123-4567", want: "+15551234567"},
		{name: "url", kind: ContactPointURL, raw: " HTTPS://EXAMPLE.COM/// ", want: "https://example.com"},
		{name: "handle", kind: ContactPointHandle, raw: " @Ada ", want: "ada"},
		{name: "address", kind: ContactPointAddress, raw: " 123\n Main   Street ", want: "123 main street"},
		{name: "unknown", kind: ContactPointKind("other"), raw: " Value ", want: "value"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Normalize(tt.kind, tt.raw); got != tt.want {
				t.Errorf("Normalize(%q, %q) = %q, want %q", tt.kind, tt.raw, got, tt.want)
			}
		})
	}
}

func TestNormalizePhone_DiscardsNonDigitsExceptLeadingPlus(t *testing.T) {
	if got := NormalizePhone("  +49 30/123-456 ext 7 "); got != "+49301234567" {
		t.Errorf("NormalizePhone() = %q, want +49301234567", got)
	}
	if got := NormalizePhone("49+30"); got != "4930" {
		t.Errorf("NormalizePhone(internal plus) = %q, want 4930", got)
	}
}
