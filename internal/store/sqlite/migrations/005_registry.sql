-- Registry: project configuration moves from projects.json into the
-- database (managed-config spec §3.1). The database is the sole runtime
-- source of truth; SyncProjects and its boot-archiving are deleted.

ALTER TABLE projects ADD COLUMN allowed_origins     TEXT NOT NULL DEFAULT '[]';
ALTER TABLE projects ADD COLUMN retention           TEXT;
ALTER TABLE projects ADD COLUMN product_aggregation TEXT;

CREATE TABLE ingest_keys (
    key         TEXT PRIMARY KEY,
    project     TEXT NOT NULL REFERENCES projects(alias),
    label       TEXT NOT NULL,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    disabled_at TEXT                                      -- NULL = active
);
CREATE INDEX idx_ingest_keys_project ON ingest_keys(project);

CREATE TABLE audit_log (
    ts      TEXT NOT NULL DEFAULT (datetime('now')),
    actor   TEXT NOT NULL,          -- 'mcp' or 'cli'
    action  TEXT NOT NULL,          -- 'project.create', 'key.disable', ...
    subject TEXT NOT NULL,          -- alias or key label
    detail  TEXT NOT NULL DEFAULT ''
);

INSERT INTO meta (key, value) VALUES ('config_version', '1')
ON CONFLICT(key) DO NOTHING;
