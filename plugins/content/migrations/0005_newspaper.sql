-- Newspaper model: rubrics and the editorial fields of an article.
--
-- Size: a site with thousands of rows at most; every statement here is cheap. In SQLite
-- ADD COLUMN with a constant default does not rewrite the table, and a new table is empty.
--
-- Rubrics. color is a name from a fixed palette (the theme maps it to a hex value whose
-- contrast was checked), never a free colour. A rubric is a topic, so its slug only has to
-- be unique among rubrics: its path is /rubric/{slug}.
CREATE TABLE content_rubrics (
    id       INTEGER PRIMARY KEY,
    slug     TEXT    NOT NULL UNIQUE CHECK (length(slug) BETWEEN 1 AND 60),
    name     TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 60),
    color    TEXT    NOT NULL CHECK (color IN ('blue','green','purple','teal','brown','olive','slate','crimson')),
    position INTEGER NOT NULL DEFAULT 0
) STRICT;

-- rubric_id is NULL for "no rubric". ON DELETE SET NULL is spelled out: deleting a rubric
-- must never delete (or fail on) articles. The plugin opens SQLite with foreign_keys on.
ALTER TABLE content_items ADD COLUMN rubric_id INTEGER REFERENCES content_rubrics(id) ON DELETE SET NULL;
-- dek: the subtitle under the headline. cover_media: a file name from the media plugin (checked
-- for shape by the API; no foreign key, plugins do not touch each other's tables).
ALTER TABLE content_items ADD COLUMN dek           TEXT    NOT NULL DEFAULT '' CHECK (length(dek) <= 300);
ALTER TABLE content_items ADD COLUMN cover_media   TEXT    NOT NULL DEFAULT '' CHECK (length(cover_media) <= 64);
ALTER TABLE content_items ADD COLUMN cover_caption TEXT    NOT NULL DEFAULT '' CHECK (length(cover_caption) <= 300);
-- placement on the front page: 0 none, 1 the main story, 2 important.
ALTER TABLE content_items ADD COLUMN placement     INTEGER NOT NULL DEFAULT 0 CHECK (placement IN (0, 1, 2));

-- The child side of the foreign key and the rubric page query: published items of a rubric, newest first.
CREATE INDEX content_items_rubric ON content_items(rubric_id, status, published_at DESC);
-- The front page picks the main and important stories: a small partial index, most rows are 0.
CREATE INDEX content_items_placement ON content_items(placement, status, published_at DESC) WHERE placement > 0;
