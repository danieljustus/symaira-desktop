package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-corekit/ollamakit"
	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/secrets"
)

func newAIConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ai-config",
		Short: "Manage the AI provider configuration (Ollama / Anthropic)",
	}

	cmd.AddCommand(newAIConfigShowCmd())
	cmd.AddCommand(newAIConfigSetCmd())
	cmd.AddCommand(newAIConfigTestCmd())

	return cmd
}

func newAIConfigShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show the current AI provider configuration",
		RunE: func(cmd *cobra.Command, _ []string) error {
			provider := cfg.LLMProvider
			if provider == "" {
				provider = "ollama"
			}

			result := map[string]interface{}{
				"provider":    provider,
				"ollama_url":  cfg.OllamaURL,
				"model":       cfg.LLMModel,
				"max_tokens":  cfg.MaxTokens,
				"has_api_key": cfg.LLMAPIKey != "",
			}

			if jsonFlag {
				b, _ := json.Marshal(result)
				fmt.Println(string(b))
			} else {
				fmt.Printf("Provider: %s\n", provider)
				fmt.Printf("Ollama URL: %s\n", cfg.OllamaURL)
				fmt.Printf("Model: %s\n", cfg.LLMModel)
				fmt.Printf("Max tokens: %d\n", cfg.MaxTokens)
				fmt.Printf("API key configured: %v\n", cfg.LLMAPIKey != "")
			}
			return nil
		},
	}
}

func newAIConfigSetCmd() *cobra.Command {
	var provider, ollamaURL, model, apiKey string
	var maxTokens int

	c := &cobra.Command{
		Use:   "set",
		Short: "Update the AI provider configuration",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if cmd.Flags().Changed("provider") {
				cfg.LLMProvider = provider
			}
			if cmd.Flags().Changed("ollama-url") {
				cfg.OllamaURL = ollamaURL
			}
			if cmd.Flags().Changed("model") {
				cfg.LLMModel = model
			}
			if cmd.Flags().Changed("api-key") {
				cfg.LLMAPIKey = apiKey
			}
			if cmd.Flags().Changed("max-tokens") {
				cfg.MaxTokens = maxTokens
			}

			configPath := config.GlobalPath()
			if err := config.Save(configPath, cfg); err != nil {
				return fmt.Errorf("failed to save config: %w", err)
			}

			if jsonFlag {
				result := map[string]string{"status": "ok"}
				b, _ := json.Marshal(result)
				fmt.Println(string(b))
			} else {
				fmt.Println("AI configuration saved.")
			}
			return nil
		},
	}

	c.Flags().StringVar(&provider, "provider", "", "AI provider: ollama, anthropic or none")
	c.Flags().StringVar(&ollamaURL, "ollama-url", "", "Ollama endpoint URL")
	c.Flags().StringVar(&model, "model", "", "Model name")
	c.Flags().StringVar(&apiKey, "api-key", "", "API key or symvault reference (op://...)")
	c.Flags().IntVar(&maxTokens, "max-tokens", 0, "Maximum tokens per response")

	return c
}

func newAIConfigTestCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "test",
		Short: "Test connectivity to the configured AI provider",
		RunE: func(cmd *cobra.Command, _ []string) error {
			provider := cfg.LLMProvider
			if provider == "" {
				provider = "ollama"
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()

			result := map[string]interface{}{"provider": provider}

			switch provider {
			case "anthropic":
				apiKey := secrets.ResolveKey(cfg.LLMAPIKey)
				if apiKey == "" {
					result["ok"] = false
					result["error"] = "Anthropic API key could not be resolved (missing secret via symvault or environment variable)."
				} else {
					result["ok"] = true
				}
			default:
				baseURL := cfg.OllamaURL
				if baseURL == "" {
					result["ok"] = false
					result["error"] = "Ollama URL is not set."
				} else {
					client := ollamakit.New(ollamakit.Config{BaseURL: baseURL})
					if err := client.Ping(ctx); err != nil {
						result["ok"] = false
						result["error"] = err.Error()
					} else {
						models, err := client.ListModels(ctx)
						if err != nil {
							result["ok"] = true
							result["models"] = []string{}
							result["models_error"] = err.Error()
						} else {
							names := make([]string, 0, len(models))
							for _, m := range models {
								names = append(names, m.Name)
							}
							result["ok"] = true
							result["models"] = names
						}
					}
				}
			}

			if jsonFlag {
				b, _ := json.Marshal(result)
				fmt.Println(string(b))
			} else {
				if ok, _ := result["ok"].(bool); ok {
					fmt.Println("Connection OK.")
					if models, ok := result["models"].([]string); ok && len(models) > 0 {
						fmt.Printf("Models: %v\n", models)
					}
				} else {
					fmt.Printf("Connection failed: %v\n", result["error"])
				}
			}
			return nil
		},
	}
}
