package press

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckOverwrite(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(t *testing.T) string
		overwrite bool
		wantErr   string
	}{
		{
			name: "missing output is allowed",
			setup: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "missing.pdf")
			},
		},
		{
			name: "directory is always refused",
			setup: func(t *testing.T) string {
				return t.TempDir()
			},
			wantErr: "is a directory",
		},
		{
			name: "existing PDF is allowed",
			setup: func(t *testing.T) string {
				return writeOutput(t, "%PDF-1.7\ncontent")
			},
		},
		{
			name: "non-PDF is refused by default",
			setup: func(t *testing.T) string {
				return writeOutput(t, "important data")
			},
			wantErr: "refusing to overwrite existing non-PDF",
		},
		{
			name: "non-PDF can be explicitly replaced",
			setup: func(t *testing.T) string {
				return writeOutput(t, "important data")
			},
			overwrite: true,
		},
		{
			name: "short existing file is not a PDF",
			setup: func(t *testing.T) string {
				return writeOutput(t, "%PD")
			},
			wantErr: "refusing to overwrite existing non-PDF",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckOverwrite(tt.setup(t), tt.overwrite)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("CheckOverwrite() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("CheckOverwrite() error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func writeOutput(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "output")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
