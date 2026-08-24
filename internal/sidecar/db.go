package sidecar

import (
	"crypto/sha256"
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/danieljustus/symaira-corekit/sqlitekit"
	_ "modernc.org/sqlite"

	"github.com/danieljustus/symaira-desktop/internal/searchquery"
	"github.com/danieljustus/symaira-desktop/internal/simhash"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// OpenForVault keeps rebuildable indexes isolated per vault. This prevents a
// configured vault from listing or searching rows indexed for another vault.
func OpenForVault(vaultRoot string) (*DB, error) {
	if explicit := os.Getenv("SYMDESK_SIDECAR"); explicit != "" {
		return Open(explicit)
	}
	canonical, err := filepath.Abs(vaultRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve vault for sidecar: %w", err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(canonical); resolveErr == nil {
		canonical = resolved
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("user home dir: %w", err)
	}
	dataRoot := os.Getenv("XDG_DATA_HOME")
	if dataRoot == "" {
		dataRoot = filepath.Join(home, ".local", "share")
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(canonical)))
	return Open(filepath.Join(dataRoot, "symdesk", "vaults", digest[:16], "sidecar.db"))
}

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

// FileStat is the cached on-disk size/mtime for an indexed file, as recorded
// at the time it was last indexed.
type FileStat struct {
	Size    int64
	ModTime int64 // UnixNano
}

// StatCache returns the cached size/mtime for path. ok is false when the
// file is not indexed, or when it was indexed without a reliable mtime (see
// vault.Document.ModTime) — in both cases the caller cannot trust a stat
// comparison and must fall back to a full parse + hash check.
func (db *DB) StatCache(path string) (stat FileStat, ok bool, err error) {
	var size, mtime sql.NullInt64
	err = db.conn.QueryRow("SELECT size, mtime_ns FROM files WHERE path = ?", path).Scan(&size, &mtime)
	if err != nil {
		if err == sql.ErrNoRows {
			return FileStat{}, false, nil
		}
		return FileStat{}, false, err
	}
	if !size.Valid || !mtime.Valid {
		return FileStat{}, false, nil
	}
	return FileStat{Size: size.Int64, ModTime: mtime.Int64}, true, nil
}

// SetFileStat refreshes the cached size/mtime for an already-indexed path
// without touching its content hash, title, or any derived index rows (FTS,
// properties, links). It is used when a file's SHA-256 still matches the
// index but its stat cache was missing or stale (e.g. its first refresh
// after the size/mtime columns were introduced), so later refreshes can use
// the fast path without re-running a full IndexDocument.
func (db *DB) SetFileStat(path string, size int64, modTimeUnixNano int64) error {
	_, err := db.conn.Exec("UPDATE files SET size = ?, mtime_ns = ? WHERE path = ?", size, modTimeUnixNano, path)
	return err
}

// RefreshIndex walks vaultRoot and brings the index up to date with every
// Markdown file found. For each file it first tries a cheap os.Stat-based
// fast path: if the cached size and mtime from the last index still match
// the file on disk, the file is skipped entirely without reading or hashing
// it. This is what lets a warm start over an unchanged vault avoid a full
// read of every file. Whenever the cached stat is missing or does not match
// — including a same-size edit, where the content changed but the mtime
// still differs — it falls back to the pre-existing correctness path: parse
// the file, hash it, and compare against the stored SHA-256 via IsIndexed
// before deciding whether a re-index is actually needed.
func (db *DB) RefreshIndex(vaultRoot string) error {
	return vault.Walk(vaultRoot, func(path string) error {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if cached, ok, err := db.StatCache(path); err != nil {
			return err
		} else if ok && cached.Size == info.Size() && cached.ModTime == info.ModTime().UnixNano() {
			return nil
		}

		doc, err := vault.ParseFile(path)
		if err != nil {
			return err
		}
		indexed, err := db.IsIndexed(doc.Path, doc.SHA256)
		if err != nil {
			return err
		}
		if indexed {
			// Content is unchanged (e.g. only the mtime moved without a real
			// edit, or this is the first refresh after the stat cache was
			// added); just record the stat so future refreshes can skip it.
			return db.SetFileStat(doc.Path, doc.Size, doc.ModTime.UnixNano())
		}
		return db.IndexDocument(doc)
	})
}

// IndexDocument indexes a single document into the sidecar.
func (db *DB) IndexDocument(doc *vault.Document) error {
	if doc.ASN != nil {
		if err := vault.ValidateASN(*doc.ASN); err != nil {
			return fmt.Errorf("invalid document ASN: %w", err)
		}
	}
	if doc.Simhash == "" && doc.Body != "" {
		doc.Simhash = simhash.ComputeHex(doc.Body)
	}

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
		_, err = tx.Exec(`UPDATE files SET sha256 = ?, title = ?, modified_at = ?, indexed_at = ?,
			"type" = ?, document_date = ?, person = ?, status = ?, due_date = ?, confidence = ?, ocr_json_path = ?, simhash = ?, asn = ?,
			size = ?, mtime_ns = ?
			WHERE id = ?`,
			doc.SHA256, doc.Title, doc.Created, time.Now(),
			doc.Type,
			nullStr(doc.DocumentDate), nullStr(doc.Person), nullStr(doc.Status),
			nullStr(doc.DueDate), nullInt(doc.Confidence), nullStr(doc.OcrJSONPath), nullStr(doc.Simhash), nullASN(doc.ASN),
			doc.Size, nullModTime(doc.ModTime),
			fileID)
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
		res, err := tx.Exec(`INSERT INTO files(path, sha256, title, modified_at, indexed_at,
			"type", document_date, person, status, due_date, confidence, ocr_json_path, simhash, asn, size, mtime_ns)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			doc.Path, doc.SHA256, doc.Title, doc.Created, time.Now(),
			doc.Type,
			nullStr(doc.DocumentDate), nullStr(doc.Person), nullStr(doc.Status),
			nullStr(doc.DueDate), nullInt(doc.Confidence), nullStr(doc.OcrJSONPath), nullStr(doc.Simhash), nullASN(doc.ASN),
			doc.Size, nullModTime(doc.ModTime))
		if err != nil {
			return err
		}
		fileID, err = res.LastInsertId()
		if err != nil {
			return err
		}
	}

	// 2. Insert into FTS
	ftsTitle := doc.Title
	if doc.ASN != nil {
		ftsTitle = fmt.Sprintf("%s ASN %d", ftsTitle, *doc.ASN)
	}
	_, err = tx.Exec("INSERT INTO fts_search(rowid, title, body) VALUES (?, ?, ?)", fileID, ftsTitle, doc.Body)
	if err != nil {
		return err
	}

	// 3. Insert file properties
	for k, v := range doc.Frontmatter {
		if k == "tags" {
			continue // Handled below to ensure doc.Tags (frontmatter + inline) is indexed
		}
		valStr := fmt.Sprintf("%v", v)
		valType := "string" // Basic type inference could go here
		_, err = tx.Exec(`INSERT INTO file_properties(file_id, key, value, value_type) VALUES (?, ?, ?, ?)`,
			fileID, k, valStr, valType)
		if err != nil {
			return err
		}
	}
	if len(doc.Tags) > 0 {
		valStr := fmt.Sprintf("%v", doc.Tags)
		_, err = tx.Exec(`INSERT INTO file_properties(file_id, key, value, value_type) VALUES (?, ?, ?, ?)`,
			fileID, "tags", valStr, "string")
		if err != nil {
			return err
		}
	} else if _, ok := doc.Frontmatter["tags"]; ok {
		valStr := fmt.Sprintf("%v", doc.Frontmatter["tags"])
		_, err = tx.Exec(`INSERT INTO file_properties(file_id, key, value, value_type) VALUES (?, ?, ?, ?)`,
			fileID, "tags", valStr, "string")
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

	// 5. Correspondent backlink: if correspondent matches an existing note title, record a link edge
	if doc.Frontmatter != nil {
		if correspondent, ok := doc.Frontmatter["correspondent"].(string); ok && correspondent != "" {
			var matchPath string
			err = tx.QueryRow(`SELECT path FROM files WHERE title = ? AND path != ? LIMIT 1`,
				correspondent, doc.Path).Scan(&matchPath)
			if err == nil && matchPath != "" {
				_, err = tx.Exec(`INSERT OR IGNORE INTO links(from_path, to_path, kind) VALUES (?, ?, 'correspondent')`,
					doc.Path, matchPath)
				if err != nil {
					return err
				}
			}
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

	if err := deleteDocumentRows(tx, path); err != nil {
		return err
	}

	return tx.Commit()
}

// deleteDocumentRows removes all sidecar rows for the given path within an
// existing transaction. It is a no-op if the path is not indexed.
func deleteDocumentRows(tx *sql.Tx, path string) error {
	var fileID int64
	err := tx.QueryRow("SELECT id FROM files WHERE path = ?", path).Scan(&fileID)
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
	return nil
}

// Prune removes indexed entries for files that are no longer on disk or that
// fall under vault ignore rules (hidden directories such as .git / .obsidian,
// and conventional build-artifact directories such as node_modules, vendor,
// dist, build, venv). Returns the number of pruned entries.
//
// The prune logic mirrors the same ignore semantics used by vault.Walk and
// RefreshIndex: it walks the vault root to collect currently valid paths, then
// deletes every indexed path not found in that set.
func (db *DB) Prune(vaultRoot string) (int, error) {
	// Build a set of valid paths by walking the vault (respects ignore rules).
	valid := make(map[string]bool)
	if err := vault.Walk(vaultRoot, func(path string) error {
		valid[path] = true
		return nil
	}); err != nil {
		return 0, fmt.Errorf("walk vault for prune: %w", err)
	}

	// Collect stale paths by diffing the index against the valid set.
	rows, err := db.conn.Query("SELECT path FROM files")
	if err != nil {
		return 0, fmt.Errorf("query indexed paths for prune: %w", err)
	}
	defer func() { _ = rows.Close() }()

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

	if len(stale) == 0 {
		return 0, nil
	}

	// Delete all stale entries in a single transaction.
	tx, err := db.conn.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin prune tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, path := range stale {
		if err := deleteDocumentRows(tx, path); err != nil {
			return 0, fmt.Errorf("prune %s: %w", path, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit prune: %w", err)
	}

	return len(stale), nil
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

// ftsSnippetExpr is the FTS5 snippet expression shared by every search path,
// so the three query sites cannot drift apart.
//
// The highlight delimiters are empty on purpose. A snippet is plain text, not
// HTML: the apps render it verbatim, and the same string is handed to the model
// as citation and notebook source material. HTML markers therefore showed up as
// literal "<b>" in the UI and as noise in prompts (issue #441). Highlighting,
// if it is ever wanted, belongs in a separate structured field rather than as
// inline markup in this one.
const ftsSnippetExpr = `snippet(fts_search, 1, '', '', '...', 64)`

// Search performs a basic FTS search over free-form user input.
func (db *DB) Search(query string) ([]*vault.Document, error) {
	query = ftsQuote(query)
	if query == "" {
		return nil, nil
	}
	rows, err := db.conn.Query(`
		SELECT f.path, f.title, `+ftsSnippetExpr+` as snippet
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

// SearchScoped is Search restricted to allowedPaths (absolute), ranked by
// FTS relevance. Scoping happens in SQL via a path IN-list rather than by
// post-filtering an unbounded result set, so a notebook-scoped ask (issue
// #425) never has to fetch rows outside its source set to begin with. An
// empty allowedPaths returns no results rather than falling back to an
// unscoped search — an empty scope must never silently widen.
func (db *DB) SearchScoped(query string, allowedPaths []string) ([]*vault.Document, error) {
	if len(allowedPaths) == 0 {
		return nil, nil
	}
	query = ftsQuote(query)
	if query == "" {
		return nil, nil
	}

	placeholders := make([]string, len(allowedPaths))
	args := make([]interface{}, 0, len(allowedPaths)+1)
	args = append(args, query)
	for i, p := range allowedPaths {
		placeholders[i] = "?"
		args = append(args, p)
	}

	rows, err := db.conn.Query(fmt.Sprintf(`
		SELECT f.path, f.title, `+ftsSnippetExpr+` as snippet
		FROM fts_search s
		JOIN files f ON f.id = s.rowid
		WHERE fts_search MATCH ? AND f.path IN (%s)
		ORDER BY rank LIMIT 20
	`, strings.Join(placeholders, ",")), args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var docs []*vault.Document
	for rows.Next() {
		var d vault.Document
		var snippet string
		if err := rows.Scan(&d.Path, &d.Title, &snippet); err != nil {
			return nil, err
		}
		d.Body = snippet
		docs = append(docs, &d)
	}
	return docs, nil
}

// SearchMatch is a sidecar search result with the raw indexed body retained for
// the query language's regex post-filter. Body is never exposed directly by the
// service; callers receive the existing snippet-only public shape.
type SearchMatch struct {
	Path    string
	Title   string
	Snippet string
	Body    string
	Tags    string
}

// SearchPlan applies a parsed search plan. Metadata and FTS filters run in
// SQLite; regex and the legacy, unnormalised tags value are applied after the
// database has narrowed the candidate set. The latter avoids a schema migration
// while preserving exact tag matching for existing sidecars.
func (db *DB) SearchPlan(plan searchquery.Plan) ([]SearchMatch, error) {
	positiveTerms := make([]searchquery.Term, 0, len(plan.Terms))
	negativeTerms := make([]searchquery.Term, 0, len(plan.Terms))
	for _, term := range plan.Terms {
		if term.Negated {
			negativeTerms = append(negativeTerms, term)
		} else {
			positiveTerms = append(positiveTerms, term)
		}
	}

	hasFullText := len(positiveTerms) > 0
	var query strings.Builder
	query.WriteString(`SELECT f.path, f.title, `)
	if hasFullText {
		query.WriteString(ftsSnippetExpr + `, `)
	} else {
		query.WriteString(`substr(COALESCE(fts_search.body, ''), 1, 256), `)
	}
	query.WriteString(`COALESCE(fts_search.body, ''),
		COALESCE((SELECT value FROM file_properties tag_prop
			WHERE tag_prop.file_id = f.id AND tag_prop.key = 'tags'), '')
		FROM files f `)
	if hasFullText {
		query.WriteString(`JOIN fts_search ON fts_search.rowid = f.id WHERE fts_search MATCH ?`)
	} else {
		query.WriteString(`LEFT JOIN fts_search ON fts_search.rowid = f.id WHERE 1 = 1`)
	}

	args := make([]interface{}, 0, len(plan.Filters)+len(plan.Terms)+1)
	if hasFullText {
		args = append(args, ftsExpression(positiveTerms))
	}

	for _, filter := range plan.Filters {
		switch filter.Field {
		case searchquery.FieldPath:
			operator := "LIKE"
			if filter.Negated {
				operator = "NOT LIKE"
			}
			query.WriteString(` AND LOWER(f.path) ` + operator + ` LOWER(?) ESCAPE '\'`)
			args = append(args, "%"+escapeLike(filter.Value)+"%")
		case searchquery.FieldStatus:
			operator := "="
			if filter.Negated {
				operator = "!="
			}
			query.WriteString(` AND COALESCE(f.status, '') ` + operator + ` ? COLLATE NOCASE`)
			args = append(args, filter.Value)
		case searchquery.FieldType:
			operator := "EXISTS"
			if filter.Negated {
				operator = "NOT EXISTS"
			}
			query.WriteString(` AND ` + operator + ` (
				SELECT 1 FROM file_properties type_prop
				WHERE type_prop.file_id = f.id
					AND type_prop.key = 'document_type'
					AND type_prop.value = ? COLLATE NOCASE
			)`)
			args = append(args, filter.Value)
		case searchquery.FieldTag:
			// Tags are exact-matched after scanning. Existing indexes store the
			// original YAML list as one property, rather than a separate tag table.
		}
	}

	for _, term := range negativeTerms {
		query.WriteString(` AND NOT EXISTS (
			SELECT 1 FROM fts_search
			WHERE fts_search.rowid = f.id AND fts_search MATCH ?
		)`)
		args = append(args, ftsExpression([]searchquery.Term{term}))
	}

	if hasFullText {
		query.WriteString(` ORDER BY rank`)
	} else {
		query.WriteString(` ORDER BY f.path COLLATE NOCASE`)
	}

	rows, err := db.conn.Query(query.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := make([]SearchMatch, 0)
	for rows.Next() {
		var match SearchMatch
		if err := rows.Scan(&match.Path, &match.Title, &match.Snippet, &match.Body, &match.Tags); err != nil {
			return nil, err
		}
		if !matchesPlanPostFilters(match, plan) {
			continue
		}
		results = append(results, match)
		if len(results) == 20 {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

func ftsExpression(terms []searchquery.Term) string {
	parts := make([]string, 0, len(terms))
	for _, term := range terms {
		quoted := `"` + strings.ReplaceAll(term.Value, `"`, `""`) + `"`
		if !term.Phrase {
			quoted += "*"
		}
		parts = append(parts, quoted)
	}
	return strings.Join(parts, " ")
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "%", `\%`)
	return strings.ReplaceAll(value, "_", `\_`)
}

func matchesPlanPostFilters(match SearchMatch, plan searchquery.Plan) bool {
	for _, filter := range plan.Filters {
		if filter.Field != searchquery.FieldTag {
			continue
		}
		matched := hasTag(match.Tags, filter.Value)
		if filter.Negated == matched {
			return false
		}
	}

	content := match.Title + "\n" + match.Body
	for _, regex := range plan.Regexes {
		matched := regex.Matches(content)
		if regex.Negated == matched {
			return false
		}
	}
	return true
}

func hasTag(raw, want string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	if strings.EqualFold(raw, want) {
		return true
	}

	for _, tag := range parseTagsValue(raw) {
		if strings.EqualFold(tag, want) {
			return true
		}
	}
	return false
}

// parseTagsValue splits a stored `tags` property into individual tags. The
// value is the original frontmatter list (e.g. `[invoice, urgent]`,
// `[invoice urgent]` or a bare comma-separated string) and is stored verbatim,
// so both list and comma forms are handled here.
func parseTagsValue(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	trimmed := strings.TrimPrefix(strings.TrimSuffix(raw, "]"), "[")
	if strings.TrimSpace(trimmed) == "" {
		return nil
	}
	var tags []string
	for _, tag := range strings.FieldsFunc(trimmed, func(r rune) bool {
		return r == ',' || unicode.IsSpace(r)
	}) {
		cleaned := strings.Trim(tag, `"'`)
		if cleaned != "" {
			tags = append(tags, cleaned)
		}
	}
	return tags
}

// GetTitle returns the title of the document at the given path.
func (db *DB) GetTitle(path string) (string, error) {
	var title string
	err := db.conn.QueryRow("SELECT title FROM files WHERE path = ?", path).Scan(&title)
	if err != nil {
		return "", err
	}
	return title, nil
}

// ListFiles returns all files, optionally filtered by a directory prefix.
func (db *DB) ListFiles(dirPrefix string) ([]*vault.Document, error) {
	query := `SELECT path, title, modified_at, "type" FROM files`
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
		var fileType sql.NullString
		if err := rows.Scan(&d.Path, &d.Title, &d.Created, &fileType); err != nil {
			return nil, err
		}
		d.Type = fileType.String
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

// DocsFilter holds optional filters for DocsList.
type DocsFilter struct {
	Type          string // document_type frontmatter value (e.g. "invoice")
	FileType      string // file kind: note|document|meeting|notebook
	Status        string // enum status
	Person        string // household member
	Correspondent string // correspondent name
	Year          string // 4-digit year, filters document_date
	DueBefore     string // ISO-8601 date, due_date <= this
	MinConfidence *int   // confidence >= this
	MaxConfidence *int   // confidence <= this
	ASN           *int   // exact archive serial number
}

// DocsResult is a single row returned by DocsList.
type DocsResult struct {
	Path          string `json:"path"`
	Title         string `json:"title"`
	Type          string `json:"type,omitempty"`
	DocumentDate  string `json:"document_date,omitempty"`
	Person        string `json:"person,omitempty"`
	Status        string `json:"status,omitempty"`
	DueDate       string `json:"due_date,omitempty"`
	Confidence    int    `json:"confidence,omitempty"`
	Correspondent string `json:"correspondent,omitempty"`
	DocumentType  string `json:"document_type,omitempty"`
	ASN           int    `json:"asn,omitempty"`
	// Tags is the parsed tag list from the file's tags frontmatter property.
	// It is empty when the file carries no tags.
	Tags []string `json:"tags,omitempty"`
}

// DocsList queries indexed documents with optional filters and returns
// structured metadata rows. Existing ListFiles is left untouched.
func (db *DB) DocsList(f DocsFilter) ([]DocsResult, error) {
	query := `
		SELECT f.path, f.title, f."type", COALESCE(f.document_date,''), COALESCE(f.person,''),
			COALESCE(f.status,''), COALESCE(f.due_date,''), f.confidence,
			COALESCE(fp_corr.value,''), COALESCE(fp_type.value,''), COALESCE(f.asn, 0),
			COALESCE((SELECT value FROM file_properties tag_prop
				WHERE tag_prop.file_id = f.id AND tag_prop.key = 'tags'), '')
		FROM files f
		LEFT JOIN file_properties fp_corr ON fp_corr.file_id = f.id AND fp_corr.key = 'correspondent'
		LEFT JOIN file_properties fp_type ON fp_type.file_id = f.id AND fp_type.key = 'document_type'
		WHERE 1=1`
	var args []interface{}

	if f.FileType != "" {
		query += ` AND f."type" = ?`
		args = append(args, f.FileType)
	}
	if f.Status != "" {
		query += ` AND f.status = ?`
		args = append(args, f.Status)
	}
	if f.Person != "" {
		query += ` AND f.person = ?`
		args = append(args, f.Person)
	}
	if f.Correspondent != "" {
		query += ` AND fp_corr.value = ?`
		args = append(args, f.Correspondent)
	}
	if f.Type != "" {
		query += ` AND fp_type.value = ?`
		args = append(args, f.Type)
	}
	if f.Year != "" {
		query += ` AND f.document_date LIKE ?`
		args = append(args, f.Year+"%")
	}
	if f.DueBefore != "" {
		query += ` AND f.due_date != '' AND f.due_date <= ?`
		args = append(args, f.DueBefore)
	}
	if f.MinConfidence != nil {
		query += ` AND f.confidence >= ?`
		args = append(args, *f.MinConfidence)
	}
	if f.MaxConfidence != nil {
		query += ` AND f.confidence <= ?`
		args = append(args, *f.MaxConfidence)
	}
	if f.ASN != nil {
		query += ` AND f.asn = ?`
		args = append(args, *f.ASN)
	}

	query += ` ORDER BY f.path ASC`

	rows, err := db.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []DocsResult
	for rows.Next() {
		var r DocsResult
		var conf sql.NullInt64
		var rawTags string
		if err := rows.Scan(&r.Path, &r.Title, &r.Type, &r.DocumentDate, &r.Person,
			&r.Status, &r.DueDate, &conf, &r.Correspondent, &r.DocumentType, &r.ASN, &rawTags); err != nil {
			return nil, err
		}
		if conf.Valid {
			r.Confidence = int(conf.Int64)
		}
		r.Tags = parseTagsValue(rawTags)
		results = append(results, r)
	}
	return results, nil
}

// DocsCounts returns aggregate counts for the given filter set.
type DocsCounts struct {
	Total  int            `json:"total"`
	Status map[string]int `json:"status,omitempty"`
	Person map[string]int `json:"person,omitempty"`
}

// DocsCounts returns counts grouped by status and person for the filtered set.
func (db *DB) DocsCounts(f DocsFilter) (*DocsCounts, error) {
	docs, err := db.DocsList(f)
	if err != nil {
		return nil, err
	}
	counts := &DocsCounts{
		Total:  len(docs),
		Status: make(map[string]int),
		Person: make(map[string]int),
	}
	for _, d := range docs {
		s := d.Status
		if s == "" {
			s = "unset"
		}
		counts.Status[s]++
		p := d.Person
		if p == "" {
			p = "unset"
		}
		counts.Person[p]++
	}
	return counts, nil
}

type ReviewResult struct {
	Path         string   `json:"path"`
	Title        string   `json:"title"`
	Status       string   `json:"status,omitempty"`
	DocumentType string   `json:"document_type,omitempty"`
	DocumentDate string   `json:"document_date,omitempty"`
	Confidence   int      `json:"confidence"`
	Reasons      []string `json:"reasons"`
}

// ReviewQueue returns documents that need human review: confidence below the
// given threshold, or missing document_type / document_date. Documents the
// user has explicitly dismissed as "not a document" (frontmatter
// review_ignored: true, set via the Review Lane ignore action) are excluded
// even if they would otherwise match, so a dismissal persists across refreshes.
func (db *DB) ReviewQueue(threshold int) ([]ReviewResult, error) {
	query := `
		SELECT f.path, f.title, COALESCE(f.status,''),
			COALESCE(fp_type.value,''), COALESCE(f.document_date,''), f.confidence
		FROM files f
		LEFT JOIN file_properties fp_type ON fp_type.file_id = f.id AND fp_type.key = 'document_type'
		LEFT JOIN file_properties fp_ignored ON fp_ignored.file_id = f.id AND fp_ignored.key = 'review_ignored'
		WHERE (f.confidence < ?
		   OR fp_type.value IS NULL OR fp_type.value = ''
		   OR f.document_date IS NULL OR f.document_date = '')
		   AND (fp_ignored.value IS NULL OR fp_ignored.value != 'true')
		ORDER BY f.path ASC`
	rows, err := db.conn.Query(query, threshold)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []ReviewResult
	for rows.Next() {
		var r ReviewResult
		var conf sql.NullInt64
		if err := rows.Scan(&r.Path, &r.Title, &r.Status,
			&r.DocumentType, &r.DocumentDate, &conf); err != nil {
			return nil, err
		}
		if conf.Valid {
			r.Confidence = int(conf.Int64)
		}
		r.Reasons = reviewReasons(r.Confidence, threshold, r.DocumentType, r.DocumentDate)
		results = append(results, r)
	}
	return results, nil
}

func reviewReasons(conf, threshold int, docType, docDate string) []string {
	var reasons []string
	if conf < threshold {
		reasons = append(reasons, fmt.Sprintf("confidence %d < %d", conf, threshold))
	}
	if docType == "" {
		reasons = append(reasons, "missing document_type")
	}
	if docDate == "" {
		reasons = append(reasons, "missing document_date")
	}
	return reasons
}

// SimilarResult describes one document compared against a reference simhash.
type SimilarResult struct {
	Path       string `json:"path"`
	Title      string `json:"title"`
	Similarity int    `json:"similarity"`
	Simhash    string `json:"simhash"`
}

// AllSimhashes returns every indexed document that has a simhash, in path
// order. Used for vault-wide duplicate scans.
func (db *DB) AllSimhashes() ([]SimilarResult, error) {
	rows, err := db.conn.Query(`
		SELECT path, title, COALESCE(simhash,'')
		FROM files
		WHERE simhash IS NOT NULL AND simhash != ''
		ORDER BY path ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // result rows are fully drained below before return

	var results []SimilarResult
	for rows.Next() {
		var r SimilarResult
		if err := rows.Scan(&r.Path, &r.Title, &r.Simhash); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, nil
}

// SimilarDocs returns indexed documents whose simhash is within the given
// hamming-distance threshold of the reference document's simhash.
// similarityThreshold is 0-100 user-facing percentage.
func (db *DB) SimilarDocs(refSimhash string, similarityThreshold int) ([]SimilarResult, error) {
	if refSimhash == "" {
		return nil, nil
	}
	refHash, err := simhash.ParseHex(refSimhash)
	if err != nil {
		return nil, fmt.Errorf("invalid reference simhash: %w", err)
	}

	rows, err := db.conn.Query(`
		SELECT path, title, COALESCE(simhash,'')
		FROM files
		WHERE simhash IS NOT NULL AND simhash != ''
		ORDER BY path ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SimilarResult
	for rows.Next() {
		var r SimilarResult
		if err := rows.Scan(&r.Path, &r.Title, &r.Simhash); err != nil {
			return nil, err
		}
		if r.Simhash == refSimhash {
			continue
		}
		otherHash, err := simhash.ParseHex(r.Simhash)
		if err != nil {
			continue
		}
		sim := simhash.Similarity(refHash, otherHash)
		if sim >= similarityThreshold {
			r.Similarity = sim
			results = append(results, r)
		}
	}
	return results, nil
}

func nullStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(n int) interface{} {
	if n == 0 {
		return nil
	}
	return n
}

func nullASN(asn *int) interface{} {
	if asn == nil {
		return nil
	}
	return *asn
}

// nullModTime stores a file's modification time as nanoseconds since the
// Unix epoch, or NULL when it is unknown (zero value) — e.g. a Document
// built from already-read bytes (vault.ParseBytes) rather than a stat'd
// path. NULL tells RefreshIndex's stat-based fast path that this row cannot
// be trusted for a skip decision.
func nullModTime(t time.Time) interface{} {
	if t.IsZero() {
		return nil
	}
	return t.UnixNano()
}
