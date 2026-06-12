"""ConnectionManager — SQLite-backed registry of SSH sessions."""

import aiosqlite

from .session import RemoteSession


class ConnectionManager:

    def __init__(self, db: aiosqlite.Connection):
        self.db = db
        self._sessions: dict[str, RemoteSession] = {}

    # ── Lifecycle ─────────────────────────────────────────────────

    async def load(self):
        """Restore persisted clients from the database."""
        cur = await self.db.execute("SELECT * FROM clients ORDER BY created_at")
        for row in await cur.fetchall():
            name = row["name"]
            s = RemoteSession(name=name, host=row["host"], port=row["port"],
                              user=row["user"], password=row["password"],
                              enabled=bool(row["enabled"]),
                              safe_rm=bool(row["safe_rm"]))
            s.set_audit_callback(self._on_audit)
            self._sessions[name] = s

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

    async def add(self, name, host, user, password, port=22, enabled=True, safe_rm=False):
        if name in self._sessions:
            raise ValueError(f"客户端 '{name}' 已存在。")
        await self.db.execute(
            """INSERT INTO clients (name, host, port, "user", password, enabled, safe_rm)
               VALUES (:n, :h, :p, :u, :pw, :e, :sr)""",
            dict(n=name, h=host, p=port, u=user, pw=password, e=int(enabled), sr=int(safe_rm)),
        )
        await self.db.commit()

        s = RemoteSession(name=name, host=host, port=port, user=user,
                          password=password, enabled=enabled, safe_rm=safe_rm)
        s.set_audit_callback(self._on_audit)
        self._sessions[name] = s
        return self._to_dict(name)

    async def remove(self, name):
        if name not in self._sessions:
            raise KeyError(f"客户端 '{name}' 不存在。")
        await self._sessions.pop(name).disconnect()
        await self.db.execute("DELETE FROM clients WHERE name=?", (name,))
        await self.db.commit()

    async def update(self, name, **fields):
        if name not in self._sessions:
            raise KeyError(f"客户端 '{name}' 不存在。")
        s = self._sessions[name]
        if "enabled" in fields:
            s.enabled = bool(fields["enabled"])
        if "safe_rm" in fields:
            s.safe_rm = bool(fields["safe_rm"])
        if "host" in fields:
            s.host = fields["host"]
        if "port" in fields:
            s.port = fields["port"]
        if "user" in fields:
            s.user = fields["user"]
        if "password" in fields:
            s.password = fields["password"]

        allowed = {"host", "port", "user", "password", "enabled", "safe_rm"}
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
                hint = f" 已启用的客户端: {names}。"
            raise KeyError(f"客户端 '{name}' 不存在。{hint}")
        return self._sessions[name]

    def list_all(self):
        return [self._to_dict(n) for n in self._sessions]

    def list_enabled(self):
        return [self._to_dict(n) for n, s in self._sessions.items() if s.enabled]

    # ── Audit queries ─────────────────────────────────────────────

    @staticmethod
    def _iso_to_sqlite(iso: str | None) -> str | None:
        """Normalize ISO 8601 to SQLite datetime format for string comparison.

        ``2026-06-12T11:30:00.123Z`` → ``2026-06-12 11:30:00``.
        """
        if not iso:
            return None
        # Strip trailing timezone / fractional seconds, replace T with space.
        s = iso.strip()
        if "T" in s:
            s = s.split("T")[0] + " " + s.split("T")[1][:8]
        return s

    async def audit_list(self, client_name=None, after=None, before=None, limit=200, offset=0):
        where = []
        params = []
        if client_name:
            where.append("client_name=?")
            params.append(client_name)
        after = self._iso_to_sqlite(after)
        before = self._iso_to_sqlite(before)
        if after:
            where.append("created_at>=?")
            params.append(after)
        if before:
            where.append("created_at<?")
            params.append(before)
        sql = "SELECT * FROM audit_log"
        if where:
            sql += " WHERE " + " AND ".join(where)
        sql += " ORDER BY created_at DESC LIMIT ? OFFSET ?"
        params.extend([limit, offset])
        cur = await self.db.execute(sql, params)
        return [dict(r) for r in await cur.fetchall()]

    async def audit_count(self, client_name=None, after=None, before=None):
        where = []
        params = []
        if client_name:
            where.append("client_name=?")
            params.append(client_name)
        after = self._iso_to_sqlite(after)
        before = self._iso_to_sqlite(before)
        if after:
            where.append("created_at>=?")
            params.append(after)
        if before:
            where.append("created_at<?")
            params.append(before)
        sql = "SELECT COUNT(*) AS cnt FROM audit_log"
        if where:
            sql += " WHERE " + " AND ".join(where)
        cur = await self.db.execute(sql, params)
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
        return self._sessions[name].to_dict()
