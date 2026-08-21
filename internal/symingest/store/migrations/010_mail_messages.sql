CREATE TABLE IF NOT EXISTS mail_messages (
    message_id TEXT PRIMARY KEY,
    source_mailbox TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
