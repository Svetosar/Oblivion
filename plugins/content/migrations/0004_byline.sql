-- The author shown on the public site: a nickname typed with the article, never
-- the account name (author_name stays for the admin only). Empty = no author is
-- shown, which is what every existing article gets.
ALTER TABLE content_items ADD COLUMN byline TEXT NOT NULL DEFAULT '' CHECK (length(byline) <= 32);
