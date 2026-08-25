// Package journal persists day-level vault activity events as daily NDJSON files
// under <vaultRoot>/.symdesk/journal/YYYY-MM-DD.ndjson.
package journal

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Entry is a single journal activity event recorded in NDJSON format.
type Entry struct {
	Timestamp time.Time `json:"ts"`
	Event     string    `json:"event"`
	Path      string    `json:"path"`
	Title     string    `json:"title,omitempty"`
	Details   string    `json:"details,omitempty"`
}

// UnmarshalJSON supports parsing RFC3339, RFC3339Nano, date strings, or Unix seconds for Timestamp.
func (e *Entry) UnmarshalJSON(data []byte) error {
	type Alias Entry
	aux := &struct {
		RawTS interface{} `json:"ts"`
		*Alias
	}{
		Alias: (*Alias)(e),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if aux.RawTS != nil {
		switch v := aux.RawTS.(type) {
		case string:
			if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
				e.Timestamp = t.UTC()
			} else if t, err := time.Parse(time.RFC3339, v); err == nil {
				e.Timestamp = t.UTC()
			} else if t, err := time.Parse("2006-01-02", v); err == nil {
				e.Timestamp = t.UTC()
			}
		case float64:
			e.Timestamp = time.Unix(int64(v), 0).UTC()
		}
	}
	return nil
}

// MarshalJSON ensures timestamp is formatted as UTC RFC3339.
func (e Entry) MarshalJSON() ([]byte, error) {
	ts := e.Timestamp
	if ts.IsZero() {
		ts = time.Now().UTC()
	} else {
		ts = ts.UTC()
	}

	type Alias Entry
	return json.Marshal(&struct {
		TS string `json:"ts"`
		Alias
	}{
		TS:    ts.Format(time.RFC3339),
		Alias: (Alias)(e),
	})
}

// JournalDir returns the path to the hidden journal folder inside vaultRoot.
func JournalDir(vaultRoot string) string {
	return filepath.Join(vaultRoot, ".symdesk", "journal")
}

// DailyFilePath returns the NDJSON path for a given date in vaultRoot.
func DailyFilePath(vaultRoot string, t time.Time) string {
	return filepath.Join(JournalDir(vaultRoot), t.UTC().Format("2006-01-02")+".ndjson")
}

var writeMu sync.Mutex

// Append writes an event entry to the daily NDJSON journal file for entry.Timestamp.
func Append(vaultRoot string, entry Entry) error {
	if vaultRoot == "" {
		return fmt.Errorf("vault root is required")
	}

	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	} else {
		entry.Timestamp = entry.Timestamp.UTC()
	}

	// Normalize path to vault-relative if within vaultRoot
	if filepath.IsAbs(entry.Path) && vaultRoot != "" {
		if rel, err := filepath.Rel(vaultRoot, entry.Path); err == nil && !strings.HasPrefix(rel, "..") {
			entry.Path = filepath.ToSlash(rel)
		}
	} else if entry.Path != "" {
		entry.Path = filepath.ToSlash(filepath.Clean(entry.Path))
	}

	dir := JournalDir(vaultRoot)
	writeMu.Lock()
	defer writeMu.Unlock()

	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create journal dir: %w", err)
	}

	filePath := DailyFilePath(vaultRoot, entry.Timestamp)
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open journal file: %w", err)
	}
	defer f.Close()

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal journal entry: %w", err)
	}

	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write journal entry: %w", err)
	}
	return nil
}

// ReadDay reads all journal entries for a specific YYYY-MM-DD date.
func ReadDay(vaultRoot string, day time.Time) ([]Entry, error) {
	filePath := DailyFilePath(vaultRoot, day)
	f, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var entries []Entry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		entries = append(entries, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

// ReadRange reads all journal entries between from and to (inclusive).
func ReadRange(vaultRoot string, from, to time.Time) ([]Entry, error) {
	fromUTC := from.UTC()
	toUTC := to.UTC()
	if toUTC.Before(fromUTC) {
		fromUTC, toUTC = toUTC, fromUTC
	}

	// Iterate day by day from fromUTC date to toUTC date
	fromDay := time.Date(fromUTC.Year(), fromUTC.Month(), fromUTC.Day(), 0, 0, 0, 0, time.UTC)
	toDay := time.Date(toUTC.Year(), toUTC.Month(), toUTC.Day(), 0, 0, 0, 0, time.UTC)

	var allEntries []Entry
	for curr := fromDay; !curr.After(toDay); curr = curr.AddDate(0, 0, 1) {
		entries, err := ReadDay(vaultRoot, curr)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if (e.Timestamp.Equal(fromUTC) || e.Timestamp.After(fromUTC)) &&
				(e.Timestamp.Equal(toUTC) || e.Timestamp.Before(toUTC)) {
				allEntries = append(allEntries, e)
			}
		}
	}
	return allEntries, nil
}

// Prune removes journal NDJSON files older than maxAge.
func Prune(vaultRoot string, maxAge time.Duration) (int, error) {
	if maxAge <= 0 {
		return 0, nil
	}
	dir := JournalDir(vaultRoot)
	files, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	cutoff := time.Now().UTC().Add(-maxAge)
	cutoffDay := time.Date(cutoff.Year(), cutoff.Month(), cutoff.Day(), 0, 0, 0, 0, time.UTC)

	removed := 0
	for _, fi := range files {
		if fi.IsDir() || !strings.HasSuffix(fi.Name(), ".ndjson") {
			continue
		}
		dateStr := strings.TrimSuffix(fi.Name(), ".ndjson")
		t, err := time.Parse("2006-01-02", dateStr)
		if err != nil {
			continue
		}
		if t.Before(cutoffDay) {
			if err := os.Remove(filepath.Join(dir, fi.Name())); err == nil {
				removed++
			}
		}
	}
	return removed, nil
}
