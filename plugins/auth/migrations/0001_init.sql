CREATE TABLE auth_users (
    id            INTEGER PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE COLLATE NOCASE CHECK (length(username) BETWEEN 3 AND 32),
    password_hash TEXT NOT NULL,
    role          TEXT NOT NULL CHECK (role IN ('admin','editor','author')),
    disabled      INTEGER NOT NULL DEFAULT 0 CHECK (disabled IN (0,1)),
    created_at    INTEGER NOT NULL
) STRICT;

-- Only a SHA-256 of the session token is stored: a database leak does not leak live sessions.
CREATE TABLE auth_sessions (
    token_hash BLOB PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
    csrf       TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    last_seen  INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
) STRICT;
CREATE INDEX auth_sessions_user ON auth_sessions(user_id);
