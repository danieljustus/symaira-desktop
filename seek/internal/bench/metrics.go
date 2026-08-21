// Package bench provides a retrieval-quality evaluation harness for Symaira-Seek.
//
// It implements offline evaluation metrics (Hit Rate@k, Mean Reciprocal Rank,
// Normalized Discounted Cumulative Gain@k) used to measure retrieval quality
// against a labeled query→relevant-document judgment set.
package bench

import "math"

// HitRateAtK returns 1 if any relevant document appears in the top-k results, 0 otherwise.
// actual is the ordered list of result document IDs. relevant is the set of ground-truth
// relevant document IDs. Ground-truth matching is exact on document ID.
func HitRateAtK(actual []string, relevant map[string]bool, k int) float64 {
	if k <= 0 || len(actual) == 0 || len(relevant) == 0 {
		return 0
	}
	if k > len(actual) {
		k = len(actual)
	}
	for i := 0; i < k; i++ {
		if relevant[actual[i]] {
			return 1.0
		}
	}
	return 0.0
}

// MRR computes Mean Reciprocal Rank. It returns the reciprocal rank (1/rank)
// of the first relevant result, or 0 if no relevant result was found.
// actual is the ordered list of result document IDs. relevant is the set of
// ground-truth relevant document IDs. Ranks are 1-indexed.
func MRR(actual []string, relevant map[string]bool) float64 {
	if len(actual) == 0 || len(relevant) == 0 {
		return 0
	}
	for i, id := range actual {
		if relevant[id] {
			return 1.0 / float64(i+1)
		}
	}
	return 0.0
}

// NDCGAtK computes Normalized Discounted Cumulative Gain at k.
// actual is the ordered list of result document IDs. relevant is the ground-truth
// relevant set. ratings maps document ID → relevance grade (higher = more relevant).
// For queries where no document is relevant, NDCG@k is defined as 0.
func NDCGAtK(actual []string, relevant map[string]bool, k int, ratings map[string]float64) float64 {
	if k <= 0 || len(actual) == 0 || len(relevant) == 0 {
		return 0
	}
	if k > len(actual) {
		k = len(actual)
	}

	// Compute DCG@k.
	var dcg float64
	for i := 0; i < k; i++ {
		id := actual[i]
		rel := 0.0
		if relevant[id] {
			if r, ok := ratings[id]; ok {
				rel = r
			} else {
				rel = 1.0 // default binary relevance
			}
		}
		if i == 0 {
			dcg += rel
		} else {
			dcg += rel / math.Log2(float64(i+1))
		}
	}

	// Compute ideal DCG@k: sort available relevant docs by rating descending.
	idealRels := make([]float64, 0, len(relevant))
	for id := range relevant {
		rel := 1.0 // default binary relevance
		if r, ok := ratings[id]; ok {
			rel = r
		}
		idealRels = append(idealRels, rel)
	}
	// Sort descending using simple insertion sort (small set).
	for i := 1; i < len(idealRels); i++ {
		key := idealRels[i]
		j := i - 1
		for j >= 0 && idealRels[j] < key {
			idealRels[j+1] = idealRels[j]
			j--
		}
		idealRels[j+1] = key
	}

	var idcg float64
	numIdeal := k
	if numIdeal > len(idealRels) {
		numIdeal = len(idealRels)
	}
	for i := 0; i < numIdeal; i++ {
		if i == 0 {
			idcg += idealRels[i]
		} else {
			idcg += idealRels[i] / math.Log2(float64(i+1))
		}
	}

	if idcg == 0 {
		return 0.0
	}
	return dcg / idcg
}
