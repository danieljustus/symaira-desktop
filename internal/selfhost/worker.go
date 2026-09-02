package selfhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/ingest"
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
		return nil, fmt.Errorf("ollama host must be a bare http(s) base URL")
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

	text, engine, model, err := w.processJob(ctx, job.ID, input)
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
	return w.processJob(ctx, "", input)
}

func (w *Worker) processJob(ctx context.Context, jobID, input string) (text, engine, model string, err error) {
	text, engine, model, err = w.processViaIngest(ctx, input)
	if err != nil {
		return "", "", "", err
	}
	verdict := ingest.InspectText(text)
	if verdict.OK {
		return text, engine, model, nil
	}

	logGuard(jobID, input, 1, verdict)
	retryText, retryEngine, retryModel, retryErr := w.processViaIngest(ctx, input)
	if retryErr != nil {
		return "", "", "", fmt.Errorf("OCR plausibility retry failed after %s: %w", verdict.Reason, retryErr)
	}
	retryVerdict := ingest.InspectText(retryText)
	if retryVerdict.OK {
		return retryText, retryEngine, retryModel, nil
	}

	logGuard(jobID, input, 2, retryVerdict)
	truncated := ingest.TruncateText(retryText, ingest.GuardFallbackWordLimit)
	markedEngine := fmt.Sprintf("%s; guard=truncated(%s)", retryEngine, retryVerdict.Reason)
	return truncated, markedEngine, retryModel, nil
}

func logGuard(jobID, input string, attempt int, verdict ingest.Verdict) {
	identifier := jobID
	if identifier == "" {
		identifier = filepath.Base(input)
	}
	log.Printf("OCR plausibility guard fired: job=%s attempt=%d reason=%s words=%d unique_ratio=%.3f", identifier, attempt, verdict.Reason, verdict.WordCount, verdict.UniqueRatio)
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
