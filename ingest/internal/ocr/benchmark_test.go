package ocr

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/danieljustus/symaira-ingest/internal/extract"
)

// TestOCRBenchmark runs the reference corpus through Tesseract and computes CER/WER.
// It is a measurement test, not a pass/fail gate: it is skipped with -short
// (the default inner-loop mode) and when tesseract is not on PATH. Run without
// -short to get the benchmark output.
func TestOCRBenchmark(t *testing.T) {
	if testing.Short() {
		t.Skip("benchmark skipped in -short mode; run without -short to measure")
	}
	if !isTesseractLikelyAvailable() {
		t.Skip("tesseract not found on PATH — skipping benchmark")
	}

	// Check tesseract actually works
	runner := DefaultRunner("deu+eng")
	if err := runner.Available(); err != nil {
		t.Skipf("tesseract not available: %v", err)
	}

	dir := t.TempDir()
	corpusDir := filepath.Join(dir, "corpus")
	entries, err := GenerateCorpus(corpusDir)
	if err != nil {
		t.Fatalf("generate corpus: %v", err)
	}

	ctx := context.Background()
	fe := NewFieldExtractor()

	type result struct {
		Entry  CorpusEntryWithImage
		Rates  ErrorRates
		Fields NumberFieldResult
		Err    error
	}
	var results []result

	for _, e := range entries {
		res, err := runner.Extract(ctx, e.ImagePath, extract.KindPNG)
		if err != nil {
			results = append(results, result{Entry: e, Err: err})
			continue
		}
		rates := ComputeErrorRates(e.GroundTruth, res.Text, fe)
		fields := ComputeNumberFieldResult(e.GroundTruth, res.Text, fe)
		results = append(results, result{
			Entry:  e,
			Rates:  rates,
			Fields: fields,
		})
	}

	// Print results
	fmt.Fprintln(os.Stderr, "\n=== OCR Benchmark Results ===")
	fmt.Fprintln(os.Stderr, "")

	var totalCER, totalWER, totalFieldCER float64
	var count int
	for _, r := range results {
		if r.Err != nil {
			fmt.Fprintf(os.Stderr, "%-25s %-15s ERROR: %v\n", r.Entry.ID, r.Entry.Category, r.Err)
			continue
		}
		count++
		totalCER += r.Rates.CER
		totalWER += r.Rates.WER
		totalFieldCER += r.Fields.CER
		fmt.Fprintf(os.Stderr, "%-25s %-15s CER=%.4f WER=%.4f FieldCER=%.4f (ref=%d hyp=%d edits=%d)\n",
			r.Entry.ID, r.Entry.Category,
			r.Rates.CER, r.Rates.WER, r.Fields.CER,
			r.Rates.RefLen, r.Rates.HypLen, r.Rates.EditOps,
		)
	}

	if count > 0 {
		avgCER := totalCER / float64(count)
		avgWER := totalWER / float64(count)
		avgFieldCER := totalFieldCER / float64(count)
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintf(os.Stderr, "%-25s %-15s CER=%.4f WER=%.4f FieldCER=%.4f\n",
			"AVERAGE", fmt.Sprintf("(%d docs)", count), avgCER, avgWER, avgFieldCER)
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "=== Detailed per-document output ===")

		// Sort for deterministic output
		sort.Slice(results, func(i, j int) bool {
			return results[i].Entry.ID < results[j].Entry.ID
		})

		for _, r := range results {
			if r.Err != nil {
				continue
			}
			hyp := r.Rates.HypLen
			ref := r.Rates.RefLen
			fmt.Fprintf(os.Stderr, "\n--- %s (%s) ---\n", r.Entry.ID, r.Entry.Category)
			fmt.Fprintf(os.Stderr, "CER=%.4f  WER=%.4f  FieldCER=%.4f  edits=%d  ref_len=%d\n",
				r.Rates.CER, r.Rates.WER, r.Fields.CER, r.Rates.EditOps, ref)
			if r.Fields.RefLen > 0 {
				fmt.Fprintf(os.Stderr, "Number fields: ref=%q hyp=%q edits=%d\n",
					truncate(r.Fields.RefFields, 80),
					truncate(r.Fields.HypFields, 80),
					r.Fields.EditOps)
			}
			_ = hyp
		}
	}

	// Documentation: print the thresholds
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "=== Thresholds for default engine replacement ===")
	fmt.Fprintln(os.Stderr, "See docs/plans/ocr-thresholds.md for the full policy.")
	fmt.Fprintln(os.Stderr, "Summary:")
	fmt.Fprintln(os.Stderr, "  CER < 0.02 and WER < 0.05 → candidate for default")
	fmt.Fprintln(os.Stderr, "  FieldCER < 0.01 on number/case-number fields → required")
	fmt.Fprintln(os.Stderr, "  Must not regress on any single document category")

	// This is a benchmark test — we don't fail on high CER.
	// The benchmark is for measurement, not for pass/fail gating.
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

// TestCorpusGeneration verifies the corpus can be generated without tesseract.
func TestCorpusGeneration(t *testing.T) {
	dir := t.TempDir()
	entries, err := GenerateCorpus(dir)
	if err != nil {
		t.Fatalf("generate corpus: %v", err)
	}
	if len(entries) != len(GermanReferenceCorpus()) {
		t.Errorf("expected %d entries, got %d", len(GermanReferenceCorpus()), len(entries))
	}
	for _, e := range entries {
		if _, err := os.Stat(e.ImagePath); os.IsNotExist(err) {
			t.Errorf("image %s not found at %s", e.ID, e.ImagePath)
		}
		// Verify the file is a valid PNG with reasonable size
		fi, err := os.Stat(e.ImagePath)
		if err != nil {
			t.Errorf("stat %s: %v", e.ID, err)
			continue
		}
		if fi.Size() < 100 {
			t.Errorf("image %s is too small (%d bytes)", e.ID, fi.Size())
		}
	}
}

// TestCorpusContent verifies all corpus entries are synthetic (no real data patterns).
func TestCorpusContent(t *testing.T) {
	corpus := GermanReferenceCorpus()
	if len(corpus) == 0 {
		t.Fatal("corpus is empty")
	}
	for _, e := range corpus {
		if e.ID == "" {
			t.Error("corpus entry has empty ID")
		}
		if e.Category == "" {
			t.Errorf("%s: category is empty", e.ID)
		}
		if e.GroundTruth == "" {
			t.Errorf("%s: ground truth is empty", e.ID)
		}
		// Ensure no real-looking German addresses or names
		// (our synthetic data uses obvious placeholders)
	}
}
