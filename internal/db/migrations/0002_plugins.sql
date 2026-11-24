-- Plugin registry state and the namespaced key/value store.
CREATE TABLE plugins (
    id           TEXT PRIMARY KEY,
    enabled      INTEGER NOT NULL CHECK (enabled IN (0,1)),
    granted_caps TEXT NOT NULL DEFAULT '[]',   -- JSON array, shown to the admin
    installed_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
) STRICT;

CREATE TABLE plugin_kv (
    plugin_id TEXT NOT NULL REFERENCES plugins(id) ON DELETE CASCADE,
    key       TEXT NOT NULL,
    value     BLOB NOT NULL,
    PRIMARY KEY (plugin_id, key)
) STRICT;
