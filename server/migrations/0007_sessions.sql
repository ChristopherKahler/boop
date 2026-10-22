-- Login sessions, kept across restarts so the installed phone app stays
-- signed in. Only the token's hash is stored.
CREATE TABLE sessions (
    token_hash TEXT PRIMARY KEY,
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    user_agent TEXT NOT NULL DEFAULT ''
);
CREATE INDEX sessions_expires_at ON sessions(expires_at);
