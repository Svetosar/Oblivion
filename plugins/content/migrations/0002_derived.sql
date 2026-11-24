-- Values derived from body_html at save time, so listing pages and the feed do
-- not parse HTML on every request. derived = 0 marks rows that still have to be
-- computed (existing rows; filled once at startup).
ALTER TABLE content_items ADD COLUMN lead     TEXT    NOT NULL DEFAULT '';
ALTER TABLE content_items ADD COLUMN og_image TEXT    NOT NULL DEFAULT '';
ALTER TABLE content_items ADD COLUMN derived  INTEGER NOT NULL DEFAULT 0;
