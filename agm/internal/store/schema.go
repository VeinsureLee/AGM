package store

// CurrentSchemaVersion is the expected schema version for this AGM build.
const CurrentSchemaVersion = 1

// schemaV1 creates all tables, indexes and seeds schema_version. It is
// idempotent because every statement uses IF NOT EXISTS.
const schemaV1 = `
CREATE TABLE IF NOT EXISTS schema_version (
    version INTEGER PRIMARY KEY,
    applied_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    agent_type TEXT NOT NULL DEFAULT 'claude-code',
    started_at INTEGER NOT NULL,
    stopped_at INTEGER,
    state      TEXT NOT NULL DEFAULT 'running',
    cwd        TEXT NOT NULL,
    metadata   TEXT
);

CREATE INDEX IF NOT EXISTS idx_sessions_state   ON sessions(state);
CREATE INDEX IF NOT EXISTS idx_sessions_started ON sessions(started_at);

CREATE TABLE IF NOT EXISTS events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT,
    event_type TEXT NOT NULL,
    timestamp  INTEGER NOT NULL,
    payload    TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_events_session ON events(session_id);
CREATE INDEX IF NOT EXISTS idx_events_type    ON events(event_type);
CREATE INDEX IF NOT EXISTS idx_events_ts      ON events(timestamp);

CREATE TABLE IF NOT EXISTS file_changes (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT,
    path       TEXT NOT NULL,
    operation  TEXT NOT NULL,
    timestamp  INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_fc_session ON file_changes(session_id);
CREATE INDEX IF NOT EXISTS idx_fc_path    ON file_changes(path);
`
