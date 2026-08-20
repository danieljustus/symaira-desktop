package symseek

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const ReportSchemaVersion = 1

type Client struct {
	Binary  string
	Timeout time.Duration
	Home    string
}

type IndexResult struct {
	Path     string `json:"path"`
	OK       bool   `json:"ok"`
	Output   string `json:"output,omitempty"`
	Error    string `json:"error,omitempty"`
	Duration string `json:"duration"`
}

type QueryFixture struct {
	Query       string   `json:"query"`
	MinResults  int      `json:"min_results"`
	MustContain []string `json:"must_contain,omitempty"`
	Limit       int      `json:"limit,omitempty"`
}

type QueryCheck struct {
	Query       string   `json:"query"`
	OK          bool     `json:"ok"`
	MinResults  int      `json:"min_results"`
	ResultCount int      `json:"result_count"`
	MustContain []string `json:"must_contain,omitempty"`
	Missing     []string `json:"missing,omitempty"`
	Error       string   `json:"error,omitempty"`
}

type ValidationReport struct {
	SchemaVersion int          `json:"schema_version"`
	ToolVersion   string       `json:"tool_version"`
	GeneratedAt   time.Time    `json:"generated_at"`
	OK            bool         `json:"ok"`
	Total         int          `json:"total"`
	Passed        int          `json:"passed"`
	Failed        int          `json:"failed"`
	Checks        []QueryCheck `json:"checks"`
}

func (c Client) binary() (string, error) {
	if c.Binary != "" {
		return c.Binary, nil
	}
	path, err := exec.LookPath("symseek")
	if err != nil {
		return "", fmt.Errorf("symseek not found in PATH")
	}
	return path, nil
}

func (c Client) command(ctx context.Context, args ...string) ([]byte, error) {
	bin, err := c.binary()
	if err != nil {
		return nil, err
	}
	if c.Timeout <= 0 {
		c.Timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	if c.Home != "" {
		env := os.Environ()
		env = append(env, "HOME="+c.Home)
		cmd.Env = env
	}
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return out, fmt.Errorf("symseek timed out after %s", c.Timeout)
	}
	if err != nil {
		return out, fmt.Errorf("symseek %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func (c Client) Index(ctx context.Context, path string) IndexResult {
	start := time.Now()
	out, err := c.command(ctx, "index", path)
	res := IndexResult{Path: path, Duration: time.Since(start).String(), Output: strings.TrimSpace(string(out))}
	if err != nil {
		res.OK = false
		res.Error = err.Error()
		return res
	}
	res.OK = true
	return res
}

func (c Client) SearchJSON(ctx context.Context, query string, limit int) ([]byte, error) {
	if limit <= 0 {
		limit = 5
	}
	return c.command(ctx, "search", "--json", "--limit", fmt.Sprintf("%d", limit), query)
}

func LoadFixtures(path string) ([]QueryFixture, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var fixtures []QueryFixture
	if err := json.Unmarshal(data, &fixtures); err != nil {
		return nil, err
	}
	for i, f := range fixtures {
		if strings.TrimSpace(f.Query) == "" {
			return nil, fmt.Errorf("fixture %d has empty query", i)
		}
		if f.MinResults < 0 {
			return nil, fmt.Errorf("fixture %d has negative min_results", i)
		}
	}
	return fixtures, nil
}

func (c Client) Validate(ctx context.Context, fixtures []QueryFixture, defaultLimit int, toolVersion string) *ValidationReport {
	report := &ValidationReport{SchemaVersion: ReportSchemaVersion, ToolVersion: toolVersion, GeneratedAt: time.Now().UTC(), Total: len(fixtures), OK: true}
	for _, f := range fixtures {
		limit := f.Limit
		if limit <= 0 {
			limit = defaultLimit
		}
		if limit <= 0 {
			limit = 5
		}
		check := QueryCheck{Query: f.Query, MinResults: f.MinResults, MustContain: f.MustContain}
		out, err := c.SearchJSON(ctx, f.Query, limit)
		if err != nil {
			check.Error = err.Error()
			check.OK = false
		} else {
			check.ResultCount = countResults(out)
			raw := strings.ToLower(string(out))
			for _, needle := range f.MustContain {
				if !strings.Contains(raw, strings.ToLower(needle)) {
					check.Missing = append(check.Missing, needle)
				}
			}
			check.OK = check.ResultCount >= f.MinResults && len(check.Missing) == 0
		}
		if check.OK {
			report.Passed++
		} else {
			report.Failed++
			report.OK = false
		}
		report.Checks = append(report.Checks, check)
	}
	return report
}

func countResults(data []byte) int {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return 0
	}
	switch x := v.(type) {
	case []any:
		return len(x)
	case map[string]any:
		for _, key := range []string{"results", "documents", "matches", "items", "hits"} {
			if arr, ok := x[key].([]any); ok {
				return len(arr)
			}
		}
	}
	return 0
}
