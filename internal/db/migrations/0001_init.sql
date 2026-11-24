-- Core key/value metadata (instance id, feature flags). Users/sessions come in 0002.
CREATE TABLE kernel_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) STRICT;
