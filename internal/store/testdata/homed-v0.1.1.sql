-- Initial schema of homed.db (PLAN section 5.6). Times are INTEGER Unix milliseconds in UTC; 0 means "never" or "not
-- set". Secrets (session cookies, person keys, invite tokens) are stored only as sha256 hex.

CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) STRICT;

CREATE TABLE people (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    role        TEXT NOT NULL CHECK (role IN ('owner', 'member')),
    plugins     TEXT NOT NULL DEFAULT '[]',   -- JSON array of plugin ids; ["*"] means all
    language    TEXT NOT NULL DEFAULT '',
    user_handle BLOB NOT NULL UNIQUE,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
) STRICT;

CREATE TABLE passkeys (
    id           TEXT PRIMARY KEY,
    person_id    TEXT NOT NULL REFERENCES people (id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    provider     TEXT NOT NULL DEFAULT '',
    credential   BLOB NOT NULL,
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX passkeys_person ON passkeys (person_id, created_at);

CREATE TABLE sessions (
    id           TEXT PRIMARY KEY,
    token_hash   TEXT NOT NULL UNIQUE,
    person_id    TEXT NOT NULL REFERENCES people (id) ON DELETE CASCADE,
    passkey_id   TEXT NOT NULL DEFAULT '',
    device       TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    step_up_at   INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX sessions_person ON sessions (person_id, last_seen_at);

CREATE TABLE person_keys (
    id            TEXT PRIMARY KEY,
    person_id     TEXT NOT NULL REFERENCES people (id) ON DELETE CASCADE,
    sha256        TEXT NOT NULL UNIQUE,
    label         TEXT NOT NULL DEFAULT '',
    hint          TEXT NOT NULL DEFAULT '',
    connection_id TEXT NOT NULL DEFAULT '',
    created_at    INTEGER NOT NULL,
    last_used_at  INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX person_keys_person ON person_keys (person_id, created_at);

CREATE TABLE invites (
    id         TEXT PRIMARY KEY,
    token_hash TEXT NOT NULL UNIQUE,
    kind       TEXT NOT NULL CHECK (kind IN ('setup', 'member')),
    person_id  TEXT REFERENCES people (id) ON DELETE CASCADE,   -- NULL: a setup link before the owner exists
    created_by TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    used_at    INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX invites_active ON invites (kind, used_at, expires_at);

CREATE TABLE connections (
    id           TEXT PRIMARY KEY,
    person_id    TEXT NOT NULL REFERENCES people (id) ON DELETE CASCADE,
    kind         TEXT NOT NULL,
    state        TEXT NOT NULL,
    title        TEXT NOT NULL DEFAULT '',
    key_id       TEXT NOT NULL DEFAULT '',
    detail       TEXT NOT NULL DEFAULT '',   -- JSON, empty when none
    fingerprint  TEXT NOT NULL DEFAULT '',
    published_at INTEGER NOT NULL DEFAULT 0,
    last_error   TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
) STRICT;
CREATE INDEX connections_person ON connections (person_id, created_at);

CREATE TABLE plugins (
    id               TEXT PRIMARY KEY,
    source           TEXT NOT NULL,
    repo             TEXT NOT NULL DEFAULT '',
    version          TEXT NOT NULL DEFAULT '',
    previous_version TEXT NOT NULL DEFAULT '',
    command          TEXT NOT NULL DEFAULT '[]',   -- JSON array
    enabled          INTEGER NOT NULL DEFAULT 1,
    installed_at     INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL
) STRICT;

-- AUTOINCREMENT keeps ids strictly increasing even after Prune, so a pagination cursor never points at a reused id.
CREATE TABLE audit (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    at          INTEGER NOT NULL,
    kind        TEXT NOT NULL CHECK (kind IN ('tool', 'admin')),
    person_id   TEXT NOT NULL DEFAULT '',   -- no foreign key: records outlive the person
    person      TEXT NOT NULL DEFAULT '',
    via         TEXT NOT NULL DEFAULT '',
    tool        TEXT NOT NULL DEFAULT '',
    plugin_id   TEXT NOT NULL DEFAULT '',
    action      TEXT NOT NULL DEFAULT '',
    target      TEXT NOT NULL DEFAULT '',
    args        TEXT,
    ok          INTEGER NOT NULL,
    error       TEXT NOT NULL DEFAULT '',
    duration_ns INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX audit_at ON audit (at);
CREATE INDEX audit_person ON audit (person_id, id);
CREATE INDEX audit_plugin ON audit (plugin_id, id);
CREATE INDEX audit_tool ON audit (tool, id);

CREATE TABLE settings (
    id              INTEGER PRIMARY KEY CHECK (id = 1),
    home_name       TEXT NOT NULL DEFAULT '',
    language        TEXT NOT NULL DEFAULT 'ru',
    connection_kind TEXT NOT NULL DEFAULT '',
    site_sign_out   INTEGER NOT NULL DEFAULT 0,
    updated_at      INTEGER NOT NULL
) STRICT;
