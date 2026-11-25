package store

import "github.com/devXpro/mcp-gatekeeper/store/sqlitestore"

// migrations are the hub's own schema steps, applied after the gatekeeper's and tracked apart from them (component
// "app" of gk_schema_migrations). A released step is never edited: change the schema with a new one.
var migrations = []sqlitestore.Migration{
	{Version: 1, Name: "plugins, settings, activity and imports", SQL: `
-- The installed plugins: what the plugin host runs and where each came from. command is a JSON array.
CREATE TABLE plugins (
	id               TEXT PRIMARY KEY,
	source           TEXT NOT NULL,
	repo             TEXT NOT NULL DEFAULT '',
	version          TEXT NOT NULL DEFAULT '',
	previous_version TEXT NOT NULL DEFAULT '',
	command          TEXT NOT NULL DEFAULT '[]',
	enabled          INTEGER NOT NULL DEFAULT 1,
	installed_at     INTEGER NOT NULL,
	updated_at       INTEGER NOT NULL
) STRICT;

-- The one row of home settings. home_name only pre-fills the first setup: once the account exists, its name is the
-- home's name. access_reset_at and access_reset_by are the last reset-access on the admin socket (review GK-5).
CREATE TABLE settings (
	id              INTEGER PRIMARY KEY CHECK (id = 1),
	language        TEXT NOT NULL DEFAULT 'ru',
	home_name       TEXT NOT NULL DEFAULT '',
	access_reset_at INTEGER NOT NULL DEFAULT 0,
	access_reset_by TEXT NOT NULL DEFAULT '',
	updated_at      INTEGER NOT NULL
) STRICT;

-- Every tool call. person_id has no foreign key: a record outlives the person, and keeps the name it was written with.
-- AUTOINCREMENT keeps ids increasing after a prune, so a page cursor never points at a reused id.
CREATE TABLE activity (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	at          INTEGER NOT NULL,
	person_id   TEXT NOT NULL DEFAULT '',
	person      TEXT NOT NULL DEFAULT '',
	via         TEXT NOT NULL DEFAULT '',
	tool        TEXT NOT NULL DEFAULT '',
	plugin_id   TEXT NOT NULL DEFAULT '',
	args        TEXT,
	ok          INTEGER NOT NULL,
	error       TEXT NOT NULL DEFAULT '',
	duration_ns INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX activity_at ON activity (at);
CREATE INDEX activity_person ON activity (person_id, id);
CREATE INDEX activity_plugin ON activity (plugin_id, id);
CREATE INDEX activity_tool ON activity (tool, id);

-- One-time imports that ran (ImportV01): the mark is written in the import's own transaction.
CREATE TABLE imports (
	name    TEXT PRIMARY KEY,
	done_at INTEGER NOT NULL
) STRICT;
`},
}
