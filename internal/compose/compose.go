package compose

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type ToolInfo struct {
	Available bool
	Version   string
}

type SearchResult struct {
	Path    string  `json:"path"`
	Score   float64 `json:"score"`
	Snippet string  `json:"snippet"`
}

type MemoryEntity struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Aliases     []string `json:"aliases"`
	Description string   `json:"description"`
}

type MemoryRelation struct {
	FromEntityID string `json:"from_entity_id"`
	ToEntityID   string `json:"to_entity_id"`
	RelationType string `json:"relation_type"`
}

type MemoryNeighbors struct {
	Nodes []MemoryEntity   `json:"nodes"`
	Edges []MemoryRelation `json:"edges"`
}

var (
	cacheMu  sync.RWMutex
	cacheMap = make(map[string]*ToolInfo)

	// SymseekBin is the path or name of the symseek binary.
	// Tests override this to avoid calling the real binary on PATH.
	SymseekBin = "symseek"

	// SymmemoryBin is the path or name of the symmemory binary.
	// Tests override this to avoid calling the real binary on PATH.
	SymmemoryBin = "symmemory"

	// SymprintBin is the path or name of the symprint binary.
	// Tests override this to avoid calling the real binary on PATH.
	SymprintBin = "symprint"
)

// ResetCache clears the internal sibling tool probe cache.
func ResetCache() {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	cacheMap = make(map[string]*ToolInfo)
}

// HasTool checks if a sibling tool is available on PATH and returns its version.
func HasTool(name string) (bool, string) {
	cacheMu.RLock()
	if info, ok := cacheMap[name]; ok {
		res := *info
		cacheMu.RUnlock()
		return res.Available, res.Version
	}
	cacheMu.RUnlock()

	cacheMu.Lock()
	defer cacheMu.Unlock()
	if info, ok := cacheMap[name]; ok {
		return info.Available, info.Version
	}

	available, version := probeTool(name)
	cacheMap[name] = &ToolInfo{Available: available, Version: version}
	return available, version
}

// HasSymseek is a shorthand helper for symseek.
func HasSymseek() (bool, string) {
	return HasTool(SymseekBin)
}

// HasSymmemory is a shorthand helper for symmemory.
func HasSymmemory() (bool, string) {
	return HasTool(SymmemoryBin)
}

// HasSymprint is a shorthand helper for symprint.
func HasSymprint() (bool, string) {
	return HasTool(SymprintBin)
}

// RenderPDF writes a Markdown document to a PDF using symprint.
// It returns the absolute output path and any structured error from symprint.
func RenderPDF(markdown []byte, outputPath, profile string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	bin, err := exec.LookPath(SymprintBin)
	if err != nil {
		return "", fmt.Errorf("symprint not found on PATH")
	}

	dir := filepath.Dir(outputPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create output directory: %w", err)
	}

	args := []string{"render", "-", "-o", outputPath}
	if profile != "" {
		args = append(args, "-p", profile)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin = bytes.NewReader(markdown)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("symprint render failed: %w (stderr: %s)", err, stderr.String())
	}
	return outputPath, nil
}

// ListSymprintProfiles returns the profile names symprint knows.
func ListSymprintProfiles() ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, SymprintBin, "profiles", "--json") //nolint:gosec
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("symprint profiles failed: %w (stderr: %s)", err, stderr.String())
	}

	var profiles []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(out.Bytes(), &profiles); err != nil {
		return nil, fmt.Errorf("failed to unmarshal symprint profiles: %w", err)
	}
	var names []string
	for _, p := range profiles {
		if p.Name != "" {
			names = append(names, p.Name)
		}
	}
	return names, nil
}

func probeTool(name string) (bool, string) {
	path, err := exec.LookPath(name)
	if err != nil {
		return false, "not_found"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, path, "version", "--json")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return true, "unknown"
	}

	var ver struct {
		SchemaVersion int    `json:"schema_version"`
		Version       string `json:"version"`
	}
	if err := json.Unmarshal(out.Bytes(), &ver); err != nil {
		return true, "unknown"
	}
	return true, ver.Version
}

// IndexDocument indexes a single document into symseek via stdin.
func IndexDocument(path, body string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, SymseekBin, "index", "--stdin", "--source", path) //nolint:gosec
	cmd.Stdin = strings.NewReader(body)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("symseek index failed: %w (stderr: %s)", err, stderr.String())
	}
	return nil
}

// DeleteDocument removes a document from symseek index.
func DeleteDocument(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, SymseekBin, "delete", path) //nolint:gosec
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("symseek delete failed: %w (stderr: %s)", err, stderr.String())
	}
	return nil
}

// Search executes a hybrid search on symseek.
func Search(query string) ([]SearchResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, SymseekBin, "search", query, "--json") //nolint:gosec
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("symseek search failed: %w (stderr: %s)", err, stderr.String())
	}

	var results []SearchResult
	if err := json.Unmarshal(out.Bytes(), &results); err != nil {
		return nil, fmt.Errorf("failed to unmarshal symseek search output: %w", err)
	}
	return results, nil
}

// ListEntities lists all entities stored in symmemory.
func ListEntities() ([]MemoryEntity, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, SymmemoryBin, "entity", "list", "--output", "json") //nolint:gosec
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("symmemory entity list failed: %w (stderr: %s)", err, stderr.String())
	}

	var entities []MemoryEntity
	if err := json.Unmarshal(out.Bytes(), &entities); err != nil {
		return nil, fmt.Errorf("failed to unmarshal symmemory entities: %w", err)
	}
	return entities, nil
}

// GetNeighbors resolves neighbors of a given entity name from symmemory.
func GetNeighbors(name string) (*MemoryNeighbors, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, SymmemoryBin, "entity", "neighbors", name, "--output", "json") //nolint:gosec
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("symmemory neighbors failed: %w (stderr: %s)", err, stderr.String())
	}

	var neighbors MemoryNeighbors
	if err := json.Unmarshal(out.Bytes(), &neighbors); err != nil {
		return nil, fmt.Errorf("failed to unmarshal symmemory neighbors: %w", err)
	}
	return &neighbors, nil
}
