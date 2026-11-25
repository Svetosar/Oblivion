-- Optimistic locking. An editor must send the version it loaded; a save succeeds
-- only if the row still has that version and then bumps it. updated_at cannot do
-- this job: it counts whole seconds, so two saves in one second look identical.
-- Cheap on any size: SQLite stores the default without rewriting rows.
ALTER TABLE content_items ADD COLUMN version INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1);
