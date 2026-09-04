package sidecar

// DatasetRowCount returns the number of derived rows without materializing them.
func (db *DB) DatasetRowCount(datasetSlug string) (int, error) {
	var count int
	if err := db.conn.QueryRow("SELECT COUNT(*) FROM dataset_rows WHERE dataset_slug = ?", datasetSlug).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}
