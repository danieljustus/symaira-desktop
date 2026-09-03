package main

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/service"
)

// addBatchStdinFlag registers the shared --stdin flag on batch doc commands.
func addBatchStdinFlag(cmd *cobra.Command) {
	cmd.Flags().Bool("stdin", false, "read file paths from stdin (newline-separated)")
}

// readStdinPaths reads newline-separated file paths, skipping blank lines.
func readStdinPaths(r io.Reader) ([]string, error) {
	var paths []string
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			paths = append(paths, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read stdin: %w", err)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no file paths on stdin")
	}
	return paths, nil
}

// splitBatchArgs resolves files and the trailing value for value-last batch
// commands (doc status|due|type|correspondent). With --stdin the sole
// positional argument is the value and files come from stdin.
func splitBatchArgs(cmd *cobra.Command, args []string) (files []string, value string, err error) {
	useStdin, _ := cmd.Flags().GetBool("stdin")
	if useStdin {
		if len(args) != 1 {
			return nil, "", fmt.Errorf("with --stdin, pass exactly one value argument")
		}
		files, err = readStdinPaths(cmd.InOrStdin())
		return files, args[0], err
	}
	if len(args) < 2 {
		return nil, "", fmt.Errorf("need at least one file and a value (or use --stdin)")
	}
	return args[:len(args)-1], args[len(args)-1], nil
}

// runDocBatch applies a value-last mutation across all given files and reports
// per-file results. A single-file failure returns the error directly (keeping
// the previous single-document exit behaviour); with multiple files partial
// failures are reported per file in the result payload.
func runDocBatch(cmd *cobra.Command, args []string, fn func(svc *service.Service, file, value string) error) error {
	files, value, err := splitBatchArgs(cmd, args)
	if err != nil {
		return err
	}
	return execDocBatch(files, func(svc *service.Service, file string) error {
		return fn(svc, file, value)
	})
}

// runDocTagBatch applies a tag mutation: the first positional argument is the
// tag, the remaining arguments (or stdin with --stdin) are the files.
func runDocTagBatch(cmd *cobra.Command, args []string, fn func(svc *service.Service, file, tag string) error) error {
	tag := args[0]
	var files []string
	useStdin, _ := cmd.Flags().GetBool("stdin")
	if useStdin {
		if len(args) != 1 {
			return fmt.Errorf("with --stdin, pass exactly one tag argument")
		}
		var err error
		files, err = readStdinPaths(cmd.InOrStdin())
		if err != nil {
			return err
		}
	} else {
		if len(args) < 2 {
			return fmt.Errorf("need a tag and at least one file (or use --stdin)")
		}
		files = args[1:]
	}
	return execDocBatch(files, func(svc *service.Service, file string) error {
		return fn(svc, file, tag)
	})
}

func execDocBatch(files []string, fn func(svc *service.Service, file string) error) error {
	vRoot, db, err := initServiceDeps()
	if err != nil {
		return err
	}
	defer closeWithWarning("sidecar database", db.Close)
	svc := service.New(vRoot, db)

	results, updated, failed := svc.DocBatch(files, func(file string) error {
		return fn(svc, file)
	})

	if len(files) == 1 && failed == 1 {
		return fmt.Errorf("%s: %s", results[0].File, results[0].Error)
	}
	return outputResult(map[string]interface{}{
		"status":  batchStatus(updated, failed),
		"results": results,
		"updated": updated,
		"failed":  failed,
	})
}

func batchStatus(updated, failed int) string {
	switch {
	case failed == 0:
		return "updated"
	case updated == 0:
		return "failed"
	default:
		return "partial"
	}
}
