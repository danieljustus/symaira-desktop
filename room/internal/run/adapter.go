package run

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/danieljustus/symaira-room/internal/config"
	"github.com/danieljustus/symaira-room/internal/identity"
)

var (
	ErrAdapterNotApproved = errors.New("adapter is not covered by approval scope")
	ErrAdapterNotFound    = errors.New("configured adapter not found")
)

func ExecuteAdapter(ctx context.Context, roomDir, runID string, id *identity.Identity, cfg *config.Config) error {
	r, err := Get(roomDir, runID)
	if err != nil {
		return err
	}

	if r.Scope != "" && r.Scope != "all" && r.Adapter != "" && !strings.Contains(r.Scope, r.Adapter) {
		return fmt.Errorf("%w: scope '%s' does not authorize adapter '%s'", ErrAdapterNotApproved, r.Scope, r.Adapter)
	}

	adapterName := r.Adapter
	if adapterName == "" {
		adapterName = "default"
	}

	adapterCfg, exists := cfg.Adapters[adapterName]
	if !exists || len(adapterCfg.Command) == 0 {
		return fmt.Errorf("%w: '%s'", ErrAdapterNotFound, adapterName)
	}

	logDir := filepath.Join(roomDir, ".symroom", "runs", runID)
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return err
	}

	stdoutPath := filepath.Join(logDir, "stdout.log")
	stderrPath := filepath.Join(logDir, "stderr.log")

	stdoutFile, err := os.Create(stdoutPath)
	if err != nil {
		return err
	}
	defer func() { _ = stdoutFile.Close() }()

	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		return err
	}
	defer func() { _ = stderrFile.Close() }()

	promptVal := r.Title
	if r.PlanFile != "" {
		if content, err := os.ReadFile(filepath.Join(roomDir, r.PlanFile)); err == nil {
			promptVal = string(content)
		}
	}

	var cmdArgs []string
	for _, arg := range adapterCfg.Command {
		s := strings.ReplaceAll(arg, "{prompt}", promptVal)
		s = strings.ReplaceAll(s, "{room_artifact_root}", roomDir)
		cmdArgs = append(cmdArgs, s)
	}

	if r.State == StateApproved {
		if _, err := Start(roomDir, runID, id); err != nil {
			return err
		}
	}

	cmd := exec.CommandContext(ctx, cmdArgs[0], cmdArgs[1:]...)
	if adapterCfg.Workdir != "" {
		cmd.Dir = strings.ReplaceAll(adapterCfg.Workdir, "{room_artifact_root}", roomDir)
	} else {
		cmd.Dir = roomDir
	}

	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile

	startTime := time.Now()
	runErr := cmd.Run()
	duration := time.Since(startTime)

	_ = stdoutFile.Sync()
	_ = stderrFile.Sync()

	if runErr != nil {
		errMsg := fmt.Sprintf("exit error: %v (duration: %v)", runErr, duration)
		_, _ = Fail(roomDir, runID, errMsg, id)
		return runErr
	}

	summary := fmt.Sprintf("Adapter %s completed in %v", adapterName, duration)
	_, _ = Finish(roomDir, runID, summary, nil, id)
	return nil
}
