package sidecar

import (
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/danieljustus/symaira-corekit/sqlitekit"
	_ "modernc.org/sqlite"

	"github.com/danieljustus/symaira-desktop/internal/vault"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// DB represents a connection to the sidecar database.
type DB struct {
	conn *sql.DB
}

// Open opens the sidecar database at the specified path and runs migrations.
// The default path is ~/.local/share/symdesk/sidecar.db
func Open(path string) (*DB, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("user home dir: %w", err)
		}
		path = filepath.Join(home, ".local", "share", "symdesk", "sidecar.db")
	}

	conn, err := sqlitekit.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	if err := sqlitekit.Migrate(conn, migrationsFS); err != nil {
		conn.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return &DB{conn: conn}, nil
}

// Close closes the database connection.
func (db *DB) Close() error {
	return db.conn.Close()
}

// IsIndexed checks if a file is already indexed with the same SHA256.
func (db *DB) IsIndexed(path, sha256 string) (bool, error) {
	var hash string
	err := db.conn.QueryRow("SELECT sha256 FROM files WHERE path = ?", path).Scan(&hash)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	return hash == sha256, nil
}

// IndexDocument indexes a single document into the sidecar.
func (db *DB) IndexDocument(doc *vault.Document) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 1. Check if exists to potentially delete from FTS first
	var fileID int64
	err = tx.QueryRow("SELECT id FROM files WHERE path = ?", doc.Path).Scan(&fileID)
	if err != nil && err != sql.ErrNoRows {
		return err
	}

	if err == nil {
		// Found existing, remove from FTS first
		_, err = tx.Exec("DELETE FROM fts_search WHERE rowid = ?", fileID)
		if err != nil {
			return err
		}

		// Update files
		_, err = tx.Exec(`UPDATE files SET sha256 = ?, title = ?, modified_at = ?, indexed_at = ? WHERE id = ?`,
			doc.SHA256, doc.Title, doc.Created, time.Now(), fileID)
		if err != nil {
			return err
		}

		// Delete old properties and links
		_, err = tx.Exec("DELETE FROM file_properties WHERE file_id = ?", fileID)
		if err != nil {
			return err
		}
		_, err = tx.Exec("DELETE FROM links WHERE from_path = ?", doc.Path)
		if err != nil {
			return err
		}

	} else {
		// New file
		res, err := tx.Exec(`INSERT INTO files(path, sha256, title, modified_at, indexed_at) VALUES (?, ?, ?, ?, ?)`,
			doc.Path, doc.SHA256, doc.Title, doc.Created, time.Now())
		if err != nil {
			return err
		}
		fileID, err = res.LastInsertId()
		if err != nil {
			return err
		}
	}

	// 2. Insert into FTS
	_, err = tx.Exec("INSERT INTO fts_search(rowid, title, body) VALUES (?, ?, ?)", fileID, doc.Title, doc.Body)
	if err != nil {
		return err
	}

	// 3. Insert file properties
	for k, v := range doc.Frontmatter {
		valStr := fmt.Sprintf("%v", v)
		valType := "string" // Basic type inference could go here
		_, err = tx.Exec(`INSERT INTO file_properties(file_id, key, value, value_type) VALUES (?, ?, ?, ?)`,
			fileID, k, valStr, valType)
		if err != nil {
			return err
		}
	}

	// 4. Insert links
	for _, target := range doc.Links {
		// To path would typically be resolved, here we just save the name
		_, err = tx.Exec(`INSERT INTO links(from_path, to_path, kind) VALUES (?, ?, 'wikilink')`,
			doc.Path, target)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// DeleteDocument removes a document and all derived rows (FTS, properties,
// outgoing links) from the sidecar. Deleting an unindexed path is a no-op.
func (db *DB) DeleteDocument(path string) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var fileID int64
	err = tx.QueryRow("SELECT id FROM files WHERE path = ?", path).Scan(&fileID)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}

	if _, err := tx.Exec("DELETE FROM fts_search WHERE rowid = ?", fileID); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM file_properties WHERE file_id = ?", fileID); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM links WHERE from_path = ?", path); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM files WHERE id = ?", fileID); err != nil {
		return err
	}

	return tx.Commit()
}

// CheckIntegrity performs a basic check on the database.
func (db *DB) CheckIntegrity() error {
	var result string
	err := db.conn.QueryRow("PRAGMA integrity_check;").Scan(&result)
	if err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("integrity check failed: %s", result)
	}
	return nil
}

// ftsQuote turns free-form user input into a safe FTS5 query: each
// whitespace-separated token becomes a quoted prefix term, so operators
// and punctuation ("e-mail", "foo:bar") cannot break the MATCH syntax.
func ftsQuote(query string) string {
	fields := strings.Fields(query)
	terms := make([]string, 0, len(fields))
	for _, f := range fields {
		terms = append(terms, `"`+strings.ReplaceAll(f, `"`, `""`)+`"*`)
	}
	return strings.Join(terms, " ")
}

// Search performs a basic FTS search over free-form user input.
func (db *DB) Search(query string) ([]*vault.Document, error) {
	query = ftsQuote(query)
	if query == "" {
		return nil, nil
	}
	rows, err := db.conn.Query(`
		SELECT f.path, f.title, snippet(fts_search, 1, '<b>', '</b>', '...', 64) as snippet
		FROM fts_search s
		JOIN files f ON f.id = s.rowid
		WHERE fts_search MATCH ?
		ORDER BY rank LIMIT 20
	`, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var docs []*vault.Document
	for rows.Next() {
		var d vault.Document
		var snippet string
		if err := rows.Scan(&d.Path, &d.Title, &snippet); err != nil {
			return nil, err
		}
		// Storing snippet in Body for now
		d.Body = snippet
		docs = append(docs, &d)
	}
	return docs, nil
}

// ListFiles returns all files, optionally filtered by a directory prefix.
func (db *DB) ListFiles(dirPrefix string) ([]*vault.Document, error) {
	query := `SELECT path, title, modified_at FROM files`
	var args []interface{}
	if dirPrefix != "" {
		query += ` WHERE path LIKE ?`
		args = append(args, dirPrefix+"%")
	}
	query += ` ORDER BY path ASC`

	rows, err := db.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var docs []*vault.Document
	for rows.Next() {
		var d vault.Document
		if err := rows.Scan(&d.Path, &d.Title, &d.Created); err != nil {
			return nil, err
		}
		docs = append(docs, &d)
	}
	return docs, nil
}

// GetProperties returns the properties for a given file.
func (db *DB) GetProperties(path string) (map[string]interface{}, error) {
	rows, err := db.conn.Query(`
		SELECT p.key, p.value
		FROM file_properties p
		JOIN files f ON f.id = p.file_id
		WHERE f.path = ?
	`, path)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	props := make(map[string]interface{})
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		props[k] = v
	}
	return props, nil
}

// GetBacklinks returns the paths of files that link to the given path or title.
func (db *DB) GetBacklinks(path string) ([]string, error) {
	baseName := filepath.Base(path)
	title := strings.TrimSuffix(baseName, filepath.Ext(baseName))

	rows, err := db.conn.Query(`
		SELECT from_path FROM links
		WHERE to_path = ? OR to_path = ?
	`, path, title)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var links []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		links = append(links, p)
	}
	return links, nil
}

// Edge represents a link between two files.
type Edge struct {
	Source string
	Target string
}

// GetAllLinks returns all links in the database.
func (db *DB) GetAllLinks() ([]Edge, error) {
	rows, err := db.conn.Query("SELECT from_path, to_path FROM links")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var edges []Edge
	for rows.Next() {
		var s, t string
		if err := rows.Scan(&s, &t); err != nil {
			return nil, err
		}
		edges = append(edges, Edge{Source: s, Target: t})
	}
	return edges, nil
}
