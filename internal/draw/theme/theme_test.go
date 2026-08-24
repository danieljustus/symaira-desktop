package theme

import (
	"testing"
)

func TestThemeHexValuesPinned(t *testing.T) {
	// Pins exact brand values from symaira-appkit / SymairaTheme
	dark := Resolve("symaira-dark")
	if dark.Background != "#0D0C0A" {
		t.Errorf("expected bgDark #0D0C0A, got %s", dark.Background)
	}
	if dark.Primary != "#E5C397" {
		t.Errorf("expected goldPrimary #E5C397, got %s", dark.Primary)
	}
	if dark.Secondary != "#F8E6CD" {
		t.Errorf("expected goldSecondary #F8E6CD, got %s", dark.Secondary)
	}
	if dark.Text != "#F5F4F0" {
		t.Errorf("expected textPrimary #F5F4F0, got %s", dark.Text)
	}
	if dark.Accent != "#70A5D6" {
		t.Errorf("expected iceSecondary #70A5D6, got %s", dark.Accent)
	}

	light := Resolve("symaira-light")
	if light.Background != "#FFFFFF" {
		t.Errorf("expected light bg #FFFFFF, got %s", light.Background)
	}
	if light.Primary != "#1E3D59" {
		t.Errorf("expected light primary #1E3D59, got %s", light.Primary)
	}

	report := Resolve("report")
	if report.Primary != "#1F4E79" {
		t.Errorf("expected report accent #1F4E79, got %s", report.Primary)
	}

	meeting := Resolve("meeting")
	if meeting.Primary != "#1E3D59" {
		t.Errorf("expected meeting accent #1E3D59, got %s", meeting.Primary)
	}
}

func TestThemeResolution(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"symaira-dark", "symaira-dark"},
		{"SYMAIRA-DARK", "symaira-dark"},
		{"dark", "symaira-dark"},
		{"light", "symaira-light"},
		{"report", "report"},
		{"meeting", "meeting"},
		{"behoerde", "behoerde"},
		{"brief", "brief"},
		{"unknown-custom", "symaira-dark"},
	}

	for _, tc := range tests {
		th := Resolve(tc.input)
		if th.Name != tc.expected {
			t.Errorf("Resolve(%q) = %q, expected %q", tc.input, th.Name, tc.expected)
		}
	}
}
