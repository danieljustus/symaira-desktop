package selfhost

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

type WorkerConfig struct {
	ServerURL   string
	Token       string
	WorkerID    string
	Engine      string
	OllamaURL   string
	OllamaModel string
	OCRLanguage string
	PollEvery   time.Duration
	Once        bool
}

type Worker struct {
	cfg    WorkerConfig
	client *http.Client
}

func NewWorker(cfg WorkerConfig) (*Worker, error) {
	base, err := url.Parse(strings.TrimRight(cfg.ServerURL, "/"))
	if err != nil || !isBareHTTPURL(base) {
		return nil, fmt.Errorf("server must be a bare http(s) base URL")
	}
	cfg.ServerURL = strings.TrimRight(base.String(), "/")
	if len(cfg.Token) < 32 {
		return nil, fmt.Errorf("server token must contain at least 32 characters")
	}
	if cfg.WorkerID == "" {
		hostname, _ := os.Hostname()
		cfg.WorkerID = hostname + "-" + runtime.GOOS + "-" + runtime.GOARCH
	}
	if cfg.Engine == "" {
		cfg.Engine = "auto"
	}
	if cfg.OCRLanguage == "" {
		cfg.OCRLanguage = "deu+eng"
	}
	if cfg.PollEvery <= 0 {
		cfg.PollEvery = 5 * time.Second
	}
	if cfg.OllamaURL == "" {
		cfg.OllamaURL = "http://127.0.0.1:11434"
	}
	ollama, err := url.Parse(strings.TrimRight(cfg.OllamaURL, "/"))
	if err != nil || !isBareHTTPURL(ollama) {
		return nil, fmt.Errorf("Ollama host must be a bare http(s) base URL")
	}
	cfg.OllamaURL = strings.TrimRight(ollama.String(), "/")
	return &Worker{cfg: cfg, client: &http.Client{Timeout: 15 * time.Minute}}, nil
}

func isBareHTTPURL(value *url.URL) bool {
	return value != nil && (value.Scheme == "http" || value.Scheme == "https") && value.Host != "" &&
		(value.Path == "" || value.Path == "/") && value.RawQuery == "" && value.Fragment == "" && value.User == nil
}

func (w *Worker) Run(ctx context.Context) error {
	for {
		processed, err := w.RunOnce(ctx)
		if err != nil {
			if w.cfg.Once {
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(w.cfg.PollEvery):
				continue
			}
		}
		if w.cfg.Once {
			return nil
		}
		if processed {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(w.cfg.PollEvery):
		}
	}
}

func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	job, err := w.lease(ctx)
	if errors.Is(err, ErrNoPendingJob) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	input, cleanup, err := w.download(ctx, job)
	if err != nil {
		_ = w.fail(ctx, job.ID, err.Error(), true)
		return true, err
	}
	defer cleanup()

	text, engine, model, err := w.process(ctx, input)
	if err != nil {
		_ = w.fail(ctx, job.ID, err.Error(), false)
		return true, err
	}
	if err := w.complete(ctx, job.ID, text, engine, model); err != nil {
		return true, err
	}
	return true, nil
}

func (w *Worker) lease(ctx context.Context) (*Job, error) {
	body, _ := json.Marshal(map[string]any{"worker_id": w.cfg.WorkerID, "capabilities": []string{"ocr"}})
	resp, err := w.request(ctx, http.MethodPost, "/api/v1/worker/lease", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil, ErrNoPendingJob
	}
	if resp.StatusCode != http.StatusOK {
		return nil, responseError(resp)
	}
	var job Job
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		return nil, err
	}
	return &job, nil
}

func (w *Worker) download(ctx context.Context, job *Job) (string, func(), error) {
	resp, err := w.request(ctx, http.MethodGet, "/api/v1/worker/input?id="+url.QueryEscape(job.ID), "", nil)
	if err != nil {
		return "", func() {}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", func() {}, responseError(resp)
	}
	dir, err := os.MkdirTemp("", "symdesk-worker-*")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	ext := filepath.Ext(job.OriginalName)
	if ext == "" {
		extensions, _ := mime.ExtensionsByType(job.ContentType)
		if len(extensions) > 0 {
			ext = extensions[0]
		}
	}
	path := filepath.Join(dir, "input"+ext)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	_, copyErr := io.Copy(file, io.LimitReader(resp.Body, maxUploadBytes+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		cleanup()
		if copyErr != nil {
			return "", func() {}, copyErr
		}
		return "", func() {}, closeErr
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() > maxUploadBytes {
		cleanup()
		return "", func() {}, fmt.Errorf("worker input exceeds 100 MiB")
	}
	return path, cleanup, nil
}

func (w *Worker) process(ctx context.Context, input string) (text, engine, model string, err error) {
	engine = strings.ToLower(w.cfg.Engine)
	if engine == "auto" {
		if w.cfg.OllamaModel != "" {
			engine = "ollama"
		} else {
			engine = "tesseract"
		}
	}
	switch engine {
	case "ollama":
		if w.cfg.OllamaModel == "" {
			return "", "", "", fmt.Errorf("--ollama-model is required for the Ollama engine")
		}
		text, err = w.ollamaOCR(ctx, input)
		return text, engine, w.cfg.OllamaModel, err
	case "tesseract":
		text, err = w.tesseractOCR(ctx, input)
		return text, engine, "", err
	default:
		return "", "", "", fmt.Errorf("unsupported OCR engine %q", engine)
	}
}

func (w *Worker) tesseractOCR(ctx context.Context, input string) (string, error) {
	images, cleanup, err := renderInput(ctx, input)
	if err != nil {
		return "", err
	}
	defer cleanup()
	if _, err := exec.LookPath("tesseract"); err != nil {
		return "", fmt.Errorf("tesseract is not installed")
	}
	var pages []string
	for _, image := range images {
		command := exec.CommandContext(ctx, "tesseract", image, "stdout", "-l", w.cfg.OCRLanguage)
		output, err := command.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("tesseract: %s", strings.TrimSpace(string(output)))
		}
		pages = append(pages, strings.TrimSpace(string(output)))
	}
	return strings.Join(pages, "\n\n--- Page ---\n\n"), nil
}

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
				"role":    "user",
				"content": "Transcribe this document page exactly. Preserve paragraphs and tables as readable Markdown. Return only the transcription; do not summarize or invent content.",
				"images":  []string{base64.StdEncoding.EncodeToString(data)},
			}},
			"stream": false,
		}
		encoded, _ := json.Marshal(payload)
		endpoint := strings.TrimRight(w.cfg.OllamaURL, "/") + "/api/chat"
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := w.client.Do(req)
		if err != nil {
			return "", fmt.Errorf("ollama page %d: %w", index+1, err)
		}
		if resp.StatusCode != http.StatusOK {
			err := responseError(resp)
			resp.Body.Close()
			return "", fmt.Errorf("ollama page %d: %w", index+1, err)
		}
		var result struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, maxOCRBytes)).Decode(&result)
		resp.Body.Close()
		if err != nil {
			return "", err
		}
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

func (w *Worker) complete(ctx context.Context, jobID, text, engine, model string) error {
	body, _ := json.Marshal(map[string]any{
		"job_id": jobID, "worker_id": w.cfg.WorkerID, "text": text, "engine": engine, "model": model,
	})
	resp, err := w.request(ctx, http.MethodPost, "/api/v1/worker/complete", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return responseError(resp)
	}
	return nil
}

func (w *Worker) fail(ctx context.Context, jobID, message string, retry bool) error {
	body, _ := json.Marshal(map[string]any{
		"job_id": jobID, "worker_id": w.cfg.WorkerID, "error": message, "retry": retry,
	})
	resp, err := w.request(ctx, http.MethodPost, "/api/v1/worker/fail", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return responseError(resp)
	}
	return nil
}

func (w *Worker) request(ctx context.Context, method, path, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(w.cfg.ServerURL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+w.cfg.Token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return w.client.Do(req)
}

func responseError(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var payload struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(data, &payload) == nil && payload.Error != "" {
		return errors.New(payload.Error)
	}
	return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
}
