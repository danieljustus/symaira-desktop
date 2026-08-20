package paperlessimport

import (
	"testing"

	"github.com/danieljustus/symaira-ingest/internal/paperless"
)

func TestPaperlessDownloadExtensionWithMetadata_Precedence(t *testing.T) {
	tests := []struct {
		name string
		doc  paperless.Document
		meta paperless.DownloadMetadata
		want string
	}{
		{
			name: "response filename wins",
			doc:  paperless.Document{ArchivedFileName: "archive.pdf", OriginalFileName: "original.docx", FileType: "xlsx"},
			meta: paperless.DownloadMetadata{Filename: "download.csv"},
			want: ".csv",
		},
		{
			name: "archived filename",
			doc:  paperless.Document{ArchivedFileName: "archive.pdf", OriginalFileName: "original.docx", FileType: "xlsx"},
			want: ".pdf",
		},
		{
			name: "original filename",
			doc:  paperless.Document{OriginalFileName: "original.docx", FileType: "xlsx"},
			want: ".docx",
		},
		{
			name: "file type",
			doc:  paperless.Document{FileType: "XLSX"},
			want: ".xlsx",
		},
		{
			name: "response content type",
			doc:  paperless.Document{MimeType: "application/pdf"},
			meta: paperless.DownloadMetadata{ContentType: "text/csv; charset=utf-8"},
			want: ".csv",
		},
		{
			name: "metadata MIME",
			doc:  paperless.Document{MimeType: "application/pdf"},
			want: ".pdf",
		},
		{
			name: "unsupported metadata",
			doc:  paperless.Document{MimeType: "application/zip"},
			meta: paperless.DownloadMetadata{ContentType: "application/zip"},
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := paperlessDownloadExtensionWithMetadata(tc.doc, tc.meta); got != tc.want {
				t.Fatalf("extension = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNormalizeExtension(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"dotted", ".PDF", ".pdf"},
		{"bare", "DOCX", ".docx"},
		{"unix path", "archive/report.TXT", ".txt"},
		{"windows path", `archive\\report.csv`, ".csv"},
		{"no extension", "report", ".report"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeExtension(tc.in); got != tc.want {
				t.Fatalf("normalizeExtension(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
