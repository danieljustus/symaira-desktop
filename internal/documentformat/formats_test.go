package documentformat

import "testing"

func TestFormatContractTable(t *testing.T) {
	tests := []struct {
		extension string
		kind      Kind
		supported bool
		reason    string
	}{
		{".pdf", KindPDF, true, ""},
		{".rtf", KindRTF, true, ""},
		{".epub", KindEPUB, true, ""},
		{".mobi", KindMOBI, false, "no bundled MOBI parser; DRM status cannot be determined"},
		{".azw3", KindAZW3, false, "no bundled AZW3 parser; DRM status cannot be determined"},
		{".pages", KindPages, false, "iWork bundle parser is not available"},
		{".key", KindKeynote, false, "iWork bundle parser is not available"},
		{".numbers", KindNumbers, false, "iWork bundle parser is not available"},
		{".doc", KindDOC, false, "legacy binary Office parser is not available"},
		{".xls", KindXLS, false, "legacy binary Office parser is not available"},
		{".ppt", KindPPT, false, "legacy binary Office parser is not available"},
		{".djvu", KindDjVu, false, "DjVu parser is not available"},
		{".odg", KindODG, false, "OpenDocument drawing parser is not available"},
	}
	for _, tt := range tests {
		t.Run(tt.extension, func(t *testing.T) {
			got, ok := KindForExtension(tt.extension)
			if !ok || got != tt.kind {
				t.Fatalf("KindForExtension() = %q, %v; want %q, true", got, ok, tt.kind)
			}
			if IsSupported(tt.extension) != tt.supported {
				t.Fatalf("IsSupported() = %v, want %v", IsSupported(tt.extension), tt.supported)
			}
			reason, hasReason := UnsupportedReason(tt.extension)
			if reason != tt.reason || hasReason != (tt.reason != "") {
				t.Fatalf("UnsupportedReason() = %q, %v; want %q, %v", reason, hasReason, tt.reason, tt.reason != "")
			}
		})
	}
}

func TestSupportedExtensionsSortedAndUnique(t *testing.T) {
	extensions := SupportedExtensions()
	for i, ext := range extensions {
		if ext == "" || ext != NormalizeExtension(ext) {
			t.Fatalf("SupportedExtensions()[%d] = %q is not normalized", i, ext)
		}
		if i > 0 && extensions[i-1] >= ext {
			t.Fatalf("SupportedExtensions() is not sorted and unique: %v", extensions)
		}
	}
}
