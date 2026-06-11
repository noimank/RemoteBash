"""ConnectionManager — SQLite-backed registry of SSH sessions."""

import aiosqlite

from .session import RemoteSession


class ConnectionManager:

    def __init__(self, db: aiosqlite.Connection):
        self.db = db
        self._sessions: dict[str, RemoteSession] = {}
        self._labels: dict[str, str] = {}

    # ── Lifecycle ─────────────────────────────────────────────────

    async def load(self):
        """Restore persisted clients from the database."""
        cur = await self.db.execute("SELECT * FROM clients ORDER BY created_at")
        for row in await cur.fetchall():
            name = row["name"]
            s = RemoteSession(name=name, host=row["host"], port=row["port"],
                              user=row["user"], password=row["password"],
                              enabled=bool(row["enabled"]))
            s.set_audit_callback(self._on_audit)
            self._sessions[name] = s
            self._labels[name] = row["label"]

    async def close(self):
        for s in self._sessions.values():
            await s.disconnect()
        self._sessions.clear()

    # ── Audit ─────────────────────────────────────────────────────

    async def _on_audit(self, client_name, command, result):
        await self.db.execute(
            """INSERT INTO audit_log (client_name, command, stdout, stderr,
               exit_code, cwd, duration_ms, success)
               VALUES (:c, :cmd, :so, :se, :ec, :wd, :ms, :ok)""",
            dict(c=client_name, cmd=command,
                 so=result["stdout"], se=result["stderr"],
                 ec=result["exit_code"], wd=result["cwd"],
                 ms=result["duration_ms"],
                 ok=1 if result["exit_code"] == 0 else 0),
        )
        await self.db.commit()

    # ── Clients ───────────────────────────────────────────────────

    async def add(self, name, host, user, password, port=22, label="", enabled=True):
        if name in self._sessions:
            raise ValueError(f"Client '{name}' already exists.")
        await self.db.execute(
            """INSERT INTO clients (name, host, port, "user", password, label, enabled)
               VALUES (:n, :h, :p, :u, :pw, :l, :e)""",
            dict(n=name, h=host, p=port, u=user, pw=password, l=label, e=int(enabled)),
        )
        await self.db.commit()

        s = RemoteSession(name=name, host=host, port=port, user=user,
                          password=password, enabled=enabled)
        s.set_audit_callback(self._on_audit)
        self._sessions[name] = s
        self._labels[name] = label
        return self._to_dict(name)

    async def remove(self, name):
        if name not in self._sessions:
            raise KeyError(f"Client '{name}' not found.")
        await self._sessions.pop(name).disconnect()
        self._labels.pop(name, None)
        await self.db.execute("DELETE FROM clients WHERE name=?", (name,))
        await self.db.commit()

    async def update(self, name, **fields):
        if name not in self._sessions:
            raise KeyError(f"Client '{name}' not found.")
        s = self._sessions[name]
        if "enabled" in fields:
            s.enabled = bool(fields["enabled"])
        if "label" in fields:
            self._labels[name] = fields["label"]

        allowed = {"host", "port", "user", "password", "label", "enabled"}
        updates = {k: v for k, v in fields.items() if k in allowed}
        if updates:
            updates["name"] = name
            cols = ", ".join(f"{k}=:{k}" for k in updates)
            await self.db.execute(
                f"UPDATE clients SET {cols}, updated_at=datetime('now') WHERE name=:name",
                updates,
            )
            await self.db.commit()
        return self._to_dict(name)

    def get(self, name):
        if name not in self._sessions:
            enabled = self.list_enabled()
            hint = ""
            if enabled:
                names = ", ".join(c["name"] for c in enabled)
                hint = f" Enabled clients: {names}."
            raise KeyError(f"Client '{name}' not found.{hint}")
        return self._sessions[name]

    def list_all(self):
        return [self._to_dict(n) for n in self._sessions]

    def list_enabled(self):
        return [self._to_dict(n) for n, s in self._sessions.items() if s.enabled]

    # ── Audit queries ─────────────────────────────────────────────

    async def audit_list(self, client_name=None, limit=200, offset=0):
        if client_name:
            cur = await self.db.execute(
                "SELECT * FROM audit_log WHERE client_name=? ORDER BY created_at DESC LIMIT ? OFFSET ?",
                (client_name, limit, offset),
            )
        else:
            cur = await self.db.execute(
                "SELECT * FROM audit_log ORDER BY created_at DESC LIMIT ? OFFSET ?", (limit, offset),
            )
        return [dict(r) for r in await cur.fetchall()]

    async def audit_count(self, client_name=None):
        if client_name:
            cur = await self.db.execute("SELECT COUNT(*) AS cnt FROM audit_log WHERE client_name=?", (client_name,))
        else:
            cur = await self.db.execute("SELECT COUNT(*) AS cnt FROM audit_log")
        return (await cur.fetchone())["cnt"]

    async def audit_delete(self, entry_id=None, client_name=None, before_id=None):
        if entry_id is not None:
            cur = await self.db.execute("DELETE FROM audit_log WHERE id=?", (entry_id,))
        elif client_name is not None:
            cur = await self.db.execute("DELETE FROM audit_log WHERE client_name=?", (client_name,))
        elif before_id is not None:
            cur = await self.db.execute("DELETE FROM audit_log WHERE id < ?", (before_id,))
        else:
            return 0
        await self.db.commit()
        return cur.rowcount

    # ── Internal ──────────────────────────────────────────────────

    def _to_dict(self, name):
        d = self._sessions[name].to_dict()
        d["label"] = self._labels.get(name, "")
        return d
