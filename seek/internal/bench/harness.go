package bench

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/danieljustus/symaira-seek/internal/db"
	"github.com/danieljustus/symaira-seek/internal/engine"
	"github.com/google/uuid"
)

// SearchMode selects which retrieval backend to evaluate.
type SearchMode int

const (
	// SearchHybridMode runs BM25 + vector search with RRF fusion.
	SearchHybridMode SearchMode = iota
	// SearchVectorOnlyMode runs only vector similarity search.
	SearchVectorOnlyMode
)

// Config configures a single evaluation run for one query.
type Config struct {
	Query        string
	RelevantIDs  []string
	SearchOpts   engine.SearchOptions
	SearchLimit  int
	CorpusDir    string
	JudgmentPath string
	Mode         SearchMode
}

// Result holds the evaluation outcome for a single query.
type Result struct {
	Query       string
	RelevantIDs []string
	RankedIDs   []string
	HitRate1    float64
	HitRate3    float64
	HitRate5    float64
	HitRate10   float64
	MRR         float64
	NDCG10      float64
	Latency     time.Duration
	Error       string
}

// openTempDB creates a temporary database by overriding HOME to a temp dir.
func openTempDB() (*db.DB, string, error) {
	homeDir, err := os.MkdirTemp("", "symseek-bench-*")
	if err != nil {
		return nil, "", fmt.Errorf("creating temp home: %w", err)
	}

	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", homeDir)

	database, err := db.Open()
	if err != nil {
		os.Setenv("HOME", oldHome)
		os.RemoveAll(homeDir)
		return nil, "", fmt.Errorf("opening temp database: %w", err)
	}

	os.Setenv("HOME", oldHome)
	return database, homeDir, nil
}

// chunkNamespace mirrors engine.sync.go's deterministic UUID namespace so
// bench chunk IDs are stable across runs and follow the same identity rules
// as indexed chunks. See deriveChunkID below.
var chunkNamespace = uuid.NewSHA1(uuid.NameSpaceURL, []byte("https://github.com/danieljustus/symaira-seek/chunk"))

// deriveChunkID returns a deterministic UUIDv5 for a bench chunk, mirroring
// engine.deriveChunkID (documentPath \x00 contentHash \x00 charStart). The
// harness stores exactly one whole-file chunk per document at offset 0, so
// the inputs are the document path, its content hash, and 0.
func deriveChunkID(documentPath, contentHash string, charStart int) string {
	name := documentPath + "\x00" + contentHash + "\x00" + strconv.Itoa(charStart)
	return uuid.NewSHA1(chunkNamespace, []byte(name)).String()
}

// intPtr returns a pointer to the given int (char offsets are stored as
// nullable pointers in db.Chunk).
func intPtr(v int) *int {
	return &v
}

// Run executes a single evaluation: indexes the corpus, runs SearchHybrid for
// the configured query, and scores the result against ground truth.
func Run(embedder engine.Embedder, cfg Config) (*Result, error) {
	start := time.Now()

	// 1. Open a temporary database.
	dbClient, tmpDir, err := openTempDB()
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)
	defer dbClient.Close()

	// 2. Load and index the corpus.
	docs, err := LoadCorpus(cfg.CorpusDir)
	if err != nil {
		return nil, fmt.Errorf("loading corpus: %w", err)
	}

	for _, doc := range docs {
		if err := dbClient.SaveDocument(&doc); err != nil {
			return nil, fmt.Errorf("saving document %s: %w", doc.Path, err)
		}

		data, err := os.ReadFile(filepath.Join(cfg.CorpusDir, doc.Path+".txt"))
		if err != nil {
			return nil, fmt.Errorf("reading %s for embedding: %w", doc.Path, err)
		}

		chunk := &db.Chunk{
			UUID:         deriveChunkID(doc.Path, doc.Hash, 0),
			DocumentPath: doc.Path,
			ChunkIndex:   0,
			Content:      string(data),
			Embedding:    embedder.GenerateVector(string(data)),
			Hash:         doc.Hash,
			Dim:          embedder.Dim(),
			Model:        embedder.ModelName(),
			CharStart:    intPtr(0),
			CharEnd:      intPtr(len(data)),
		}
		if err := dbClient.SaveChunks([]*db.Chunk{chunk}); err != nil {
			return nil, fmt.Errorf("saving chunk for %s: %w", doc.Path, err)
		}
	}

	// Pre-warm the vector index.
	dbClient.SearchVector(make([]float32, embedder.Dim()), 1)

	// 3. Build ground-truth set.
	relevant := make(map[string]bool, len(cfg.RelevantIDs))
	for _, id := range cfg.RelevantIDs {
		relevant[id] = true
	}

	// 4. Run search.
	var results []*db.SearchResult
	switch cfg.Mode {
	case SearchVectorOnlyMode:
		queryVec := embedder.GenerateVector(cfg.Query)
		results, err = dbClient.SearchVector(queryVec, cfg.SearchLimit)
	default:
		results, err = engine.SearchHybridWithOptions(dbClient, dbClient, embedder, cfg.Query, cfg.SearchLimit, cfg.SearchOpts)
	}

	if err != nil {
		return &Result{
			Query:       cfg.Query,
			RelevantIDs: cfg.RelevantIDs,
			Error:       err.Error(),
			Latency:     time.Since(start),
		}, nil
	}

	// 5. Collect ranked document IDs.
	rankedIDs := make([]string, 0, len(results))
	for _, r := range results {
		rankedIDs = append(rankedIDs, r.Chunk.DocumentPath)
	}

	// 6. Compute metrics.
	return &Result{
		Query:       cfg.Query,
		RelevantIDs: cfg.RelevantIDs,
		RankedIDs:   rankedIDs,
		HitRate1:    HitRateAtK(rankedIDs, relevant, 1),
		HitRate3:    HitRateAtK(rankedIDs, relevant, 3),
		HitRate5:    HitRateAtK(rankedIDs, relevant, 5),
		HitRate10:   HitRateAtK(rankedIDs, relevant, 10),
		MRR:         MRR(rankedIDs, relevant),
		NDCG10:      NDCGAtK(rankedIDs, relevant, 10, nil),
		Latency:     time.Since(start),
	}, nil
}

// RunAll runs all queries from the judgment file against the corpus and
// returns one Result per query.
func RunAll(embedder engine.Embedder, corpusDir, judgmentPath string, searchLimit int, rerankCfg engine.RerankConfig, expandCfg engine.ExpandConfig) ([]*Result, error) {
	judgments, err := LoadJudgments(judgmentPath)
	if err != nil {
		return nil, fmt.Errorf("loading judgments: %w", err)
	}

	results := make([]*Result, 0, len(judgments))
	for _, j := range judgments {
		cfg := Config{
			Query:       j.Query,
			RelevantIDs: j.RelevantIDs,
			CorpusDir:   corpusDir,
			SearchLimit: searchLimit,
			SearchOpts: engine.SearchOptions{
				RerankCfg: rerankCfg,
				ExpandCfg: expandCfg,
			},
		}
		res, err := Run(embedder, cfg)
		if err != nil {
			return nil, fmt.Errorf("running query %q: %w", j.Query, err)
		}
		results = append(results, res)
	}
	return results, nil
}

// WriteCSV writes evaluation results as CSV.
func WriteCSV(w io.Writer, results []*Result) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()

	header := []string{
		"query", "hit_rate_1", "hit_rate_3", "hit_rate_5", "hit_rate_10",
		"mrr", "ndcg_10", "latency_ms", "error",
	}
	if err := cw.Write(header); err != nil {
		return err
	}

	for _, r := range results {
		latency := fmt.Sprintf("%.0f", float64(r.Latency)/float64(time.Millisecond))
		errStr := r.Error
		row := []string{
			r.Query,
			fmt.Sprintf("%.4f", r.HitRate1),
			fmt.Sprintf("%.4f", r.HitRate3),
			fmt.Sprintf("%.4f", r.HitRate5),
			fmt.Sprintf("%.4f", r.HitRate10),
			fmt.Sprintf("%.4f", r.MRR),
			fmt.Sprintf("%.4f", r.NDCG10),
			latency,
			errStr,
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	return nil
}

// Aggregate computes mean metrics across all results.
type Aggregate struct {
	Count     int
	HitRate1  float64
	HitRate3  float64
	HitRate5  float64
	HitRate10 float64
	MRR       float64
	NDCG10    float64
}

// ComputeAggregate returns the mean of each metric across results.
// Results with errors are excluded from the aggregate.
func ComputeAggregate(results []*Result) Aggregate {
	var agg Aggregate
	var count float64
	for _, r := range results {
		if r.Error != "" {
			continue
		}
		agg.HitRate1 += r.HitRate1
		agg.HitRate3 += r.HitRate3
		agg.HitRate5 += r.HitRate5
		agg.HitRate10 += r.HitRate10
		agg.MRR += r.MRR
		agg.NDCG10 += r.NDCG10
		count++
	}
	agg.Count = int(count)
	if count > 0 {
		agg.HitRate1 /= count
		agg.HitRate3 /= count
		agg.HitRate5 /= count
		agg.HitRate10 /= count
		agg.MRR /= count
		agg.NDCG10 /= count
	}
	return agg
}
