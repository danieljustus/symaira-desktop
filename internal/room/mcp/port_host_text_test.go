package mcp

import "testing"

// The Windows branch of unixRecordedOSErrors cannot run on a unix host, so the
// pure translation is exercised here with the exact text Go prints on Windows.
func TestUnixRecordedOSErrorsMapsWindowsWording(t *testing.T) {
	tests := []struct {
		name, in, want string
		windows        bool
	}{
		{
			name:    "missing room.toml",
			in:      `read room.toml: open ${ROOM}\room.toml: The system cannot find the file specified.`,
			want:    `read room.toml: open ${ROOM}/room.toml: no such file or directory`,
			windows: true,
		},
		{
			name:    "missing artifact",
			in:      `compute sha256: open ${ARTIFACT_ROOT}\nonexistent.txt: The system cannot find the file specified.`,
			want:    `compute sha256: open ${ARTIFACT_ROOT}/nonexistent.txt: no such file or directory`,
			windows: true,
		},
		{
			name:    "directory read",
			in:      `compute sha256: read ${ARTIFACT_ROOT}: Incorrect function.`,
			want:    `compute sha256: read ${ARTIFACT_ROOT}: is a directory`,
			windows: true,
		},
		{
			name:    "nested separators",
			in:      `open ${ROOM}\a\b.txt: The system cannot find the file specified. and ${ROOM}\keep`,
			want:    `open ${ROOM}/a/b.txt: no such file or directory and ${ROOM}\keep`,
			windows: true,
		},
		{name: "unrelated text", in: `path is outside artifact root`, want: `path is outside artifact root`, windows: true},
		{name: "unplaceholdered path", in: `open C:\x\y: The system cannot find the file specified.`, want: `open C:\x\y: The system cannot find the file specified.`, windows: true},
		{name: "unix host is untouched", in: `open ${ROOM}\room.toml: The system cannot find the file specified.`, want: `open ${ROOM}\room.toml: The system cannot find the file specified.`},
		{name: "unix wording is a fixed point", in: `open ${ROOM}/room.toml: no such file or directory`, want: `open ${ROOM}/room.toml: no such file or directory`, windows: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := unixRecordedOSErrors(tt.in, tt.windows); got != tt.want {
				t.Fatalf("unixRecordedOSErrors(%q, %v) = %q, want %q", tt.in, tt.windows, got, tt.want)
			}
		})
	}
}
