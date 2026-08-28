package sidecar

import (
	"database/sql"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// IndexState is the derived retrieval lifecycle for one source document.
// Markdown remains authoritative; this state is safe to rebuild from a vault.
type IndexState string

const (
	IndexStateQueued      IndexState = "queued"
	IndexStateIndexing    IndexState = "indexing"
	IndexStateIndexed     IndexState = "indexed"
	IndexStateFailed      IndexState = "failed"
	IndexStateEncrypted   IndexState = "encrypted"
	IndexStateUnsupported IndexState = "unsupported"
)

var validIndexStates = map[IndexState]bool{
	IndexStateQueued: true, IndexStateIndexing: true, IndexStateIndexed: true,
	IndexStateFailed: true, IndexStateEncrypted: true, IndexStateUnsupported: true,
}

// IndexStatus is a diagnostic row for a source document.
type IndexStatus struct {
	Path      string     `json:"path"`
	State     IndexState `json:"index_state"`
	Reason    string     `json:"index_failure_reason,omitempty"`
	UpdatedAt time.Time  `json:"index_updated_at"`
}

// SetIndexStatus records derived lifecycle state. It intentionally has no
// foreign key: failed and unsupported source files are not present in files.
func (db *DB) SetIndexStatus(path string, state IndexState, reason string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("index status path is required")
	}
	if !validIndexStates[state] {
		return fmt.Errorf("invalid index state %q", state)
	}
	_, err := db.conn.Exec(`INSERT INTO index_lifecycle(path, state, reason, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET state = excluded.state, reason = excluded.reason, updated_at = excluded.updated_at`,
		path, state, reason, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// GetIndexStatus returns a recorded status. The bool is false for paths that
// have not yet been seen by an index pass.
func (db *DB) GetIndexStatus(path string) (IndexStatus, bool, error) {
	var status IndexStatus
	var state, updated string
	err := db.conn.QueryRow(`SELECT path, state, reason, updated_at FROM index_lifecycle WHERE path = ?`, path).
		Scan(&status.Path, &state, &status.Reason, &updated)
	if err == sql.ErrNoRows {
		return IndexStatus{}, false, nil
	}
	if err != nil {
		return IndexStatus{}, false, err
	}
	status.State = IndexState(state)
	status.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return IndexStatus{}, false, fmt.Errorf("parse index status timestamp: %w", err)
	}
	return status, true, nil
}

// ListIndexStatuses lists diagnostics in stable path order. An empty state
// returns all rows.
func (db *DB) ListIndexStatuses(state IndexState) ([]IndexStatus, error) {
	query := `SELECT path, state, reason, updated_at FROM index_lifecycle`
	args := []interface{}{}
	if state != "" {
		if !validIndexStates[state] {
			return nil, fmt.Errorf("invalid index state %q", state)
		}
		query += ` WHERE state = ?`
		args = append(args, state)
	}
	query += ` ORDER BY path ASC`
	rows, err := db.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []IndexStatus
	for rows.Next() {
		var item IndexStatus
		var state, updated string
		if err := rows.Scan(&item.Path, &state, &item.Reason, &updated); err != nil {
			return nil, err
		}
		item.State = IndexState(state)
		item.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
		if err != nil {
			return nil, fmt.Errorf("parse index status timestamp: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// DeleteIndexStatus removes one derived diagnostic row. It never touches the
// Markdown source file.
func (db *DB) DeleteIndexStatus(path string) error {
	_, err := db.conn.Exec(`DELETE FROM index_lifecycle WHERE path = ?`, path)
	return err
}

// PruneIndexStatuses removes diagnostics for paths no longer present in the
// vault. It only deletes derived rows; source files are never modified.
func (db *DB) PruneIndexStatuses(vaultRoot string) (int, error) {
	valid := make(map[string]bool)
	if err := vault.WalkAll(vaultRoot, func(path string, _ fs.DirEntry) error {
		valid[path] = true
		return nil
	}); err != nil {
		return 0, fmt.Errorf("walk vault for index status cleanup: %w", err)
	}
	rows, err := db.conn.Query(`SELECT path FROM index_lifecycle`)
	if err != nil {
		return 0, err
	}
	var stale []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return 0, err
		}
		if !valid[path] {
			stale = append(stale, path)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, path := range stale {
		if err := db.DeleteIndexStatus(path); err != nil {
			return 0, err
		}
	}
	return len(stale), nil
}
