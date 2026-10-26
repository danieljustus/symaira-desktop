// SQL copied verbatim from Go store.go.
pub(crate) const ENQUEUE_JOB_0: &str =
    r#"INSERT INTO jobs (document_id, kind, status, attempts) VALUES (?, ?, 'pending', 0)"#;
pub(crate) const ENQUEUE_SKIPPED_JOB_0: &str = r#"INSERT INTO jobs (document_id, kind, status, attempts, last_error) VALUES (?, ?, 'skipped', 0, ?)"#;
pub(crate) const ENQUEUE_REPROCESS_JOB_0: &str = r#"
		SELECT id, document_id, kind, status, attempts, last_error, created_at, updated_at
		FROM jobs
		WHERE document_id = ? AND kind = 'reocr' AND status IN ('pending', 'running')
		ORDER BY id DESC LIMIT 1"#;
pub(crate) const ENQUEUE_REPROCESS_JOB_1: &str =
    r#"INSERT INTO jobs (document_id, kind, status, attempts) VALUES (?, 'reocr', 'pending', 0)"#;
pub(crate) const CLAIM_JOB_BY_I_D_0: &str = r#"
		UPDATE jobs
		SET status = 'running',
			attempts = attempts + 1,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
		  AND (status = 'pending'
		       OR (status = 'failed' AND attempts < 3 AND updated_at <= datetime('now', '-10 seconds')))
		RETURNING id, document_id, kind, status, attempts, last_error, created_at, updated_at
	"#;
pub(crate) const CLAIM_JOB_0: &str = r#"
		UPDATE jobs
		SET status = 'running',
			attempts = attempts + 1,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = (
			SELECT id FROM jobs
			WHERE status = 'pending'
			   OR (status = 'failed' AND attempts < 3 AND updated_at <= datetime('now', '-10 seconds'))
			ORDER BY created_at ASC
			LIMIT 1
		)
		RETURNING id, document_id, kind, status, attempts, last_error, created_at, updated_at
	"#;
pub(crate) const COMPLETE_JOB_0: &str = r#"UPDATE jobs SET status = 'completed', updated_at = CURRENT_TIMESTAMP WHERE id = ? RETURNING document_id"#;
pub(crate) const COMPLETE_JOB_1: &str =
    r#"UPDATE documents SET status = 'done', updated_at = CURRENT_TIMESTAMP WHERE id = ?"#;
pub(crate) const FAIL_JOB_0: &str = r#"UPDATE jobs SET status = 'failed', last_error = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? RETURNING document_id, attempts"#;
pub(crate) const FAIL_JOB_1: &str =
    r#"UPDATE documents SET status = 'failed', updated_at = CURRENT_TIMESTAMP WHERE id = ?"#;
pub(crate) const RETRY_JOB_0: &str = r#"UPDATE jobs SET status = 'pending', attempts = 0, last_error = NULL, updated_at = CURRENT_TIMESTAMP WHERE id = ? RETURNING document_id"#;
pub(crate) const RETRY_JOB_1: &str =
    r#"UPDATE documents SET status = 'pending', updated_at = CURRENT_TIMESTAMP WHERE id = ?"#;
pub(crate) const RESET_RUNNING_JOBS_0: &str =
    r#"UPDATE jobs SET status = 'pending' WHERE status = 'running'"#;
