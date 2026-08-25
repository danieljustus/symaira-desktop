package run

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var (
	ErrWaitTimeout = errors.New("wait timed out before approval decision")
	ErrRunDenied   = errors.New("run was denied")
)

func Wait(ctx context.Context, roomDir, runID string, pollInterval time.Duration) (*Run, error) {
	if pollInterval <= 0 {
		pollInterval = 500 * time.Millisecond
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		r, err := Get(roomDir, runID)
		if err == nil {
			switch r.State {
			case StateApproved:
				return r, nil
			case StateDenied:
				return r, fmt.Errorf("%w: %s", ErrRunDenied, r.Error)
			case StateCancelled:
				return r, fmt.Errorf("%w: run cancelled", ErrRunDenied)
			}
		}

		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, ErrWaitTimeout
			}
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
