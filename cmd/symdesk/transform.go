package main

import (
	"io"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/ai"
)

func newTransformCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "transform <intent>",
		Short: "Transform text with AI (summarize|rewrite|continue); reads text from --text or stdin",
		Long: "Transform a piece of text with a local AI action. The intent is one of " +
			"summarize, rewrite or continue. The text is taken from --text, or read from " +
			"stdin when the flag is empty. Operates purely on the given text and never " +
			"touches the vault; degrades honestly when Ollama is not configured.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			text, _ := cmd.Flags().GetString("text")
			if text == "" {
				data, err := io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return err
				}
				text = string(data)
			}

			out := make(chan interface{})
			go func() {
				chunks := make(chan ai.AskChunk)
				go ai.Transform(cmd.Context(), cfg, text, args[0], chunks)
				for c := range chunks {
					out <- c
				}
				close(out)
			}()

			return outputStream(out)
		},
	}
	cmd.Flags().String("text", "", "Text to transform (otherwise read from stdin)")
	return cmd
}
