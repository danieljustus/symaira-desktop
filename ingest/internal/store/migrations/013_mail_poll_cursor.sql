CREATE TABLE IF NOT EXISTS mail_poll_cursor (
    account_id TEXT PRIMARY KEY,
    folder TEXT NOT NULL,
    uid_validity INTEGER NOT NULL,
    last_uid INTEGER NOT NULL
);
