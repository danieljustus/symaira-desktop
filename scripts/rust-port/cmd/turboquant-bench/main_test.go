package main

import (
	"testing"
	"time"
)

func TestRunGoIsDeterministicAndCounted(t *testing.T) {
	p := params{dim: 16, bitWidth: 4, vectors: 8, queries: 3, rounds: 2}
	first, err := runGo(p)
	if err != nil {
		t.Fatal(err)
	}
	second, err := runGo(p)
	if err != nil {
		t.Fatal(err)
	}
	if first.Checksum != second.Checksum || first.BestSum != second.BestSum {
		t.Fatalf("workload not deterministic: %+v vs %+v", first.Checksum, second.Checksum)
	}
	if len(first.EncodeNS) != p.vectors*p.rounds || len(first.ScanNS) != p.queries*p.rounds {
		t.Fatalf("sample counts %d/%d", len(first.EncodeNS), len(first.ScanNS))
	}
	if _, err := runGo(params{dim: 16, bitWidth: 5}); err == nil {
		t.Fatal("invalid bit width accepted")
	}
}

func TestStatsAndBest(t *testing.T) {
	samples := make([]uint64, 100)
	for i := range samples {
		samples[i] = uint64(100 - i)
	}
	p50, p95, tput := stats(samples, 2)
	if p50 != 50 || p95 != 95 || tput != 200/(5050/1e9) {
		t.Fatalf("stats = %v %v %v", p50, p95, tput)
	}
	slow := measurement{EncodeNS: []uint64{1000, 2000}}
	fast := measurement{EncodeNS: []uint64{10, 20}}
	pick := func(m measurement) []uint64 { return m.EncodeNS }
	b50, b95, _ := best([]measurement{slow, fast}, pick, 1)
	if b50 != 10*time.Nanosecond || b95 != 20*time.Nanosecond {
		t.Fatalf("best = %v %v", b50, b95)
	}
}

func TestRunRustRejectsMissingBinary(t *testing.T) {
	if _, err := runRust(t.TempDir()+"/missing", params{}); err == nil {
		t.Fatal("missing binary accepted")
	}
}
