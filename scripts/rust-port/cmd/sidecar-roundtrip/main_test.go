package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCommittedSidecarOracleIdentities(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	read := func(path string, value any) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if err := json.Unmarshal(data, value); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
	}
	var provenance provenanceFixture
	read("testdata/port/provenance.json", &provenance)
	if provenance.Oracle["commit"] == "" || provenance.Oracle["release"] == "" {
		t.Fatal("testdata/port/provenance.json lacks an oracle identity")
	}
	for _, path := range []string{
		"testdata/port/sidecar/roundtrip.json",
		"testdata/port/sidecar/large-corpus.json",
		"testdata/port/sidecar/lifecycle.json",
	} {
		t.Run(path, func(t *testing.T) {
			var metadata provenanceFixture
			read(path, &metadata)
			if !reflect.DeepEqual(metadata.Oracle, provenance.Oracle) {
				t.Fatalf("%s oracle %v differs from testdata/port/provenance.json oracle %v", path, metadata.Oracle, provenance.Oracle)
			}
		})
	}
	var roundTrip fixture
	read("testdata/port/sidecar/roundtrip.json", &roundTrip)
	var manifest largeCorpusFixture
	read("testdata/port/sidecar/large-corpus.json", &manifest)
	if err := validateLargeCorpusManifest(manifest, roundTrip.Oracle, provenance.Oracle); err != nil {
		t.Fatalf("committed testdata/port/sidecar/large-corpus.json rejected by pinned validator: %v", err)
	}
}

func TestRequireSuccessRejectsErrorOutcome(t *testing.T) {
	if err := requireSuccess(helperResult{Outcome: "error", ErrorClass: "readonly"}); err == nil {
		t.Fatal("error outcome must not be accepted as success")
	}
}

func TestRequireSuccessAcceptsOK(t *testing.T) {
	if err := requireSuccess(helperResult{Outcome: "ok"}); err != nil {
		t.Fatalf("ok outcome rejected: %v", err)
	}
}

func TestCanonicalJSONDigestPreservesIntegerAndIgnoresObjectOrder(t *testing.T) {
	left, err := canonicalJSONDigest([]byte(`{"mtime_ns":1800000000000000001,"title":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	right, err := canonicalJSONDigest([]byte(`{"title":"x","mtime_ns":1800000000000000001}`))
	if err != nil {
		t.Fatal(err)
	}
	if left != right {
		t.Fatalf("canonical digests differ: %s != %s", left, right)
	}
	rounded, err := canonicalJSONDigest([]byte(`{"mtime_ns":1800000000000000000,"title":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if left == rounded {
		t.Fatal("distinct 64-bit nanosecond values produced the same digest")
	}
}

func TestVerifyLargeCountsRejectsPartialSnapshot(t *testing.T) {
	state := map[string]interface{}{
		"files":      make([]interface{}, 2),
		"properties": make([]interface{}, 13),
		"links":      make([]interface{}, 1),
		"fts_search": make([]interface{}, 2),
		"fts_norm":   make([]interface{}, 2),
		"fts_tri":    make([]interface{}, 1),
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyLargeCounts(raw, 2); err == nil {
		t.Fatal("partial FTS snapshot must be rejected")
	}
}

func TestVerifySearchResultRequiresExpectedPaths(t *testing.T) {
	test := largeCorpusSearchCase{ExpectedCount: 1, ExpectedPaths: []string{"corpus/00001.md"}}
	if err := verifySearchResult([]byte(`[{"path":"corpus/99999.md"}]`), test); err == nil {
		t.Fatal("wrong search path must be rejected")
	}
}

func TestValidateLargeCorpusManifestRejectsContractDrift(t *testing.T) {
	oracle := map[string]string{"commit": "b37ca57258174e2c7f9e321f1418a25c82ce00a6", "release": "post-v0.12.2-security-880"}
	valid := largeCorpusFixture{
		SchemaVersion:  1,
		Oracle:         oracle,
		DocumentCount:  10_000,
		SnapshotSHA256: "digest",
		PathTemplate:   "corpus/%05d.md",
		TitleTemplate:  "Corpus document %05d",
		SearchCases: []largeCorpusSearchCase{{
			Query:         "marker",
			ExpectedCount: 1,
			ExpectedPaths: []string{"corpus/00001.md"},
		}},
	}
	if err := validateLargeCorpusManifest(valid, oracle, oracle); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*largeCorpusFixture) (map[string]string, map[string]string)
	}{
		{"template", func(value *largeCorpusFixture) (map[string]string, map[string]string) {
			value.PathTemplate = "corpus/%d.md"
			return oracle, oracle
		}},
		{"provenance", func(_ *largeCorpusFixture) (map[string]string, map[string]string) {
			return oracle, map[string]string{"commit": "wrong", "release": "v0.12.2"}
		}},
		{"empty searches", func(value *largeCorpusFixture) (map[string]string, map[string]string) {
			value.SearchCases = nil
			return oracle, oracle
		}},
		{"missing expected paths", func(value *largeCorpusFixture) (map[string]string, map[string]string) {
			value.SearchCases[0].ExpectedPaths = nil
			return oracle, oracle
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			candidate.SearchCases = append([]largeCorpusSearchCase(nil), valid.SearchCases...)
			roundTrip, provenance := test.mutate(&candidate)
			if err := validateLargeCorpusManifest(candidate, roundTrip, provenance); err == nil {
				t.Fatal("contract drift was accepted")
			}
		})
	}
}
