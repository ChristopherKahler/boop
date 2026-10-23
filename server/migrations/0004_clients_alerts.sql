-- Clients sit above projects: every project belongs to one client. Routing
-- (which levels push to the phone, which only land in the inbox) is set per
-- client, and a project may override it.
CREATE TABLE clients (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    slug            TEXT NOT NULL UNIQUE,
    push_levels     TEXT NOT NULL DEFAULT 'warning,error,critical',
    push_recoveries INTEGER NOT NULL DEFAULT 1,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);

INSERT INTO clients (id, name, slug, push_levels, push_recoveries, created_at, updated_at)
VALUES ('cli_internal', 'Internal', 'internal', 'warning,error,critical', 1,
        strftime('%Y-%m-%dT%H:%M:%f', 'now') || '000000Z', strftime('%Y-%m-%dT%H:%M:%f', 'now') || '000000Z');

-- push_levels NULL means "use the client's"; '' means "push nothing".
ALTER TABLE projects ADD COLUMN client_id TEXT REFERENCES clients(id);
ALTER TABLE projects ADD COLUMN push_levels TEXT;

-- Existing projects keep pushing exactly what their min_level allowed.
UPDATE projects SET client_id = 'cli_internal',
    push_levels = CASE min_level
        WHEN 'info' THEN 'info,success,warning,error,critical'
        WHEN 'success' THEN 'success,warning,error,critical'
        WHEN 'warning' THEN 'warning,error,critical'
        WHEN 'error' THEN 'error,critical'
        ELSE 'critical' END;
CREATE INDEX projects_client_id ON projects(client_id);

-- An alert group is one problem: the warning/error/critical events sharing a
-- fingerprint within a project, from the first occurrence until it resolves.
-- A new occurrence after resolution opens a new group, so each row is one
-- incident with its own acknowledge and resolve times.
CREATE TABLE alert_groups (
    id               TEXT PRIMARY KEY,
    project_id       TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    group_key        TEXT NOT NULL, -- the fingerprint, or event:<id> for events without one
    fingerprint      TEXT NOT NULL DEFAULT '',
    source           TEXT NOT NULL DEFAULT '',
    title            TEXT NOT NULL,
    level            TEXT NOT NULL,
    event_count      INTEGER NOT NULL DEFAULT 0,
    first_seen       TEXT NOT NULL,
    last_seen        TEXT NOT NULL,
    last_event_id    TEXT,
    notified_at      TEXT,
    acknowledged_at  TEXT,
    acknowledged_by  TEXT NOT NULL DEFAULT '',
    resolved_at      TEXT,
    resolved_by      TEXT NOT NULL DEFAULT '',
    resolve_event_id TEXT,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);
CREATE UNIQUE INDEX alert_groups_open_key ON alert_groups(project_id, group_key) WHERE resolved_at IS NULL;
CREATE INDEX alert_groups_last_seen ON alert_groups(last_seen, id);
CREATE INDEX alert_groups_first_seen ON alert_groups(first_seen);

ALTER TABLE events ADD COLUMN group_id TEXT REFERENCES alert_groups(id) ON DELETE SET NULL;
-- Why the event did or did not push ("pushed: alert opened", "inbox: repeat of an open alert", ...).
ALTER TABLE events ADD COLUMN route TEXT NOT NULL DEFAULT '';
CREATE INDEX events_group_id ON events(group_id);
