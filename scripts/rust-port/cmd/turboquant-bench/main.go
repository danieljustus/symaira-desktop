// Command turboquant-bench compares pinned CoreKit TurboQuant latency and
// throughput with the local Rust port (#1137). A Rust p95 or throughput
// regression above --max-regression exits nonzero.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"time"

	"github.com/danieljustus/symaira-corekit/vectorkit/turboquant"
)

type measurement struct {
	Checksum uint64   `json:"checksum"`
	BestSum  float64  `json:"best_sum"`
	EncodeNS []uint64 `json:"encode_ns"`
	ScanNS   []uint64 `json:"scan_ns"`
}

type params struct {
	dim, bitWidth, vectors, queries, rounds int
}

func main() {
	rustBin := flag.String("rust-bin", "", "release build of symdesk-index turboquant-bench")
	report := flag.String("report", "", "markdown report path (stdout when empty)")
	maxRegression := flag.Float64("max-regression", 0.10, "allowed Rust regression vs Go")
	var p params
	flag.IntVar(&p.dim, "dim", 768, "vector dimension")
	flag.IntVar(&p.bitWidth, "bit-width", 4, "TurboQuant bit width")
	flag.IntVar(&p.vectors, "vectors", 2048, "corpus vectors")
	flag.IntVar(&p.queries, "queries", 32, "scan queries per round")
	flag.IntVar(&p.rounds, "rounds", 5, "measured rounds after one warm-up")
	trials := flag.Int("trials", 3, "alternating Go/Rust trials; the best trial per implementation is reported")
	flag.Parse()
	if *rustBin == "" || *trials < 1 || flag.NArg() != 0 {
		fatalf("usage: turboquant-bench --rust-bin PATH [--report FILE]")
	}

	// Alternating trials and best-of-trials damp scheduler noise on shared
	// hosts equally for both implementations.
	var goRuns, rustRuns []measurement
	for range *trials {
		goResult, err := runGo(p)
		if err != nil {
			fatalf("go workload: %v", err)
		}
		rustResult, err := runRust(*rustBin, p)
		if err != nil {
			fatalf("rust workload: %v", err)
		}
		if goResult.Checksum != rustResult.Checksum {
			fatalf("encoded sidecar checksum differs: go=%d rust=%d", goResult.Checksum, rustResult.Checksum)
		}
		if math.Abs(goResult.BestSum-rustResult.BestSum) > 1e-6*math.Abs(goResult.BestSum) {
			fatalf("scan scores differ: go=%v rust=%v", goResult.BestSum, rustResult.BestSum)
		}
		goRuns = append(goRuns, goResult)
		rustRuns = append(rustRuns, rustResult)
	}

	var out bytes.Buffer
	fmt.Fprintf(&out, "| Operation | Impl | p50 | p95 | Throughput |\n|---|---|---:|---:|---:|\n")
	failed := false
	for _, op := range []struct {
		name  string
		unit  string
		perOp int
		pick  func(measurement) []uint64
	}{
		{"encode sidecar (1 vector)", "vectors/s", 1, func(m measurement) []uint64 { return m.EncodeNS }},
		{fmt.Sprintf("quantized scan (%d sidecars)", p.vectors), "scored/s", p.vectors, func(m measurement) []uint64 { return m.ScanNS }},
	} {
		goP50, goP95, goTput := best(goRuns, op.pick, op.perOp)
		rustP50, rustP95, rustTput := best(rustRuns, op.pick, op.perOp)
		fmt.Fprintf(&out, "| %s | Go | %s | %s | %.0f %s |\n", op.name, goP50, goP95, goTput, op.unit)
		fmt.Fprintf(&out, "| %s | Rust | %s | %s | %.0f %s |\n", op.name, rustP50, rustP95, rustTput, op.unit)
		if float64(rustP95) > float64(goP95)*(1+*maxRegression) || rustTput < goTput/(1+*maxRegression) {
			failed = true
			fmt.Fprintf(&out, "\nREGRESSION: %s exceeds the %.0f%% budget.\n\n", op.name, *maxRegression*100)
		}
	}
	verdict := "PASS"
	if failed {
		verdict = "FAIL"
	}
	header := fmt.Sprintf("# TurboQuant Go vs Rust benchmark (#1137)\n\nRegenerate with `make retrieval-turboquant-benchmark` on an idle host.\n\nHost %s/%s, %d CPUs, %s; dim=%d bit_width=%d seed=42 vectors=%d queries=%d rounds=%d (+1 warm-up), best of %d alternating trials.\n"+
		"Encoded sidecar checksum %d identical; scan score sums agree within 1e-6. Budget: Rust p95 and throughput within %.0f%% of Go. Verdict: %s.\n\n",
		runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), runtime.Version(), p.dim, p.bitWidth, p.vectors, p.queries, p.rounds, *trials,
		goRuns[0].Checksum, *maxRegression*100, verdict)
	text := header + out.String()
	if *report == "" {
		fmt.Print(text)
	} else if err := os.WriteFile(*report, []byte(text), 0o644); err != nil { //nolint:gosec // report artifact
		fatalf("write report: %v", err)
	}
	fmt.Fprint(os.Stderr, text)
	if failed {
		os.Exit(1)
	}
}

// runGo mirrors crates/symdesk-index/src/bin/turboquant-bench.rs.
func runGo(p params) (measurement, error) {
	codec, err := turboquant.NewCodec(p.dim, turboquant.BitWidth(p.bitWidth), 42, 0)
	if err != nil {
		return measurement{}, err
	}
	corpus := make([][]float32, p.vectors)
	for i := range corpus {
		corpus[i] = floats32(p.dim, uint64(i)+1)
	}
	probes := make([][]float32, p.queries)
	for i := range probes {
		probes[i] = floats32(p.dim, 1_000_000+uint64(i))
	}
	var m measurement
	blobs := make([][]byte, 0, p.vectors)
	for round := 0; round <= p.rounds; round++ {
		blobs = blobs[:0]
		for _, vector := range corpus {
			start := time.Now()
			blob, _, err := codec.EncodeSidecar(vector, 0)
			elapsed := time.Since(start)
			if err != nil {
				return m, err
			}
			if round > 0 {
				m.EncodeNS = append(m.EncodeNS, uint64(elapsed.Nanoseconds())) //nolint:gosec // positive duration
			}
			blobs = append(blobs, blob)
		}
		for _, query := range probes {
			start := time.Now()
			best := float32(-math.MaxFloat32)
			for _, blob := range blobs {
				code, err := turboquant.UnpackSidecarBlob(blob)
				if err != nil {
					return m, err
				}
				best = max(best, codec.Score(query, code))
			}
			elapsed := time.Since(start)
			if round > 0 {
				m.ScanNS = append(m.ScanNS, uint64(elapsed.Nanoseconds())) //nolint:gosec // positive duration
			}
			m.BestSum += float64(best)
		}
	}
	for _, blob := range blobs {
		for _, b := range blob {
			m.Checksum = m.Checksum*31 + uint64(b)
		}
	}
	return m, nil
}

func runRust(bin string, p params) (measurement, error) {
	//nolint:gosec // operator-supplied benchmark binary
	cmd := exec.Command(bin,
		"--dim", strconv.Itoa(p.dim), "--bit-width", strconv.Itoa(p.bitWidth),
		"--vectors", strconv.Itoa(p.vectors), "--queries", strconv.Itoa(p.queries), "--rounds", strconv.Itoa(p.rounds))
	cmd.Stderr = os.Stderr
	output, err := cmd.Output()
	if err != nil {
		return measurement{}, err
	}
	var m measurement
	if err := json.Unmarshal(output, &m); err != nil {
		return m, err
	}
	if len(m.EncodeNS) != p.vectors*p.rounds || len(m.ScanNS) != p.queries*p.rounds {
		return m, fmt.Errorf("unexpected sample counts %d/%d", len(m.EncodeNS), len(m.ScanNS))
	}
	return m, nil
}

// best returns the lowest p50/p95 and highest throughput across trials.
func best(runs []measurement, pick func(measurement) []uint64, perOp int) (time.Duration, time.Duration, float64) {
	p50, p95, tput := time.Duration(math.MaxInt64), time.Duration(math.MaxInt64), 0.0
	for _, run := range runs {
		a, b, c := stats(pick(run), perOp)
		p50, p95, tput = min(p50, a), min(p95, b), max(tput, c)
	}
	return p50, p95, tput
}

func stats(samples []uint64, perOp int) (time.Duration, time.Duration, float64) {
	sorted := slices.Clone(samples)
	slices.Sort(sorted)
	var total uint64
	for _, s := range sorted {
		total += s
	}
	at := func(q float64) time.Duration {
		return time.Duration(sorted[int(math.Ceil(q*float64(len(sorted))))-1]) //nolint:gosec // bounded sample
	}
	return at(0.50), at(0.95), float64(len(sorted)*perOp) / (float64(total) / 1e9)
}

func splitmix64(state *uint64) uint64 {
	*state += 0x9e3779b97f4a7c15
	z := *state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

func floats32(n int, seed uint64) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = float32(splitmix64(&seed)>>40)/float32(1<<24)*2 - 1
	}
	return out
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
