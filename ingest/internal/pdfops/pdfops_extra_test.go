package pdfops

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultTools(t *testing.T) {
	tools := DefaultTools()
	if tools.PDFInfo != "pdfinfo" {
		t.Errorf("PDFInfo = %q, want pdfinfo", tools.PDFInfo)
	}
	if tools.PDFSeparate != "pdfseparate" {
		t.Errorf("PDFSeparate = %q, want pdfseparate", tools.PDFSeparate)
	}
	if tools.PDFUnite != "pdfunite" {
		t.Errorf("PDFUnite = %q, want pdfunite", tools.PDFUnite)
	}
	if tools.QPDF != "qpdf" {
		t.Errorf("QPDF = %q, want qpdf", tools.QPDF)
	}
}

func TestMerge_TwoFiles(t *testing.T) {
	dir := t.TempDir()
	input1 := filepath.Join(dir, "a.pdf")
	input2 := filepath.Join(dir, "b.pdf")
	output := filepath.Join(dir, "merged.pdf")

	if err := os.WriteFile(input1, []byte("%PDF-1.4\na\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input2, []byte("%PDF-1.4\nb\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	pdfunite := writeExecutable(t, dir, "pdfunite", `
last=""
for arg in "$@"; do last="$arg"; done
cat "$1" "$2" > "$last"
`)
	tools := Tools{PDFUnite: pdfunite}

	err := tools.Merge(context.Background(), []string{input1, input2}, output)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "%PDF-") {
		t.Errorf("output is not PDF-like: %q", data)
	}
}

func TestMerge_LessThanTwoInputs(t *testing.T) {
	dir := t.TempDir()
	tools := Tools{PDFUnite: "pdfunite"}

	err := tools.Merge(context.Background(), []string{filepath.Join(dir, "a.pdf")}, filepath.Join(dir, "out.pdf"))
	if err == nil {
		t.Fatal("expected error for single input")
	}
	if !strings.Contains(err.Error(), "at least two") {
		t.Errorf("error = %q, want 'at least two'", err.Error())
	}
}

func TestMerge_MissingInput(t *testing.T) {
	dir := t.TempDir()
	tools := Tools{PDFUnite: "pdfunite"}

	err := tools.Merge(context.Background(), []string{
		filepath.Join(dir, "nonexistent1.pdf"),
		filepath.Join(dir, "nonexistent2.pdf"),
	}, filepath.Join(dir, "out.pdf"))
	if err == nil {
		t.Fatal("expected error for missing input")
	}
}

func TestMerge_EmptyInputs(t *testing.T) {
	tools := Tools{PDFUnite: "pdfunite"}
	err := tools.Merge(context.Background(), nil, "/tmp/out.pdf")
	if err == nil {
		t.Fatal("expected error for nil inputs")
	}
}

func TestRotate_InvalidDegrees(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input.pdf")
	os.WriteFile(input, []byte("%PDF-1.4\n"), 0o600)
	tools := Tools{QPDF: "qpdf"}

	for _, deg := range []int{0, 45, 100, -45, 360, 91} {
		err := tools.Rotate(context.Background(), input, filepath.Join(dir, "out.pdf"), deg, "")
		if err == nil {
			t.Errorf("Rotate(%d) should fail", deg)
		}
	}
}

func TestRotate_ValidDegreesWithoutQPDF(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input.pdf")
	os.WriteFile(input, []byte("%PDF-1.4\n"), 0o600)

	// Use a fake qpdf that succeeds
	qpdf := writeExecutable(t, dir, "qpdf", `cp "$2" "$3"`)
	tools := Tools{QPDF: qpdf}
	output := filepath.Join(dir, "rotated.pdf")

	// We need to write a real-looking PDF for requireFile
	os.WriteFile(input, []byte("%PDF-1.4\nvalid\n"), 0o600)

	err := tools.Rotate(context.Background(), input, output, 90, "")
	if err != nil {
		t.Fatalf("Rotate(90): %v", err)
	}
	if _, err := os.Stat(output); err != nil {
		t.Errorf("output not created: %v", err)
	}
}

func TestRotate_WithPageSpec(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input.pdf")
	os.WriteFile(input, []byte("%PDF-1.4\nvalid\n"), 0o600)

	qpdf := writeExecutable(t, dir, "qpdf", `cp "$2" "$3"`)
	tools := Tools{QPDF: qpdf}
	output := filepath.Join(dir, "rotated.pdf")

	err := tools.Rotate(context.Background(), input, output, 180, "1-2")
	if err != nil {
		t.Fatalf("Rotate(180, 1-2): %v", err)
	}
}

func TestRotate_InvalidPageSpec(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input.pdf")
	os.WriteFile(input, []byte("%PDF-1.4\nvalid\n"), 0o600)

	tools := Tools{QPDF: "qpdf"}
	output := filepath.Join(dir, "rotated.pdf")

	err := tools.Rotate(context.Background(), input, output, 90, "invalid")
	if err == nil {
		t.Fatal("expected error for invalid page spec")
	}
}

func TestCommandError_WithStderr(t *testing.T) {
	err := commandError("/usr/bin/tool", "something went wrong", nil)
	if !strings.Contains(err.Error(), "something went wrong") {
		t.Errorf("error = %q, want stderr message", err.Error())
	}
}

func TestCommandError_EmptyStderr(t *testing.T) {
	inner := context.DeadlineExceeded
	err := commandError("/usr/bin/tool", "", inner)
	if !strings.Contains(err.Error(), inner.Error()) {
		t.Errorf("error = %q, want inner error message", err.Error())
	}
}

func TestRequireFile_Nonexistent(t *testing.T) {
	err := requireFile("/nonexistent/file.pdf")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestRequireFile_IsDirectory(t *testing.T) {
	dir := t.TempDir()
	err := requireFile(dir)
	if err == nil {
		t.Fatal("expected error for directory")
	}
	if !strings.Contains(err.Error(), "is a directory") {
		t.Errorf("error = %q, want 'is a directory'", err.Error())
	}
}

func TestToolPath_Empty(t *testing.T) {
	_, err := toolPath("", "pdfinfo")
	if err == nil {
		t.Fatal("expected error for empty path")
	}
	if !strings.Contains(err.Error(), "not configured") {
		t.Errorf("error = %q, want 'not configured'", err.Error())
	}
}

func TestToolPath_NotInPATH(t *testing.T) {
	_, err := toolPath("definitely-not-a-real-binary-xyz", "pdfinfo")
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
	if !strings.Contains(err.Error(), "not found in PATH") {
		t.Errorf("error = %q, want 'not found in PATH'", err.Error())
	}
}

func TestParsePages_AdditionalCases(t *testing.T) {
	cases := []struct {
		spec    string
		want    []int
		wantErr bool
	}{
		{"1", []int{1}, false},
		{"3-5", []int{3, 4, 5}, false},
		{"5,3,1", []int{1, 3, 5}, false},
		{" 1 , 2 ", []int{1, 2}, false},
		{"0", nil, true},
		{"-1", nil, true},
		{"3-1", nil, true},
		{"1,1", nil, true},
		{"1-2-3", nil, true},
		{"abc", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.spec, func(t *testing.T) {
			got, err := ParsePages(tc.spec)
			if tc.wantErr {
				if err == nil {
					t.Errorf("ParsePages(%q) = %v, want error", tc.spec, got)
				}
			} else {
				if err != nil {
					t.Errorf("ParsePages(%q) error: %v", tc.spec, err)
				}
				if len(got) != len(tc.want) {
					t.Errorf("ParsePages(%q) = %v, want %v", tc.spec, got, tc.want)
				} else {
					for i := range tc.want {
						if got[i] != tc.want[i] {
							t.Errorf("ParsePages(%q)[%d] = %d, want %d", tc.spec, i, got[i], tc.want[i])
						}
					}
				}
			}
		})
	}
}

func TestSplit_MissingInputFile(t *testing.T) {
	dir := t.TempDir()
	tools := Tools{
		PDFInfo:     writeExecutable(t, dir, "pdfinfo", "printf 'Pages: 3\\n'"),
		PDFSeparate: writeExecutable(t, dir, "pdfseparate", ""),
		PDFUnite:    writeExecutable(t, dir, "pdfunite", ""),
	}
	_, err := tools.Split(context.Background(), filepath.Join(dir, "nonexistent.pdf"), "1", filepath.Join(dir, "out"))
	if err == nil {
		t.Fatal("expected error for missing input file")
	}
}

func TestSplit_OutsideDocumentPage(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input.pdf")
	os.WriteFile(input, []byte("%PDF-1.4\n"), 0o600)

	tools := Tools{
		PDFInfo: writeExecutable(t, dir, "pdfinfo", "printf 'Pages: 2\\n'"),
	}
	_, err := tools.Split(context.Background(), input, "5", filepath.Join(dir, "out"))
	if err == nil {
		t.Fatal("expected error for page outside document")
	}
	if !strings.Contains(err.Error(), "outside the document") {
		t.Errorf("error = %q, want 'outside the document'", err.Error())
	}
}
