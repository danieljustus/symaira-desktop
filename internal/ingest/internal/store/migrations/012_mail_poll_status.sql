CREATE TABLE IF NOT EXISTS mail_poll_status (
    account_id TEXT PRIMARY KEY,
    last_polled_at DATETIME NOT NULL,
    status TEXT NOT NULL,
    last_error TEXT
);
