-- Key/value storage for the settings document and the admin password hash.
CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- Login sessions. Only a hash of the session token is stored, so a copy
-- of the database does not let anyone take over a session.
CREATE TABLE sessions (
    token_hash TEXT PRIMARY KEY,
    expires_at INTEGER NOT NULL
);

-- AUTOINCREMENT guarantees that an id is never reused after a delete;
-- the id is part of the agent token.
CREATE TABLE agents (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT    NOT NULL UNIQUE,
    token_hash   TEXT    NOT NULL,
    hostname     TEXT    NOT NULL DEFAULT '',
    host_ip      TEXT    NOT NULL DEFAULT '',
    observed_ip  TEXT    NOT NULL DEFAULT '',
    version      TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL DEFAULT 0
);

-- The last snapshot each agent reported. Replaced on every report.
CREATE TABLE containers (
    agent_id        INTEGER NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    container_id    TEXT    NOT NULL,
    name            TEXT    NOT NULL,
    image           TEXT    NOT NULL,
    workload        TEXT    NOT NULL,
    compose_project TEXT    NOT NULL DEFAULT '',
    compose_service TEXT    NOT NULL DEFAULT '',
    labels          TEXT    NOT NULL DEFAULT '{}',
    ports           TEXT    NOT NULL DEFAULT '[]',
    PRIMARY KEY (agent_id, container_id)
);

CREATE TABLE routes (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    -- label: derived from container labels; ui: container approved in
    -- the UI; manual: arbitrary host:port entered by hand.
    source         TEXT    NOT NULL CHECK (source IN ('label', 'ui', 'manual')),
    hostname       TEXT    NOT NULL,
    -- label and ui routes point at a workload on an agent ...
    agent_id       INTEGER REFERENCES agents(id) ON DELETE CASCADE,
    workload       TEXT    NOT NULL DEFAULT '',
    container_port INTEGER NOT NULL DEFAULT 0, -- 0 = the only published port
    -- ... manual routes at a fixed address.
    target_host    TEXT    NOT NULL DEFAULT '',
    target_port    INTEGER NOT NULL DEFAULT 0,
    backend_https  INTEGER NOT NULL DEFAULT 0,
    -- ok | conflict (hostname already taken) | invalid (see detail)
    state          TEXT    NOT NULL DEFAULT 'ok',
    detail         TEXT    NOT NULL DEFAULT '',
    created_at     INTEGER NOT NULL
);

-- The database itself guarantees that a hostname has only one owner.
-- Rows in state conflict/invalid are parked and do not count.
CREATE UNIQUE INDEX routes_hostname_owner ON routes (hostname) WHERE state = 'ok';

-- At most one label route per workload.
CREATE UNIQUE INDEX routes_label_workload ON routes (agent_id, workload) WHERE source = 'label';
