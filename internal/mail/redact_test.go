package mail

import "testing"

func TestRedactCredentialsRedactsSymvaultURIs(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "mid-string reference",
			in:   `open symvault://mail/work: connect failed`,
			want: `open <redacted> connect failed`,
		},
		{
			name: "reference to end of string",
			in:   `auth failed for symvault://mail/work`,
			want: `auth failed for <redacted>`,
		},
		{
			name: "no reference stays untouched",
			in:   `plain dial error 1.2.3.4:443`,
			want: `plain dial error 1.2.3.4:443`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := redactCredentials(tc.in); got != tc.want {
				t.Errorf("redactCredentials(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRedactCredentialsRedactsPasswordPatterns(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`login password="s3cret" failed`, `login password="<redacted>" failed`},
		{`config password_secret="hunter2" set`, `config password_secret="<redacted>" set`},
		{`password: "top secret" rejected`, `password: "<redacted>" rejected`},
		// Unterminated value: everything after the prefix is redacted
		// (the marker replaces the rest of the string).
		{`broken password="never-closed`, `broken password="<redacted>never-closed`},
	}
	for _, tc := range cases {
		if got := redactCredentials(tc.in); got != tc.want {
			t.Errorf("redactCredentials(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
