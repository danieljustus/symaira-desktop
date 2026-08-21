package bench

import (
	"testing"
)

func TestHitRateAtK(t *testing.T) {
	relevant := map[string]bool{"a": true, "b": true}

	tests := []struct {
		name   string
		actual []string
		k      int
		want   float64
	}{
		{"first result relevant", []string{"a", "c", "d"}, 3, 1.0},
		{"third result relevant at k=3", []string{"c", "d", "a"}, 3, 1.0},
		{"third result relevant at k=5", []string{"c", "d", "a", "e", "f"}, 5, 1.0},
		{"no relevant in top 3", []string{"c", "d", "e"}, 3, 0.0},
		{"relevant at k=1", []string{"a", "c", "d"}, 1, 1.0},
		{"relevant at k=2 not at k=1", []string{"c", "a"}, 1, 0.0},
		{"k=0 returns 0", []string{"a"}, 0, 0.0},
		{"empty actual returns 0", []string{}, 10, 0.0},
		{"empty relevant returns 0", []string{"c"}, 10, 0.0},
		{"k larger than actual", []string{"a"}, 100, 1.0},
		{"nullable k", []string{"x", "y"}, 1, 0.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HitRateAtK(tt.actual, relevant, tt.k)
			if got != tt.want {
				t.Errorf("HitRateAtK(%v, _, %d) = %v, want %v", tt.actual, tt.k, got, tt.want)
			}
		})
	}
}

func TestMRR(t *testing.T) {
	relevant := map[string]bool{"a": true, "b": true}

	tests := []struct {
		name   string
		actual []string
		want   float64
	}{
		{"first result", []string{"a", "c", "d"}, 1.0},
		{"second result", []string{"c", "a", "d"}, 0.5},
		{"third result", []string{"c", "d", "a"}, 1.0 / 3.0},
		{"no relevant", []string{"c", "d", "e"}, 0.0},
		{"empty actual", []string{}, 0.0},
		{"empty relevant", []string{"x"}, 0.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MRR(tt.actual, relevant)
			if got != tt.want {
				t.Errorf("MRR(%v, _) = %v, want %v", tt.actual, got, tt.want)
			}
		})
	}
}

func TestNDCGAtK(t *testing.T) {
	// Relevant docs: a (grade 3), b (grade 2), c (grade 1)
	relevant := map[string]bool{"a": true, "b": true, "c": true}
	ratings := map[string]float64{"a": 3.0, "b": 2.0, "c": 1.0}

	t.Run("perfect ranking at k=3", func(t *testing.T) {
		actual := []string{"a", "b", "c"}
		got := NDCGAtK(actual, relevant, 3, ratings)
		// Ideal ranking is [a,b,c], so DCG = IDCG → NDCG = 1.0
		if got != 1.0 {
			t.Errorf("perfect ranking: got %v, want 1.0", got)
		}
	})

	t.Run("worst ranking at k=3", func(t *testing.T) {
		actual := []string{"c", "b", "a"}
		got := NDCGAtK(actual, relevant, 3, ratings)
		// DCG < IDCG but > 0
		if got <= 0 || got >= 1.0 {
			t.Errorf("worst ranking: got %v, want in (0,1)", got)
		}
	})

	t.Run("k=1 with best doc", func(t *testing.T) {
		actual := []string{"a"}
		got := NDCGAtK(actual, relevant, 1, ratings)
		if got != 1.0 {
			t.Errorf("k=1 best doc: got %v, want 1.0", got)
		}
	})

	t.Run("k=1 with worst doc", func(t *testing.T) {
		actual := []string{"c"}
		got := NDCGAtK(actual, relevant, 1, ratings)
		// DCG = 1, IDCG = 3 (best doc a has relevance 3), so 1/3
		want := 1.0 / 3.0
		if got != want {
			t.Errorf("k=1 worst doc: got %v, want %v", got, want)
		}
	})

	t.Run("no relevant docs returns 0", func(t *testing.T) {
		actual := []string{"x", "y", "z"}
		got := NDCGAtK(actual, relevant, 3, ratings)
		if got != 0.0 {
			t.Errorf("no relevant: got %v, want 0.0", got)
		}
	})

	t.Run("empty actual returns 0", func(t *testing.T) {
		got := NDCGAtK([]string{}, relevant, 3, ratings)
		if got != 0.0 {
			t.Errorf("empty actual: got %v, want 0.0", got)
		}
	})

	t.Run("k=0", func(t *testing.T) {
		got := NDCGAtK([]string{"a"}, relevant, 0, ratings)
		if got != 0.0 {
			t.Errorf("k=0: got %v, want 0.0", got)
		}
	})

	t.Run("binary relevance without ratings", func(t *testing.T) {
		actual := []string{"a", "x", "y"}
		got := NDCGAtK(actual, relevant, 3, map[string]float64{})
		if got <= 0 || got > 1.0 {
			t.Errorf("binary relevance: got %v, want in (0,1]", got)
		}
	})

	t.Run("k>actual defaults to len(actual)", func(t *testing.T) {
		actual := []string{"a", "b"}
		got := NDCGAtK(actual, relevant, 10, ratings)
		if got < 0.9 || got > 1.0 {
			t.Errorf("k larger than actual: got %v, want near 1.0", got)
		}
	})
}
