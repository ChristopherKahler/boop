-- Intakes: one webhook address per project and service (Sentry, Healthchecks,
-- Uptime Kuma, GitHub Actions, or a generic JSON mapping). The address carries
-- a secret token; only its hash is stored.
CREATE TABLE intakes (
    id               TEXT PRIMARY KEY,
    project_id       TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    kind             TEXT NOT NULL,
    name             TEXT NOT NULL DEFAULT '',
    token_hash       TEXT NOT NULL UNIQUE,
    token_hint       TEXT NOT NULL DEFAULT '',
    -- A signing secret issued by the service (Sentry's client secret), used to
    -- verify request signatures. Never returned by the API.
    signing_secret   TEXT NOT NULL DEFAULT '',
    mapping_json     TEXT NOT NULL DEFAULT '{}',
    received_count   INTEGER NOT NULL DEFAULT 0,
    last_received_at TEXT,
    last_result      TEXT NOT NULL DEFAULT '',
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);
CREATE INDEX intakes_project_id ON intakes(project_id);
