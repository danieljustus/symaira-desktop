package selfhost

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/danieljustus/symaira-desktop/internal/compose"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// processViaSymingest composes the sibling OCR tool, resolved via
// compose.Resolve ($SYMAIRA_BIN, then the managed runtime directory
// ~/.symaira/bin, then PATH). The child is given a temporary vault so it can
// use its normal pipeline without writing a second persistent note into the
// server vault.
func (w *Worker) processViaSymingest(ctx context.Context, input string) (text, engine, model string, err error) {
	mode, err := w.symingestMode()
	if err != nil {
		return "", "", "", err
	}
	binary, err := compose.Resolve("symingest")
	if err != nil {
		return "", "", "", fmt.Errorf("symingest is required for OCR but was not found via $SYMAIRA_BIN, the managed runtime directory, or PATH: %w", err)
	}

	scratch, err := os.MkdirTemp("", "symdesk-symingest-*")
	if err != nil {
		return "", "", "", fmt.Errorf("create symingest scratch directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(scratch) }()

	scratchHome := filepath.Join(scratch, "home")
	vaultRoot := filepath.Join(scratch, "vault")
	archiveRoot := filepath.Join(scratch, "archive")
	if err := os.MkdirAll(vaultRoot, 0700); err != nil {
		return "", "", "", fmt.Errorf("create symingest scratch vault: %w", err)
	}
	if err := os.MkdirAll(scratchHome, 0700); err != nil {
		return "", "", "", fmt.Errorf("create symingest scratch home: %w", err)
	}

	cmd := exec.CommandContext(ctx, binary, //nolint:gosec // symingest is the intentional PATH integration boundary
		"ingest",
		"--vault", vaultRoot,
		"--archive", archiveRoot,
		"--db", filepath.Join(scratch, "symingest.db"),
		"--ocr-lang", w.cfg.OCRLanguage,
		input,
	)
	cmd.Env = symingestEnvironment(os.Environ(), scratchHome, mode, w.cfg.OllamaURL, w.cfg.OllamaModel)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = strings.TrimSpace(stdout.String())
		}
		if message == "" {
			message = err.Error()
		}
		return "", "", "", fmt.Errorf("symingest OCR failed: %s", message)
	}

	notePath, err := findSymingestNote(vaultRoot)
	if err != nil {
		return "", "", "", err
	}
	doc, err := vault.ParseFile(notePath)
	if err != nil {
		return "", "", "", fmt.Errorf("parse symingest output: %w", err)
	}
	text = stripSymingestArchive(doc.Body)
	if value, ok := doc.Frontmatter["ocr_engine"].(string); ok {
		engine = strings.TrimSpace(value)
	}
	if engine == "" {
		engine = outputValue(stdout.String(), "engine:")
	}
	if engine == "" {
		engine = "symingest"
	}
	if mode == "ollama" {
		model = w.cfg.OllamaModel
	}
	return text, engine, model, nil
}

func (w *Worker) symingestMode() (string, error) {
	mode := strings.ToLower(strings.TrimSpace(w.cfg.Engine))
	if mode == "" || mode == "auto" {
		if w.cfg.OllamaModel != "" {
			return "ollama", nil
		}
		return "tesseract", nil
	}
	if mode != "tesseract" && mode != "ollama" {
		return "", fmt.Errorf("unsupported OCR engine %q; symingest supports tesseract or ollama", w.cfg.Engine)
	}
	if mode == "ollama" && w.cfg.OllamaModel == "" {
		return "", fmt.Errorf("--ollama-model is required for the Ollama engine")
	}
	return mode, nil
}

func symingestEnvironment(base []string, home, mode, ollamaURL, ollamaModel string) []string {
	values := map[string]string{
		"HOME":                         home,
		"XDG_CONFIG_HOME":              filepath.Join(home, ".config"),
		"SYMINGEST_OLLAMA_BASE_URL":    "",
		"SYMINGEST_OLLAMA_MODEL":       "",
		"SYMINGEST_VAULT":              "",
		"SYMINGEST_ARCHIVE_PATH":       "",
		"SYMINGEST_DB_PATH":            "",
		"SYMINGEST_INBOX":              "",
		"SYMINGEST_SYMSEEK_ENABLED":    "false",
		"SYMINGEST_SYMSEEK_BINARY":     "",
		"SYMINGEST_PAPERLESS_BASE_URL": "",
	}
	if mode == "ollama" {
		values["SYMINGEST_OLLAMA_BASE_URL"] = ollamaURL
		values["SYMINGEST_OLLAMA_MODEL"] = ollamaModel
	}
	result := make([]string, 0, len(base)+len(values))
	for _, item := range base {
		key, _, ok := strings.Cut(item, "=")
		if ok {
			if _, replace := values[key]; replace {
				continue
			}
		}
		result = append(result, item)
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

func findSymingestNote(root string) (string, error) {
	var notes []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if strings.HasPrefix(entry.Name(), ".") && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(entry.Name()) == ".md" {
			notes = append(notes, path)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("inspect symingest output: %w", err)
	}
	if len(notes) == 0 {
		return "", fmt.Errorf("symingest completed without producing a Markdown note")
	}
	sort.Strings(notes)
	return notes[0], nil
}

func stripSymingestArchive(body string) string {
	body = strings.TrimSpace(body)
	if index := strings.LastIndex(body, "\n---\n"); index >= 0 {
		trailer := strings.TrimSpace(body[index+len("\n---\n"):])
		if strings.HasPrefix(trailer, "[Archived Original]") {
			body = strings.TrimSpace(body[:index])
		}
	}
	return body
}

func outputValue(output, prefix string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}
