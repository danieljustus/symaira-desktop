package desk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

var (
	ErrSymdeskNotFound = errors.New("symdesk binary not found")
	ErrInspectFailed   = errors.New("symdesk inspect failed")
)

type InspectResult struct {
	DocumentID string `json:"document_id"`
	VaultName  string `json:"vault_name"`
	Valid      bool   `json:"valid"`
}

func IsAvailable() bool {
	_, err := exec.LookPath("symdesk")
	return err == nil
}

func InspectPath(ctx context.Context, path string) (*InspectResult, error) {
	symdeskBin, err := exec.LookPath("symdesk")
	if err != nil {
		return nil, ErrSymdeskNotFound
	}

	ctxTimeout, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctxTimeout, symdeskBin, "inspect", path, "--json")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInspectFailed, err)
	}

	var res InspectResult
	if err := json.Unmarshal(output, &res); err != nil {
		return nil, fmt.Errorf("parse inspect json: %w", err)
	}

	return &res, nil
}
