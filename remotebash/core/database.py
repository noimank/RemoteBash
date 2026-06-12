"""Database schema and connection."""

from pathlib import Path

import aiosqlite

SCHEMA = """
CREATE TABLE IF NOT EXISTS clients (
    name        TEXT PRIMARY KEY,
    host        TEXT NOT NULL,
    port        INTEGER NOT NULL DEFAULT 22,
    "user"      TEXT NOT NULL,
    password    TEXT NOT NULL,
    safe_rm     INTEGER NOT NULL DEFAULT 0,
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS audit_log (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    client_name  TEXT NOT NULL,
    command      TEXT NOT NULL,
    stdout       TEXT DEFAULT '',
    stderr       TEXT DEFAULT '',
    exit_code    INTEGER DEFAULT -1,
    cwd          TEXT DEFAULT '',
    duration_ms  INTEGER DEFAULT 0,
    success      INTEGER NOT NULL DEFAULT 0,
    created_at   TEXT NOT NULL DEFAULT (datetime('now')),
    FOREIGN KEY (client_name) REFERENCES clients(name) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_audit_client ON audit_log(client_name);
CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_log(created_at);
"""


async def open_db(path: str | Path) -> aiosqlite.Connection:
    """Open and initialise the database.  Creates parent dirs if needed."""
    p = Path(path)
    p.parent.mkdir(parents=True, exist_ok=True)
    db = await aiosqlite.connect(str(p))
    db.row_factory = aiosqlite.Row
    await db.executescript(SCHEMA)
    await db.commit()
    return db
