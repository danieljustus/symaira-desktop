package documentformat

import (
	"strings"
	"testing"
)

func TestFormatContractHandlesNormalizationAndUnknownExtensions(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{input: " PDF ", want: ".pdf"},
		{input: "markdown", want: ".markdown"},
		{input: ".TXT", want: ".txt"},
		{input: "", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			if got := NormalizeExtension(tc.input); got != tc.want {
				t.Fatalf("NormalizeExtension(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}

	if _, ok := Lookup("unknown"); ok {
		t.Fatal("Lookup recognized an unknown extension")
	}
	if kind, ok := KindForExtension("unknown"); ok || kind != "" {
		t.Fatalf("KindForExtension(unknown) = %q, %v, want empty, false", kind, ok)
	}
	if reason, ok := UnsupportedReason("unknown"); ok || reason != "" {
		t.Fatalf("UnsupportedReason(unknown) = %q, %v, want empty, false", reason, ok)
	}
	if IsRecognizedDocument(".unknown") {
		t.Fatal("unknown extension reported as recognized")
	}
}

func TestFormatContractDistinguishesUnsupportedKinds(t *testing.T) {
	for _, tc := range []struct {
		name          string
		kind          Kind
		unsupported   bool
		recognizedExt string
		wantError     string
	}{
		{name: "unsupported", kind: KindMOBI, unsupported: true, recognizedExt: ".mobi", wantError: "unsupported document format MOBI"},
		{name: "supported", kind: KindPDF, unsupported: false, recognizedExt: ".pdf"},
		{name: "unknown", kind: Kind("application/x-unknown"), unsupported: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsUnsupportedKind(tc.kind); got != tc.unsupported {
				t.Fatalf("IsUnsupportedKind(%q) = %v, want %v", tc.kind, got, tc.unsupported)
			}
			if tc.recognizedExt != "" && !IsRecognizedDocument(tc.recognizedExt) {
				t.Fatalf("IsRecognizedDocument(%q) = false, want true", tc.recognizedExt)
			}
			err := UnsupportedFormatError(tc.kind)
			if tc.wantError == "" {
				if err == nil || !strings.Contains(err.Error(), `unsupported document format "`+string(tc.kind)+`"`) {
					t.Fatalf("UnsupportedFormatError(%q) = %v, want unknown-kind error", tc.kind, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("UnsupportedFormatError(%q) = %v, want text containing %q", tc.kind, err, tc.wantError)
			}
		})
	}
}
