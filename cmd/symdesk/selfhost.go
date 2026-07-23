package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/selfhost"
	"github.com/danieljustus/symaira-desktop/internal/vault"
	"github.com/spf13/cobra"
)

func newServeCmd() *cobra.Command {
	var listen, token, workerToken, tlsCert, tlsKey string
	var localWorker bool
	var workerEngine, ollamaURL, ollamaModel, language string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the self-hosted SymDesk API and processing queue",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ha := readHomeAssistantOptions()
			if token == "" {
				token = ha.ServerToken
			}
			if workerToken == "" {
				workerToken = ha.WorkerToken
			}
			if ha.Listen != "" && !cmd.Flags().Changed("listen") {
				listen = ha.Listen
			}
			if ha.Vault != "" && vaultFlag == "" && cfg.Vault == "" {
				cfg.Vault = ha.Vault
			}
			if ha.LocalProcessing && !cmd.Flags().Changed("local-worker") {
				localWorker = true
			}
			if ha.WorkerEngine != "" && !cmd.Flags().Changed("worker-engine") {
				workerEngine = ha.WorkerEngine
			}
			if ha.OllamaURL != "" && !cmd.Flags().Changed("ollama-url") {
				ollamaURL = ha.OllamaURL
			}
			if ha.OllamaModel != "" && !cmd.Flags().Changed("ollama-model") {
				ollamaModel = ha.OllamaModel
			}
			if localWorker && tlsCert != "" {
				return fmt.Errorf("the built-in local worker cannot connect to a custom TLS listener; run it as a separate worker")
			}
			root, err := vault.ResolveVaultRoot("", cfg)
			if err != nil {
				return err
			}
			server, err := selfhost.NewServer(selfhost.ServerConfig{
				ListenAddress: listen, VaultRoot: root, Token: token, WorkerToken: workerToken, Version: version,
				TLSCert: tlsCert, TLSKey: tlsKey,
			})
			if err != nil {
				return err
			}
			defer server.Close()

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			errCh := make(chan error, 1)
			go func() { errCh <- server.ListenAndServe() }()
			if localWorker {
				localWorkerToken := workerToken
				if localWorkerToken == "" {
					localWorkerToken = token
				}
				worker, workerErr := selfhost.NewWorker(selfhost.WorkerConfig{
					ServerURL: "http://127.0.0.1:" + portFromListen(listen), Token: localWorkerToken,
					WorkerID: "server-local-worker", Engine: workerEngine, OllamaURL: ollamaURL,
					OllamaModel: ollamaModel, OCRLanguage: language, PollEvery: 5 * time.Second,
				})
				if workerErr != nil {
					return workerErr
				}
				go func() {
					if err := worker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
						errCh <- err
					}
				}()
			}
			select {
			case <-ctx.Done():
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				return server.Shutdown(shutdownCtx)
			case err := <-errCh:
				if errors.Is(err, http.ErrServerClosed) {
					return nil
				}
				return err
			}
		},
	}
	cmd.Flags().StringVar(&listen, "listen", envOr("SYMDESK_SERVER_LISTEN", "127.0.0.1:8787"), "listen address")
	cmd.Flags().StringVar(&token, "token", os.Getenv("SYMDESK_SERVER_TOKEN"), "admin/client bearer token (or SYMDESK_SERVER_TOKEN; minimum 32 characters)")
	cmd.Flags().StringVar(&workerToken, "worker-token", os.Getenv("SYMDESK_WORKER_TOKEN"), "separate bearer token scoped to worker routes only (or SYMDESK_WORKER_TOKEN; minimum 32 characters). When unset, workers must use --token instead")
	cmd.Flags().StringVar(&tlsCert, "tls-cert", os.Getenv("SYMDESK_TLS_CERT"), "TLS certificate path")
	cmd.Flags().StringVar(&tlsKey, "tls-key", os.Getenv("SYMDESK_TLS_KEY"), "TLS private-key path")
	cmd.Flags().BoolVar(&localWorker, "local-worker", false, "process OCR jobs inside the server container")
	cmd.Flags().StringVar(&workerEngine, "worker-engine", envOr("SYMDESK_WORKER_ENGINE", "tesseract"), "local worker OCR engine")
	cmd.Flags().StringVar(&ollamaURL, "ollama-url", envOr("OLLAMA_HOST", "http://127.0.0.1:11434"), "Ollama base URL for the local worker")
	cmd.Flags().StringVar(&ollamaModel, "ollama-model", os.Getenv("SYMDESK_OLLAMA_MODEL"), "Ollama vision model for the local worker")
	cmd.Flags().StringVar(&language, "ocr-language", envOr("SYMDESK_OCR_LANGUAGE", "deu+eng"), "Tesseract languages for the local worker")
	return cmd
}

func newWorkerCmd() *cobra.Command {
	var serverURL, token, workerID, engine, ollamaURL, ollamaModel, language string
	var poll time.Duration
	var once bool
	cmd := &cobra.Command{
		Use:   "worker",
		Short: "Process OCR jobs on this machine for a remote SymDesk server",
		RunE: func(cmd *cobra.Command, _ []string) error {
			worker, err := selfhost.NewWorker(selfhost.WorkerConfig{
				ServerURL: serverURL, Token: token, WorkerID: workerID, Engine: engine,
				OllamaURL: ollamaURL, OllamaModel: ollamaModel, OCRLanguage: language,
				PollEvery: poll, Once: once,
			})
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if err := worker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				return fmt.Errorf("worker: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&serverURL, "server", os.Getenv("SYMDESK_SERVER_URL"), "SymDesk server URL")
	cmd.Flags().StringVar(&token, "token", os.Getenv("SYMDESK_SERVER_TOKEN"), "server bearer token")
	cmd.Flags().StringVar(&workerID, "worker-id", os.Getenv("SYMDESK_WORKER_ID"), "stable worker name")
	cmd.Flags().StringVar(&engine, "engine", envOr("SYMDESK_WORKER_ENGINE", "auto"), "OCR engine: auto, tesseract, or ollama")
	cmd.Flags().StringVar(&ollamaURL, "ollama-url", envOr("OLLAMA_HOST", "http://127.0.0.1:11434"), "local Ollama base URL")
	cmd.Flags().StringVar(&ollamaModel, "ollama-model", os.Getenv("SYMDESK_OLLAMA_MODEL"), "Ollama vision model, for example gemma3")
	cmd.Flags().StringVar(&language, "ocr-language", envOr("SYMDESK_OCR_LANGUAGE", "deu+eng"), "Tesseract languages")
	cmd.Flags().DurationVar(&poll, "poll", 5*time.Second, "queue poll interval")
	cmd.Flags().BoolVar(&once, "once", false, "process at most one job and exit")
	return cmd
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

type homeAssistantOptions struct {
	ServerToken     string `json:"server_token"`
	WorkerToken     string `json:"worker_token"`
	Listen          string `json:"listen"`
	Vault           string `json:"vault"`
	LocalProcessing bool   `json:"local_processing"`
	WorkerEngine    string `json:"worker_engine"`
	OllamaURL       string `json:"ollama_url"`
	OllamaModel     string `json:"ollama_model"`
}

func readHomeAssistantOptions() homeAssistantOptions {
	return readHomeAssistantOptionsFromFile("/data/options.json")
}

func readHomeAssistantOptionsFromFile(path string) homeAssistantOptions {
	data, err := os.ReadFile(path)
	if err != nil {
		return homeAssistantOptions{}
	}
	var options homeAssistantOptions
	_ = json.Unmarshal(data, &options)
	return options
}

func portFromListen(listen string) string {
	for i := len(listen) - 1; i >= 0; i-- {
		if listen[i] == ':' {
			return listen[i+1:]
		}
	}
	return "8787"
}
