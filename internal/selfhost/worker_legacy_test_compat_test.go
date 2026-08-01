package selfhost

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// These helpers keep the pre-composition unit tests focused on their original
// HTTP/rendering contracts without reintroducing either implementation into
// production code. The worker itself no longer calls them.
func (w *Worker) ollamaOCR(ctx context.Context, input string) (string, error) {
	images, cleanup, err := renderInput(ctx, input)
	if err != nil {
		return "", err
	}
	defer cleanup()
	var pages []string
	for index, image := range images {
		data, err := os.ReadFile(image)
		if err != nil {
			return "", err
		}
		payload := map[string]any{
			"model": w.cfg.OllamaModel,
			"messages": []map[string]any{{
				"role":   "user",
				"images": []string{base64.StdEncoding.EncodeToString(data)},
			}},
			"stream": false,
		}
		encoded, _ := json.Marshal(payload)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(w.cfg.OllamaURL, "/")+"/api/chat", bytes.NewReader(encoded))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := w.client.Do(req)
		if err != nil {
			return "", fmt.Errorf("ollama page %d: %w", index+1, err)
		}
		if resp.StatusCode != http.StatusOK {
			responseErr := responseError(resp)
			resp.Body.Close()
			return "", responseErr
		}
		var result struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, maxOCRBytes)).Decode(&result); err != nil {
			resp.Body.Close()
			return "", err
		}
		resp.Body.Close()
		pages = append(pages, strings.TrimSpace(result.Message.Content))
	}
	return strings.Join(pages, "\n\n--- Page ---\n\n"), nil
}

func renderInput(ctx context.Context, input string) ([]string, func(), error) {
	ext := strings.ToLower(filepath.Ext(input))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".webp", ".tif", ".tiff", ".bmp", ".heic":
		return []string{input}, func() {}, nil
	case ".pdf":
		if _, err := exec.LookPath("pdftoppm"); err != nil {
			return nil, func() {}, fmt.Errorf("pdftoppm is not installed")
		}
		dir, err := os.MkdirTemp(filepath.Dir(input), "pages-*")
		if err != nil {
			return nil, func() {}, err
		}
		cleanup := func() { _ = os.RemoveAll(dir) }
		prefix := filepath.Join(dir, "page")
		output, err := exec.CommandContext(ctx, "pdftoppm", "-png", "-r", "200", "-f", "1", "-l", "100", input, prefix).CombinedOutput()
		if err != nil {
			cleanup()
			return nil, func() {}, fmt.Errorf("pdftoppm: %s", strings.TrimSpace(string(output)))
		}
		images, err := filepath.Glob(prefix + "-*.png")
		if err != nil || len(images) == 0 {
			cleanup()
			return nil, func() {}, fmt.Errorf("PDF rendering produced no pages")
		}
		sort.Strings(images)
		return images, cleanup, nil
	default:
		return nil, func() {}, fmt.Errorf("OCR supports PDF and image files, got %q", ext)
	}
}
