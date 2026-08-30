package db

import (
	"github.com/danieljustus/symaira-desktop/internal/searchquery"
)

// backfillContentNorm populates the normalised German token form for chunks
// that predate migration 0012. Stemming lives in Go, so the backfill cannot
// be expressed in the SQL migration itself; the content_norm update trigger
// keeps chunks_norm in step as rows are filled. The loop is a no-op once
// every chunk carries its normalised form.
func (db *DB) backfillContentNorm() error {
	var missing int
	if err := db.conn.QueryRow("SELECT COUNT(*) FROM chunks WHERE content_norm IS NULL").Scan(&missing); err != nil {
		return err
	}
	if missing == 0 {
		return nil
	}

	rows, err := db.conn.Query("SELECT id, content FROM chunks WHERE content_norm IS NULL")
	if err != nil {
		return err
	}
	type normRow struct {
		id   int64
		norm string
	}
	var pending []normRow
	for rows.Next() {
		var id int64
		var content string
		if err := rows.Scan(&id, &content); err != nil {
			_ = rows.Close()
			return err
		}
		pending = append(pending, normRow{id: id, norm: searchquery.GermanNormText(content)})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, row := range pending {
		if _, err := tx.Exec("UPDATE chunks SET content_norm = ? WHERE id = ?", row.norm, row.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
