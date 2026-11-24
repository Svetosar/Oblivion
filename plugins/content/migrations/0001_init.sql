-- No foreign key to the auth plugin's tables: plugins own their schema and must
-- not depend on each other's. author_name is a snapshot for display.
CREATE TABLE content_items (
    id           INTEGER PRIMARY KEY,
    kind         TEXT NOT NULL CHECK (kind IN ('post','page')),
    slug         TEXT NOT NULL CHECK (length(slug) BETWEEN 1 AND 100),
    title        TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    body_md      TEXT NOT NULL CHECK (length(body_md) <= 500000),
    body_html    TEXT NOT NULL,  -- sanitized at save time
    status       TEXT NOT NULL CHECK (status IN ('draft','published')),
    author_id    INTEGER NOT NULL,
    author_name  TEXT NOT NULL,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL,
    published_at INTEGER,
    CHECK ((status = 'published') = (published_at IS NOT NULL)),
    UNIQUE (kind, slug)
) STRICT;
CREATE INDEX content_items_public ON content_items(kind, status, published_at DESC);
