CREATE TABLE media_items (
    id          INTEGER PRIMARY KEY,
    stored_name TEXT NOT NULL UNIQUE,   -- random; the only name used on disk and in URLs
    filename    TEXT NOT NULL,          -- original name, display only, sanitized
    mime        TEXT NOT NULL CHECK (mime IN ('image/jpeg','image/png','image/gif','image/webp')),
    size        INTEGER NOT NULL CHECK (size > 0),
    width       INTEGER NOT NULL CHECK (width > 0),
    height      INTEGER NOT NULL CHECK (height > 0),
    uploader_id INTEGER NOT NULL,
    created_at  INTEGER NOT NULL
) STRICT;
