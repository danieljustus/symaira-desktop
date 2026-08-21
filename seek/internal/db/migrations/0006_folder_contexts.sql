-- Add folder_contexts table for QMD-style context trees.
-- Each row associates a filesystem path prefix with descriptive context text.
-- Longest-prefix-match determines which context applies to a given file path.

CREATE TABLE IF NOT EXISTS folder_contexts (
    path_prefix TEXT PRIMARY KEY,
    context_text TEXT NOT NULL
);
