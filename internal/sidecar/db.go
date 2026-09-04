package sidecar

import (
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

	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/contacts"
	"github.com/danieljustus/symaira-desktop/internal/searchquery"
	"github.com/danieljustus/symaira-desktop/internal/simhash"
	"github.com/danieljustus/symaira-desktop/internal/vault"
	"gopkg.in/yaml.v3"
)

// VaultDir returns the canonical per-vault directory used for rebuildable
// sidecar state. Retrieval and other derived stores use this resolver so all
// per-vault databases remain colocated without duplicating the hash scheme.
func VaultDir(vaultRoot string) (string, error) {
	return config.SidecarVaultDir(vaultRoot)
}

// OpenForVault keeps rebuildable indexes isolated per vault. This prevents a
// configured vault from listing or searching rows indexed for another vault.
func OpenForVault(vaultRoot string) (*DB, error) {
	if explicit := os.Getenv("SYMDESK_SIDECAR"); explicit != "" {
		return Open(explicit)
	}
	dir, err := VaultDir(vaultRoot)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "sidecar.db")
	db, err := Open(path)
	if err != nil {
		return nil, err
	}
	canonical, canonicalErr := filepath.Abs(vaultRoot)
	if canonicalErr != nil {
		_ = db.Close()
		return nil, fmt.Errorf("resolve vault for sidecar metadata: %w", canonicalErr)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(canonical); resolveErr == nil {
		canonical = resolved
	}
	if err := recordSidecarMetadata(filepath.Dir(path), canonical); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

//go:embed migrations/*.sql
var migrationsFS embed.FS

// DB represents a connection to the sidecar database.
type DB struct {
	conn *sql.DB

	// indexBatchCommits counts how many transactions IndexDocuments has
	// committed. It exists purely so tests can assert that a large batch is
	// committed in chunks rather than once per document (#760); production
	// code never reads it.
	indexBatchCommits int
}

// Open opens the sidecar database at the specified path and runs migrations.
// The default path is ~/.local/share/symdesk/sidecar.db
func Open(path string) (*DB, error) {
	if path == "" {
		var err error
		path, err = config.SidecarPath("")
		if err != nil {
			return nil, err
		}
	}

	conn, err := sqlitekit.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	if err := sqlitekit.Migrate(conn, migrationsFS); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	if err := backfillNormIndex(conn); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("backfill norm index: %w", err)
	}

	return &DB{conn: conn}, nil
}

// backfillNormIndex populates the normalised German token index for rows
// that predate migration 009. Stemming lives in Go, so the backfill cannot
// be expressed in the SQL migration itself; it runs once, inside a single
// transaction, and is a no-op once the two indexes hold the same rows.
func backfillNormIndex(conn *sql.DB) error {
	var missing int
	if err := conn.QueryRow("SELECT COUNT(*) FROM fts_search WHERE rowid NOT IN (SELECT rowid FROM fts_norm)").Scan(&missing); err != nil {
		return err
	}
	if missing == 0 {
		return nil
	}

	rows, err := conn.Query("SELECT rowid, title, body FROM fts_search WHERE rowid NOT IN (SELECT rowid FROM fts_norm)")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	tx, err := conn.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	type normRow struct {
		id   int64
		norm string
	}
	var pending []normRow
	for rows.Next() {
		var id int64
		var title, body string
		if err := rows.Scan(&id, &title, &body); err != nil {
			return err
		}
		pending = append(pending, normRow{id: id, norm: searchquery.GermanNormText(title + " " + body)})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, row := range pending {
		if _, err := tx.Exec("INSERT INTO fts_norm(rowid, norm) VALUES (?, ?)", row.id, row.norm); err != nil {
			return err
		}
	}
	return tx.Commit()
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

// maxIndexBatchSize caps how many documents share one transaction in
// IndexDocuments. It bounds how much work (and how long a write lock is
// held) a single failing or slow batch can accumulate before committing
// (#760).
const maxIndexBatchSize = 200

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
//
// Files that need indexing are queued and indexed via IndexDocuments so a
// full or initial refresh commits in batches instead of once per file
// (#760).
func (db *DB) RefreshIndex(vaultRoot string) error {
	batch := make([]*vault.Document, 0, maxIndexBatchSize)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		// Detach and reset the batch before indexing so a failed flush is
		// never retried with the same documents by a later flush call (the
		// deferred one after the walk always runs, success or not).
		docs := batch
		batch = make([]*vault.Document, 0, maxIndexBatchSize)
		return db.IndexDocuments(docs)
	}

	walkErr := vault.Walk(vaultRoot, func(path string) error {
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
		if doc.IsDerived() {
			return db.DeleteDocument(doc.Path)
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

		batch = append(batch, doc)
		if len(batch) >= maxIndexBatchSize {
			return flush()
		}
		return nil
	})
	// Flush whatever accumulated even if the walk itself failed partway
	// through, so a mid-vault error does not discard documents already
	// parsed and queued in this run.
	if err := flush(); err != nil {
		return err
	}
	return walkErr
}

// IndexDocument indexes a single document into the sidecar, in its own
// transaction. Prefer IndexDocuments when indexing more than one document —
// e.g. an initial or full-rebuild pass over a vault — so the documents share
// transactions instead of committing one at a time (#760).
func (db *DB) IndexDocument(doc *vault.Document) error {
	if doc.IsDerived() {
		return db.DeleteDocument(doc.Path)
	}

	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := indexDocumentTx(tx, doc); err != nil {
		return err
	}
	return tx.Commit()
}

// IndexDocuments indexes a batch of documents, committing every
// maxIndexBatchSize documents (200) in a single transaction instead of once
// per document. This is what lets the initial index of a large vault, and
// every full rebuild, commit in batches rather than one commit per file
// (#760).
//
// If a document in the middle of a batch fails (e.g. an invalid ASN), the
// documents already applied earlier in that same batch are committed before
// the error is returned, so a mid-batch failure does not lose progress
// already made.
func (db *DB) IndexDocuments(docs []*vault.Document) error {
	for len(docs) > 0 {
		n := len(docs)
		if n > maxIndexBatchSize {
			n = maxIndexBatchSize
		}
		if err := db.indexDocumentBatch(docs[:n]); err != nil {
			return err
		}
		docs = docs[n:]
	}
	return nil
}

// indexDocumentBatch indexes up to maxIndexBatchSize documents in a single
// transaction.
func (db *DB) indexDocumentBatch(docs []*vault.Document) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for _, doc := range docs {
		if doc.IsDerived() {
			if err := deleteDocumentRows(tx, doc.Path); err != nil {
				return fmt.Errorf("delete derived document %s: %w", doc.Path, err)
			}
			continue
		}
		if err := indexDocumentTx(tx, doc); err != nil {
			// Commit the documents already applied earlier in this batch
			// rather than rolling the whole batch back, so a single bad
			// document does not undo work already done on its neighbors.
			if commitErr := tx.Commit(); commitErr != nil {
				return commitErr
			}
			return fmt.Errorf("index %s: %w", doc.Path, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	db.indexBatchCommits++
	return nil
}

// indexDocumentTx performs the actual insert/update/FTS work for doc within
// an already-open transaction. It is shared by IndexDocument (one document,
// one transaction) and indexDocumentBatch (many documents, one transaction).
func indexDocumentTx(tx *sql.Tx, doc *vault.Document) error {
	if doc.ASN != nil {
		if err := vault.ValidateASN(*doc.ASN); err != nil {
			return fmt.Errorf("invalid document ASN: %w", err)
		}
	}
	if doc.Simhash == "" && doc.Body != "" {
		doc.Simhash = simhash.ComputeHex(doc.Body)
	}

	// 1. Check if exists to potentially delete from FTS first
	var fileID int64
	err := tx.QueryRow("SELECT id FROM files WHERE path = ?", doc.Path).Scan(&fileID)
	if err != nil && err != sql.ErrNoRows {
		return err
	}

	if err == nil {
		// Found existing, remove from FTS first
		_, err = tx.Exec("DELETE FROM fts_search WHERE rowid = ?", fileID)
		if err != nil {
			return err
		}
		if _, err = tx.Exec("DELETE FROM fts_norm WHERE rowid = ?", fileID); err != nil {
			return err
		}
		if _, err = tx.Exec("DELETE FROM fts_tri WHERE rowid = ?", fileID); err != nil {
			return err
		}

		// Update files
		_, err = tx.Exec(`UPDATE files SET sha256 = ?, title = ?, created_at = ?, modified_at = ?, indexed_at = ?,
			"type" = ?, document_date = ?, person = ?, status = ?, due_date = ?, confidence = ?, ocr_json_path = ?, simhash = ?, asn = ?,
			size = ?, mtime_ns = ?
			WHERE id = ?`,
			doc.SHA256, doc.Title, doc.Created, documentModifiedAt(doc), time.Now(),
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
		res, err := tx.Exec(`INSERT INTO files(path, sha256, title, created_at, modified_at, indexed_at,
			"type", document_date, person, status, due_date, confidence, ocr_json_path, simhash, asn, size, mtime_ns)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			doc.Path, doc.SHA256, doc.Title, doc.Created, documentModifiedAt(doc), time.Now(),
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
	// Keep the normalised German token index in step so inflected and
	// umlaut-bearing queries match (#672).
	if _, err = tx.Exec("INSERT INTO fts_norm(rowid, norm) VALUES (?, ?)", fileID, searchquery.GermanNormText(ftsTitle+" "+doc.Body)); err != nil {
		return err
	}
	// Keep the trigram substring index in step so compound parts are
	// findable anywhere inside a token (#673).
	if _, err = tx.Exec("INSERT INTO fts_tri(rowid, body) VALUES (?, ?)", fileID, doc.Body); err != nil {
		return err
	}

	// 3. Insert file properties
	for k, v := range doc.Frontmatter {
		if k == "tags" || k == "aliases" {
			continue // Handled below to ensure doc.Tags and doc.Aliases are indexed
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
	if len(doc.Aliases) > 0 {
		b, _ := yaml.Marshal(doc.Aliases)
		valStr := strings.TrimSpace(string(b))
		_, err = tx.Exec(`INSERT INTO file_properties(file_id, key, value, value_type) VALUES (?, ?, ?, ?)`,
			fileID, "aliases", valStr, "string")
		if err != nil {
			return err
		}
	} else if _, ok := doc.Frontmatter["aliases"]; ok {
		valStr := fmt.Sprintf("%v", doc.Frontmatter["aliases"])
		_, err = tx.Exec(`INSERT INTO file_properties(file_id, key, value, value_type) VALUES (?, ?, ?, ?)`,
			fileID, "aliases", valStr, "string")
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

	// Contact references are derived graph edges. The target is an opaque
	// contact-store token, never contact data, so backlinks can show which
	// notes mention a contact without making the vault authoritative for it.
	for _, ref := range contacts.ReferencesInFrontmatter(doc.Frontmatter) {
		_, err = tx.Exec(`INSERT OR IGNORE INTO links(from_path, to_path, kind) VALUES (?, ?, 'contact_ref')`,
			doc.Path, contacts.ReferenceTarget(ref))
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

	return nil
}

// DeleteDocument removes a document and all derived rows (FTS, properties,
// outgoing links) from the sidecar. Deleting an unindexed path is a no-op.
func (db *DB) DeleteDocument(path string) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

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
	if _, err := tx.Exec("DELETE FROM fts_norm WHERE rowid = ?", fileID); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM fts_tri WHERE rowid = ?", fileID); err != nil {
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
		doc, err := vault.ParseFile(path)
		if err == nil && doc.IsDerived() {
			return nil
		}
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

	if len(stale) == 0 {
		return db.PruneIndexStatuses(vaultRoot)
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
	cleaned, err := db.PruneIndexStatuses(vaultRoot)
	if err != nil {
		return len(stale), err
	}

	return len(stale) + cleaned, nil
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
	return searchquery.GermanFTSQuery(query)
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

// ftsMatchJoin is the shared driving subquery for full-text search: the
// union of original-text matches (with rank and snippet) and normalised
// German token matches (#672), deduplicated per document. The MATCH
// constraints live inside the subquery legs because FTS5 does not allow
// MATCH on a LEFT JOINed table; the outer query joins the materialised
// match set and can filter and order freely. Norm-only matches carry NULL
// rank and sort after ranked matches via "sm.rank IS NULL, sm.rank".
const ftsMatchJoin = ` JOIN (
	SELECT rowid, MAX(rank) AS rank, MAX(snip) AS snip, MAX(body) AS body FROM (
		SELECT rowid, rank, ` + ftsSnippetExpr + ` AS snip, body FROM fts_search WHERE fts_search MATCH ?
		UNION ALL
		SELECT rowid, NULL, NULL, NULL FROM fts_norm WHERE fts_norm MATCH ?
		UNION ALL
		SELECT rowid, NULL, NULL, NULL FROM fts_tri WHERE fts_tri MATCH ?
	) GROUP BY rowid
) sm ON sm.rowid = f.id`

// Search performs a basic FTS search over free-form user input.
func (db *DB) Search(query string) ([]*vault.Document, error) {
	triQuery := searchquery.GermanTrigramQuery(query)
	query = ftsQuote(query)
	if query == "" {
		return nil, nil
	}
	rows, err := db.conn.Query(`
		SELECT f.path, f.title, COALESCE(sm.snip, '') as snippet
		FROM files f`+ftsMatchJoin+`
		ORDER BY sm.rank IS NULL, sm.rank LIMIT 20
	`, query, query, triQuery)
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
	triQuery := searchquery.GermanTrigramQuery(query)
	query = ftsQuote(query)
	if query == "" {
		return nil, nil
	}

	placeholders := make([]string, len(allowedPaths))
	args := make([]interface{}, 0, len(allowedPaths)+3)
	args = append(args, query, query, triQuery)
	for i, p := range allowedPaths {
		placeholders[i] = "?"
		args = append(args, p)
	}

	rows, err := db.conn.Query(fmt.Sprintf(`
		SELECT f.path, f.title, COALESCE(sm.snip, '') as snippet
		FROM files f`+ftsMatchJoin+`
		WHERE f.path IN (%s)
		ORDER BY sm.rank IS NULL, sm.rank LIMIT 20
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
	Path        string
	Title       string
	Snippet     string
	Body        string
	Tags        string
	Status      string
	Type        string
	CreatedAt   string
	ModifiedAt  string
	IndexState  string
	IndexReason string
}

// SearchPlan applies a parsed search plan. Metadata and FTS filters run in
// SQLite; regex and the legacy, unnormalised tags value are applied after the
// database has narrowed the candidate set. The latter avoids a schema migration
// while preserving exact tag matching for existing sidecars.
var searchClock = time.Now

// SearchPlan evaluates a plan using the current clock. SearchPlanAt is exposed
// for deterministic callers and tests that need a fixed reference instant.
func (db *DB) SearchPlan(plan searchquery.Plan) ([]SearchMatch, error) {
	return db.SearchPlanAt(plan, searchClock())
}

func (db *DB) SearchPlanAt(plan searchquery.Plan, reference time.Time) ([]SearchMatch, error) {
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
	ftsQuery := ""
	triQuery := ""
	if hasFullText {
		ftsQuery = ftsExpression(positiveTerms)
		hasFullText = ftsQuery != ""
		triQuery = triExpression(positiveTerms)
	}
	var query strings.Builder
	query.WriteString(`SELECT f.path, f.title, `)
	if hasFullText {
		query.WriteString(`COALESCE(sm.snip, ''), `)
	} else {
		query.WriteString(`substr(COALESCE(fts_search.body, ''), 1, 256), `)
	}
	if hasFullText {
		query.WriteString(`COALESCE(sm.body, ''),`)
	} else {
		query.WriteString(`COALESCE(fts_search.body, ''),`)
	}
	query.WriteString(`
		COALESCE((SELECT value FROM file_properties tag_prop
			WHERE tag_prop.file_id = f.id AND tag_prop.key = 'tags'), ''),
		COALESCE(f.status, ''), COALESCE(f."type", ''),
		COALESCE(f.created_at, (SELECT value FROM file_properties created_prop
			WHERE created_prop.file_id = f.id AND created_prop.key = 'created'), ''),
		COALESCE(f.modified_at, ''),
		COALESCE(lifecycle.state, 'indexed'), COALESCE(lifecycle.reason, '')
		FROM files f LEFT JOIN index_lifecycle lifecycle ON lifecycle.path = f.path `)
	if hasFullText {
		query.WriteString(ftsMatchJoin + ` WHERE 1 = 1`)
	} else {
		query.WriteString(`LEFT JOIN fts_search ON fts_search.rowid = f.id WHERE 1 = 1`)
	}

	args := make([]interface{}, 0, len(plan.Filters)+2*len(plan.Terms)+3)
	if hasFullText {
		args = append(args, ftsQuery, ftsQuery, triQuery)
	}

	for _, filter := range plan.Filters {
		switch filter.Field {
		case searchquery.FieldPath:
			if strings.Contains(filter.Value, ",") {
				continue
			}
			operator := "LIKE"
			if filter.Negated {
				operator = "NOT LIKE"
			}
			query.WriteString(` AND LOWER(f.path) ` + operator + ` LOWER(?) ESCAPE '\'`)
			args = append(args, "%"+escapeLike(filter.Value)+"%")
		case searchquery.FieldStatus:
			if strings.Contains(filter.Value, ",") {
				continue
			}
			operator := "="
			if filter.Negated {
				operator = "!="
			}
			query.WriteString(` AND COALESCE(f.status, '') ` + operator + ` ? COLLATE NOCASE`)
			args = append(args, filter.Value)
		case searchquery.FieldIndexState:
			operator := "="
			if filter.Negated {
				operator = "!="
			}
			query.WriteString(` AND COALESCE(lifecycle.state, 'indexed') ` + operator + ` ? COLLATE NOCASE`)
			args = append(args, filter.Value)
		case searchquery.FieldType:
			if strings.Contains(filter.Value, ",") {
				continue
			}
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
		expression := ftsExpression([]searchquery.Term{term})
		if expression == "" {
			continue
		}
		query.WriteString(` AND NOT EXISTS (
			SELECT 1 FROM fts_search
			WHERE fts_search.rowid = f.id AND fts_search MATCH ?
		) AND NOT EXISTS (
			SELECT 1 FROM fts_norm
			WHERE fts_norm.rowid = f.id AND fts_norm MATCH ?
		)`)
		args = append(args, expression, expression)
	}

	if hasFullText {
		query.WriteString(` ORDER BY sm.rank IS NULL, sm.rank`)
	} else {
		query.WriteString(` ORDER BY f.path COLLATE NOCASE`)
	}

	rows, err := db.conn.Query(query.String(), args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	results := make([]SearchMatch, 0)
	for rows.Next() {
		var match SearchMatch
		if err := rows.Scan(&match.Path, &match.Title, &match.Snippet, &match.Body, &match.Tags, &match.Status, &match.Type, &match.CreatedAt, &match.ModifiedAt, &match.IndexState, &match.IndexReason); err != nil {
			return nil, err
		}
		if !matchesPlanPostFiltersAt(match, plan, reference) {
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

// triExpression builds the trigram-leg query for the positive terms (#673):
// every term must be expressible against the trigram index, otherwise the
// leg is skipped entirely ("") and only prefix/norm matching applies —
// trigram tokens shorter than three runes silently match nothing, which
// would void the AND semantics.
func triExpression(terms []searchquery.Term) string {
	parts := make([]string, 0, len(terms))
	for _, term := range terms {
		expression := searchquery.GermanTrigramTerm(term.Value, term.Phrase)
		if expression == `""` {
			return `""`
		}
		parts = append(parts, expression)
	}
	return strings.Join(parts, " AND ")
}

func ftsExpression(terms []searchquery.Term) string {
	parts := make([]string, 0, len(terms))
	for _, term := range terms {
		expression := searchquery.GermanFTSTerm(term.Value, term.Phrase)
		if expression != "" {
			parts = append(parts, expression)
		}
	}
	return strings.Join(parts, " AND ")
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "%", `\%`)
	return strings.ReplaceAll(value, "_", `\_`)
}

func matchesPlanPostFiltersAt(match SearchMatch, plan searchquery.Plan, reference time.Time) bool {
	for _, filter := range plan.Filters {
		if filter.Field == searchquery.FieldPath {
			if !strings.Contains(filter.Value, ",") {
				continue
			}
			matched := hasAnyValue(match.Path, filter.Value, func(raw, wanted string) bool {
				return strings.Contains(strings.ToLower(raw), strings.ToLower(wanted))
			})
			if filter.Negated == matched {
				return false
			}
			continue
		}
		if filter.Field == searchquery.FieldStatus || filter.Field == searchquery.FieldType {
			if !strings.Contains(filter.Value, ",") {
				continue
			}
			raw := match.Status
			if filter.Field == searchquery.FieldType {
				raw = match.Type
			}
			matched := hasAnyValue(raw, filter.Value, func(value, wanted string) bool {
				return strings.EqualFold(value, wanted)
			})
			if filter.Negated == matched {
				return false
			}
			continue
		}
		if filter.Field == searchquery.FieldCreated || filter.Field == searchquery.FieldModified {
			value := match.CreatedAt
			if filter.Field == searchquery.FieldModified {
				value = match.ModifiedAt
			}
			matched := matchesDateList(value, filter.Value, reference)
			if filter.Negated == matched {
				return false
			}
			continue
		}
		if filter.Field == searchquery.FieldFilename || filter.Field == searchquery.FieldFileType {
			matched := matchesFilenameFilter(match.Path, filter)
			if filter.Negated == matched {
				return false
			}
			continue
		}
		if filter.Field != searchquery.FieldTag {
			continue
		}
		matched := hasAnyValue(match.Tags, filter.Value, hasTag)
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

// parseAliasesValue splits a stored `aliases` property into individual aliases.
func parseAliasesValue(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	if strings.HasPrefix(raw, "[") || strings.HasPrefix(raw, "-") {
		var list []string
		if err := yaml.Unmarshal([]byte(raw), &list); err == nil && len(list) > 0 {
			var res []string
			for _, s := range list {
				if trimmed := strings.TrimSpace(s); trimmed != "" {
					res = append(res, trimmed)
				}
			}
			if len(res) > 0 {
				return res
			}
		}
	}

	if strings.Contains(raw, ",") {
		trimmed := strings.TrimPrefix(strings.TrimSuffix(raw, "]"), "[")
		var res []string
		for _, part := range strings.Split(trimmed, ",") {
			cleaned := strings.Trim(strings.TrimSpace(part), `"'`)
			if cleaned != "" {
				res = append(res, cleaned)
			}
		}
		if len(res) > 0 {
			return res
		}
	}

	var single string
	if err := yaml.Unmarshal([]byte(raw), &single); err == nil {
		single = strings.TrimSpace(single)
		if single != "" && !strings.HasPrefix(single, "[") {
			return []string{single}
		}
	}

	cleaned := strings.Trim(strings.TrimPrefix(strings.TrimSuffix(raw, "]"), "["), `"'`)
	if cleaned != "" {
		return []string{cleaned}
	}
	return nil
}

// GetAllAliases returns a map from file path to its list of aliases.
func (db *DB) GetAllAliases() (map[string][]string, error) {
	rows, err := db.conn.Query(`
		SELECT f.path, fp.value
		FROM files f
		JOIN file_properties fp ON fp.file_id = f.id
		WHERE fp.key = 'aliases'
	`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	result := make(map[string][]string)
	for rows.Next() {
		var path, val string
		if err := rows.Scan(&path, &val); err != nil {
			return nil, err
		}
		aliases := parseAliasesValue(val)
		if len(aliases) > 0 {
			result[path] = aliases
		}
	}
	return result, nil
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
	defer func() { _ = rows.Close() }()

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
	defer func() { _ = rows.Close() }()

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

// GetBacklinks returns the paths of files that link to the given path, title,
// aliases, or opaque contact reference target.
func (db *DB) GetBacklinks(path string) ([]string, error) {
	targetsMap := make(map[string]struct{})
	addTarget := func(t string) {
		t = strings.TrimSpace(t)
		if t == "" {
			return
		}
		targetsMap[t] = struct{}{}
		targetsMap[strings.ToLower(t)] = struct{}{}
		if noExt := strings.TrimSuffix(t, filepath.Ext(t)); noExt != "" {
			targetsMap[noExt] = struct{}{}
			targetsMap[strings.ToLower(noExt)] = struct{}{}
		}
	}

	addTarget(path)
	baseName := filepath.Base(path)
	title := strings.TrimSuffix(baseName, filepath.Ext(baseName))
	if _, isContact := contacts.ParseReferenceTarget(path); !isContact {
		addTarget(baseName)
		addTarget(title)
	}

	var fileID int64
	var docTitle string
	err := db.conn.QueryRow("SELECT id, title FROM files WHERE path = ?", path).Scan(&fileID, &docTitle)
	if err == sql.ErrNoRows {
		err = db.conn.QueryRow("SELECT id, title FROM files WHERE title = ? COLLATE NOCASE OR path LIKE ? COLLATE NOCASE LIMIT 1", path, "%/"+baseName).Scan(&fileID, &docTitle)
	}
	if err == nil && fileID != 0 {
		if docTitle != "" {
			addTarget(docTitle)
		}
		var aliasesRaw sql.NullString
		_ = db.conn.QueryRow("SELECT value FROM file_properties WHERE file_id = ? AND key = 'aliases'", fileID).Scan(&aliasesRaw)
		if aliasesRaw.Valid {
			for _, alias := range parseAliasesValue(aliasesRaw.String) {
				addTarget(alias)
			}
		}
	}

	targets := make([]string, 0, len(targetsMap))
	for t := range targetsMap {
		targets = append(targets, t)
	}
	if len(targets) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(targets))
	args := make([]interface{}, len(targets))
	for i, t := range targets {
		placeholders[i] = "?"
		args[i] = t
	}

	query := fmt.Sprintf("SELECT DISTINCT from_path FROM links WHERE to_path IN (%s)", strings.Join(placeholders, ",")) //nolint:gosec // query constructed with parameter placeholders only
	rows, err := db.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var links []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		links = append(links, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
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
	defer func() { _ = rows.Close() }()

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
	Status        string // workflow status
	IndexState    string // retrieval lifecycle state
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
	Tags               []string `json:"tags,omitempty"`
	IndexState         string   `json:"index_state"`
	IndexFailureReason string   `json:"index_failure_reason,omitempty"`
}

// DocsList queries indexed documents with optional filters and returns
// structured metadata rows. Existing ListFiles is left untouched.
func (db *DB) DocsList(f DocsFilter) ([]DocsResult, error) {
	query := `
		SELECT f.path, f.title, f."type", COALESCE(f.document_date,''), COALESCE(f.person,''),
			COALESCE(f.status,''), COALESCE(f.due_date,''), f.confidence,
			COALESCE(fp_corr.value,''), COALESCE(fp_type.value,''), COALESCE(f.asn, 0),
			COALESCE((SELECT value FROM file_properties tag_prop
				WHERE tag_prop.file_id = f.id AND tag_prop.key = 'tags'), ''),
			COALESCE(lifecycle.state, 'indexed'), COALESCE(lifecycle.reason, '')
		FROM files f
		LEFT JOIN index_lifecycle lifecycle ON lifecycle.path = f.path
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
	if f.IndexState != "" {
		query += ` AND COALESCE(lifecycle.state, 'indexed') = ? COLLATE NOCASE`
		args = append(args, f.IndexState)
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
	defer func() { _ = rows.Close() }()

	var results []DocsResult
	for rows.Next() {
		var r DocsResult
		var conf sql.NullInt64
		var rawTags string
		var state, reason string
		if err := rows.Scan(&r.Path, &r.Title, &r.Type, &r.DocumentDate, &r.Person,
			&r.Status, &r.DueDate, &conf, &r.Correspondent, &r.DocumentType, &r.ASN, &rawTags, &state, &reason); err != nil {
			return nil, err
		}
		if conf.Valid {
			r.Confidence = int(conf.Int64)
		}
		r.Tags = parseTagsValue(rawTags)
		r.IndexState = state
		r.IndexFailureReason = reason
		results = append(results, r)
	}
	return results, nil
}

// TimelineDocs queries indexed documents with document_date or modified_at falling in [fromDate, toDate].
func (db *DB) TimelineDocs(fromDate, toDate string) ([]DocsResult, error) {
	query := `
		SELECT f.path, f.title, f."type", COALESCE(f.document_date,''), COALESCE(f.person,''),
			COALESCE(f.status,''), COALESCE(f.due_date,''), f.confidence,
			COALESCE(fp_corr.value,''), COALESCE(fp_type.value,''), COALESCE(f.asn, 0),
			COALESCE((SELECT value FROM file_properties tag_prop
				WHERE tag_prop.file_id = f.id AND tag_prop.key = 'tags'), '')
		FROM files f
		LEFT JOIN file_properties fp_corr ON fp_corr.file_id = f.id AND fp_corr.key = 'correspondent'
		LEFT JOIN file_properties fp_type ON fp_type.file_id = f.id AND fp_type.key = 'document_type'
		WHERE (f.document_date != '' AND f.document_date >= ? AND f.document_date <= ?)
		   OR (f.modified_at != '' AND f.modified_at >= ? AND f.modified_at <= ?)
		ORDER BY f.path ASC`

	rows, err := db.conn.Query(query, fromDate, toDate, fromDate, toDate+"T23:59:59Z")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

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
	defer func() { _ = rows.Close() }()

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
	Body       string `json:"-"`
}

// AllSimhashes returns every indexed document with non-empty body content, in
// path order. The body is carried internally so the duplicate grouping path
// can compute a fresh hash instead of trusting frontmatter metadata.
func (db *DB) AllSimhashes() ([]SimilarResult, error) {
	rows, err := db.conn.Query(`
		SELECT files.path, files.title, COALESCE(files.simhash,''), fts_search.body
		FROM files
		JOIN fts_search ON fts_search.rowid = files.id
		WHERE trim(COALESCE(fts_search.body,'')) != ''
		ORDER BY files.path ASC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }() //nolint:errcheck // result rows are fully drained below before return

	var results []SimilarResult
	for rows.Next() {
		var r SimilarResult
		if err := rows.Scan(&r.Path, &r.Title, &r.Simhash, &r.Body); err != nil {
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
	defer func() { _ = rows.Close() }()

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

func documentModifiedAt(doc *vault.Document) string {
	if !doc.ModTime.IsZero() {
		return doc.ModTime.UTC().Format(time.RFC3339Nano)
	}
	if doc.Created != "" {
		return doc.Created
	}
	return searchClock().UTC().Format(time.RFC3339Nano)
}

func hasAnyValue(raw, wanted string, match func(string, string) bool) bool {
	for _, value := range strings.Split(wanted, ",") {
		if value = strings.TrimSpace(value); value != "" && match(raw, value) {
			return true
		}
	}
	return false
}

func matchesFilenameFilter(path string, filter searchquery.Filter) bool {
	base := filepath.Base(filepath.ToSlash(path))
	if filter.Field == searchquery.FieldFileType {
		ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(base)), ".")
		for _, wanted := range strings.Split(filter.Value, ",") {
			if strings.EqualFold(ext, strings.TrimPrefix(strings.TrimSpace(wanted), ".")) {
				return true
			}
		}
		return false
	}
	for _, wanted := range strings.Split(filter.Value, ",") {
		if wanted = strings.TrimSpace(wanted); wanted != "" && strings.Contains(strings.ToLower(base), strings.ToLower(wanted)) {
			return true
		}
	}
	return false
}

func matchesDateList(raw, wanted string, reference time.Time) bool {
	value, err := parseIndexedTime(raw)
	if err != nil {
		return false
	}
	for _, expression := range strings.Split(wanted, ",") {
		rangeValue, err := searchquery.ParseDateValue(strings.TrimSpace(expression), reference)
		if err == nil && !value.Before(rangeValue.From) && !value.After(rangeValue.To) {
			return true
		}
	}
	return false
}

func parseIndexedTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05-07:00", "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid indexed timestamp %q", value)
}
