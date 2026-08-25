package service

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/journal"
	"github.com/danieljustus/symaira-desktop/internal/retention"
)

// TimelineItem represents a single activity event or document milestone in the vault's day-level record.
type TimelineItem struct {
	Timestamp   string      `json:"timestamp"`
	Type        string      `json:"type"`
	Action      string      `json:"action"`
	Path        string      `json:"path,omitempty"`
	Title       string      `json:"title,omitempty"`
	Description string      `json:"description,omitempty"`
	Details     interface{} `json:"details,omitempty"`
}

// TimelineResult contains the merged timeline activity for a date range.
type TimelineResult struct {
	From  string         `json:"from"`
	To    string         `json:"to"`
	Total int            `json:"total"`
	Items []TimelineItem `json:"items"`
}

// RecordActivity records a journal event for the current time into the daily NDJSON journal.
func (s *Service) RecordActivity(event, path, title, details string) error {
	return s.RecordActivityAt(time.Now().UTC(), event, path, title, details)
}

// RecordActivityAt records a journal event with a specific timestamp into the daily NDJSON journal.
func (s *Service) RecordActivityAt(t time.Time, event, path, title, details string) error {
	relPath := path
	if s.VaultRoot != "" && filepath.IsAbs(path) {
		if r, err := filepath.Rel(s.VaultRoot, path); err == nil && !strings.HasPrefix(r, "..") {
			relPath = r
		}
	}
	return journal.Append(s.VaultRoot, journal.Entry{
		Timestamp: t,
		Event:     event,
		Path:      relPath,
		Title:     title,
		Details:   details,
	})
}

// parseFlexibleDate parses a date string in YYYY-MM-DD or RFC3339 format.
func parseFlexibleDate(s string, isEnd bool) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty date string")
	}

	// Try YYYY-MM-DD
	if t, err := time.Parse("2006-01-02", s); err == nil {
		if isEnd {
			return time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 999999999, time.UTC), nil
		}
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), nil
	}

	// Try RFC3339Nano
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC(), nil
	}

	// Try RFC3339
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}

	// Try 2006-01-02 15:04:05
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return t.UTC(), nil
	}

	return time.Time{}, fmt.Errorf("invalid date format %q: expected YYYY-MM-DD or RFC3339", s)
}

// Timeline merges journal entries, sidecar document dates/mtimes, meetings, and retention events
// into a unified chronological day-level activity timeline.
func (s *Service) Timeline(fromStr, toStr string, limit int) (*TimelineResult, error) {
	if strings.TrimSpace(fromStr) == "" {
		return nil, fmt.Errorf("from date is required")
	}

	from, err := parseFlexibleDate(fromStr, false)
	if err != nil {
		return nil, err
	}

	to := from.Add(24*time.Hour - time.Nanosecond)
	if strings.TrimSpace(toStr) != "" {
		parsedTo, err := parseFlexibleDate(toStr, true)
		if err != nil {
			return nil, err
		}
		to = parsedTo
	}

	if to.Before(from) {
		from, to = to, from
	}

	var items []TimelineItem

	// 1. Read persisted journal events
	journalEntries, err := journal.ReadRange(s.VaultRoot, from, to)
	if err == nil {
		for _, e := range journalEntries {
			title := e.Title
			if title == "" && s.DB != nil {
				title, _ = s.DB.GetTitle(e.Path)
			}
			desc := e.Details
			if desc == "" {
				desc = fmt.Sprintf("%s: %s", e.Event, e.Path)
			}
			items = append(items, TimelineItem{
				Timestamp:   e.Timestamp.UTC().Format(time.RFC3339),
				Type:        "journal",
				Action:      e.Event,
				Path:        e.Path,
				Title:       title,
				Description: desc,
			})
		}
	}

	// 2. Query sidecar indexed documents with document_date or modified_at in range
	if s.DB != nil {
		fromDay := from.Format("2006-01-02")
		toDay := to.Format("2006-01-02")
		docs, err := s.DB.TimelineDocs(fromDay, toDay)
		if err == nil {
			for _, doc := range docs {
				relPath, _ := filepath.Rel(s.VaultRoot, doc.Path)
				if relPath == "" {
					relPath = doc.Path
				}
				relPath = filepath.ToSlash(relPath)

				if doc.DocumentDate != "" && doc.DocumentDate >= fromDay && doc.DocumentDate <= toDay {
					ts := doc.DocumentDate + "T00:00:00Z"
					items = append(items, TimelineItem{
						Timestamp:   ts,
						Type:        "document",
						Action:      "document_date",
						Path:        relPath,
						Title:       doc.Title,
						Description: fmt.Sprintf("Document dated %s", doc.DocumentDate),
						Details: map[string]interface{}{
							"document_type": doc.DocumentType,
							"status":        doc.Status,
							"person":        doc.Person,
						},
					})
				}
			}
		}
	}

	// 3. Query meetings
	meetings, err := s.MeetingList()
	if err == nil {
		for _, m := range meetings {
			if m.StartedAt == "" {
				continue
			}
			mTime, err := time.Parse(time.RFC3339, m.StartedAt)
			if err != nil {
				mTime, err = time.Parse("2006-01-02", m.StartedAt)
			}
			if err == nil {
				mTimeUTC := mTime.UTC()
				if (mTimeUTC.Equal(from) || mTimeUTC.After(from)) && (mTimeUTC.Equal(to) || mTimeUTC.Before(to)) {
					desc := fmt.Sprintf("Meeting (%s)", m.ReviewState)
					if m.DurationMS > 0 {
						desc = fmt.Sprintf("Meeting (%s, %d mins)", m.ReviewState, m.DurationMS/60000)
					}
					items = append(items, TimelineItem{
						Timestamp:   m.StartedAt,
						Type:        "meeting",
						Action:      "meeting",
						Path:        m.Path,
						Title:       m.Title,
						Description: desc,
					})
				}
			}
		}
	}

	// 4. Query retention history
	retHistory, err := retention.LoadHistory(s.VaultRoot)
	if err == nil {
		for _, rh := range retHistory {
			rhTime := rh.Timestamp.UTC()
			if (rhTime.Equal(from) || rhTime.After(from)) && (rhTime.Equal(to) || rhTime.Before(to)) {
				items = append(items, TimelineItem{
					Timestamp:   rhTime.Format(time.RFC3339),
					Type:        "retention",
					Action:      string(rh.Action),
					Path:        rh.Path,
					Title:       rh.Title,
					Description: fmt.Sprintf("Retention action %s (%s)", rh.Action, rh.RuleName),
				})
			}
		}
	}

	// 5. Query trash list
	trashItems, err := s.TrashList()
	if err == nil {
		for _, tr := range trashItems {
			trTime := tr.DeletedAt.UTC()
			if (trTime.Equal(from) || trTime.After(from)) && (trTime.Equal(to) || trTime.Before(to)) {
				items = append(items, TimelineItem{
					Timestamp:   trTime.Format(time.RFC3339),
					Type:        "trash",
					Action:      "file_removed",
					Path:        tr.OriginalPath,
					Title:       tr.Name,
					Description: "Soft-deleted to trash",
				})
			}
		}
	}

	// Sort items by Timestamp ascending
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Timestamp < items[j].Timestamp
	})

	// Deduplicate identical consecutive items (same timestamp, type, action, path)
	deduped := make([]TimelineItem, 0, len(items))
	seen := make(map[string]bool)
	for _, it := range items {
		key := fmt.Sprintf("%s|%s|%s|%s", it.Timestamp, it.Type, it.Action, it.Path)
		if !seen[key] {
			seen[key] = true
			deduped = append(deduped, it)
		}
	}

	if limit > 0 && len(deduped) > limit {
		deduped = deduped[:limit]
	}

	return &TimelineResult{
		From:  from.Format("2006-01-02"),
		To:    to.Format("2006-01-02"),
		Total: len(deduped),
		Items: deduped,
	}, nil
}
