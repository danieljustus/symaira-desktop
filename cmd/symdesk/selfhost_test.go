package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServeCmdFlagDefaults(t *testing.T) {
	cmd := newServeCmd()
	if cmd.Use != "serve" {
		t.Errorf("expected Use 'serve', got %q", cmd.Use)
	}

	listenFlag := cmd.Flags().Lookup("listen")
	if listenFlag == nil || listenFlag.DefValue != "127.0.0.1:8787" {
		t.Errorf("expected default listen '127.0.0.1:8787', got %v", listenFlag)
	}

	engineFlag := cmd.Flags().Lookup("worker-engine")
	if engineFlag == nil || engineFlag.DefValue != "tesseract" {
		t.Errorf("expected default worker-engine 'tesseract', got %v", engineFlag)
	}

	langFlag := cmd.Flags().Lookup("ocr-language")
	if langFlag == nil || langFlag.DefValue != "deu+eng" {
		t.Errorf("expected default ocr-language 'deu+eng', got %v", langFlag)
	}
}

func TestServeCmdLocalWorkerWithTLSConflict(t *testing.T) {
	vaultDir := t.TempDir()
	cmd := newServeCmd()
	_ = cmd.Flags().Set("local-worker", "true")
	_ = cmd.Flags().Set("tls-cert", "/tmp/cert.pem")
	_ = cmd.Flags().Set("listen", "127.0.0.1:8787")
	_ = cmd.Flags().Set("token", "12345678901234567890123456789012") // 32 chars
	t.Setenv("SYMDESK_VAULT", vaultDir)

	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("expected error when local-worker and tls-cert are both set")
	}
	wantMsg := "the built-in local worker cannot connect to a custom TLS listener; run it as a separate worker"
	if err.Error() != wantMsg {
		t.Errorf("expected error %q, got %q", wantMsg, err.Error())
	}
}

func TestServeCmdHomeAssistantOptions(t *testing.T) {
	optsDir := t.TempDir()
	optionsJSON := `{
		"server_token": "ha-server-token-32-bytes-long!!",
		"worker_token": "ha-worker-token-32-bytes-long!!",
		"listen": "0.0.0.0:8787",
		"vault": "/data/vault",
		"local_processing": true,
		"worker_engine": "tesseract",
		"ollama_url": "http://ollama:11434",
		"ollama_model": "gemma3"
	}`

	if err := os.WriteFile(filepath.Join(optsDir, "options.json"), []byte(optionsJSON), 0600); err != nil {
		t.Fatal(err)
	}

	// Temporarily override /data/options.json location by overriding system calls if needed,
	// or test readHomeAssistantOptions directly.
	opts := readHomeAssistantOptionsFromFile(filepath.Join(optsDir, "options.json"))
	if opts.ServerToken != "ha-server-token-32-bytes-long!!" {
		t.Errorf("expected server token 'ha-server-token-32-bytes-long!!', got %q", opts.ServerToken)
	}
	if opts.WorkerToken != "ha-worker-token-32-bytes-long!!" {
		t.Errorf("expected worker token 'ha-worker-token-32-bytes-long!!', got %q", opts.WorkerToken)
	}
	if opts.Listen != "0.0.0.0:8787" {
		t.Errorf("expected listen '0.0.0.0:8787', got %q", opts.Listen)
	}
	if !opts.LocalProcessing {
		t.Errorf("expected LocalProcessing true")
	}
	if opts.OllamaModel != "gemma3" {
		t.Errorf("expected OllamaModel 'gemma3', got %q", opts.OllamaModel)
	}
}

func TestWorkerCmdFlagDefaults(t *testing.T) {
	cmd := newWorkerCmd()
	if cmd.Use != "worker" {
		t.Errorf("expected Use 'worker', got %q", cmd.Use)
	}

	engineFlag := cmd.Flags().Lookup("engine")
	if engineFlag == nil || engineFlag.DefValue != "auto" {
		t.Errorf("expected default engine 'auto', got %v", engineFlag)
	}

	pollFlag := cmd.Flags().Lookup("poll")
	if pollFlag == nil || pollFlag.DefValue != (5*time.Second).String() {
		t.Errorf("expected default poll '5s', got %v", pollFlag)
	}
}

func TestPortFromListen(t *testing.T) {
	tests := []struct {
		listen string
		want   string
	}{
		{"127.0.0.1:8787", "8787"},
		{"0.0.0.0:9090", "9090"},
		{"8787", "8787"},
	}

	for _, tt := range tests {
		if got := portFromListen(tt.listen); got != tt.want {
			t.Errorf("portFromListen(%q) = %q, want %q", tt.listen, got, tt.want)
		}
	}
}
