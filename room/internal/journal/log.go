package journal

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/danieljustus/symaira-room/internal/event"
)

type LogFilter struct {
	Since  string
	Until  string
	Kind   string
	Author string
	Run    string
	Limit  int
}

type LogResult struct {
	Events       []*event.Event
	InvalidCount int
}

func (j *Journal) QueryLog(filter LogFilter) (*LogResult, error) {
	report, err := j.Verify()
	if err != nil {
		return nil, fmt.Errorf("verify error: %w", err)
	}

	invalidIDs := make(map[string]bool)
	for _, f := range report.Findings {
		if f.Code == CodeSignatureInvalid || f.Code == CodeChainBroken {
			if f.EventID != "" {
				invalidIDs[f.EventID] = true
			}
		}
	}

	merged, err := j.MergeAll()
	if err != nil {
		return nil, fmt.Errorf("merge all: %w", err)
	}

	var filtered []*event.Event
	for _, ev := range merged {
		if invalidIDs[ev.ID] {
			continue
		}

		if filter.Since != "" && ev.TS < filter.Since {
			continue
		}
		if filter.Until != "" && ev.TS > filter.Until {
			continue
		}
		if filter.Kind != "" && ev.Kind != filter.Kind {
			continue
		}
		if filter.Author != "" && ev.Author != filter.Author && !strings.EqualFold(ev.Author, filter.Author) {
			continue
		}
		if filter.Run != "" {
			var bodyMap map[string]any
			if err := json.Unmarshal(ev.Body, &bodyMap); err == nil {
				runID, _ := bodyMap["run_id"].(string)
				if runID == "" {
					runID, _ = bodyMap["run"].(string)
				}
				if runID != filter.Run {
					continue
				}
			} else {
				continue
			}
		}

		filtered = append(filtered, ev)
	}

	if filter.Limit > 0 && len(filtered) > filter.Limit {
		filtered = filtered[:filter.Limit]
	}

	return &LogResult{
		Events:       filtered,
		InvalidCount: len(invalidIDs),
	}, nil
}

func FormatEventHuman(ev *event.Event) string {
	summary := ""
	var bodyMap map[string]any
	if err := json.Unmarshal(ev.Body, &bodyMap); err == nil {
		if text, ok := bodyMap["text"].(string); ok {
			summary = text
		} else if name, ok := bodyMap["name"].(string); ok {
			summary = name
		} else {
			summary = string(ev.Body)
		}
	} else {
		summary = string(ev.Body)
	}
	return fmt.Sprintf("[%s] %s (%s): %s", ev.TS, ev.Author, ev.Kind, summary)
}

func PrintLogWarnings(invalidCount int) {
	if invalidCount > 0 {
		fmt.Fprintf(os.Stderr, "Warning: %d invalid event(s) omitted from log. Run 'symroom verify' for details.\n", invalidCount)
	}
}
