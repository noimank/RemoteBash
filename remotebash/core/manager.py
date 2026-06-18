"""ConnectionManager — SQLite-backed registry of SSH sessions."""

import aiosqlite

from .session import RemoteSession


class ConnectionManager:

    def __init__(self, db: aiosqlite.Connection):
        self.db = db
        self._sessions: dict[str, RemoteSession] = {}
        # Browser-terminal shells — separate from the MCP exec shells so the
        # two consumption modes don't corrupt each other's PTY framing.
        # Keyed by client name; one terminal per client.
        self._terminals: dict[str, object] = {}

    # ── Lifecycle ─────────────────────────────────────────────────

    async def load(self):
        """Restore persisted clients from the database."""
        cur = await self.db.execute("SELECT * FROM clients ORDER BY created_at")
        for row in await cur.fetchall():
            name = row["name"]
            via = row["via"] if "via" in row.keys() else None
            s = RemoteSession(name=name, host=row["host"], port=row["port"],
                              user=row["user"], password=row["password"],
                              enabled=bool(row["enabled"]),
                              safe_rm=bool(row["safe_rm"]),
                              via=via)
            s.set_audit_callback(self._on_audit)
            s.set_tunnel_resolver(self._resolve_tunnel)
            self._sessions[name] = s

    async def close(self):
        for shell in list(self._terminals.values()):
            try:
                await shell.close()
            except Exception:
                pass
        self._terminals.clear()
        for s in self._sessions.values():
            await s.disconnect()
        self._sessions.clear()

    # ── Audit ─────────────────────────────────────────────────────

    async def _on_audit(self, client_name, command, result):
        await self.db.execute(
            """INSERT INTO audit_log (client_name, command, output,
               exit_code, cwd, duration_ms, success)
               VALUES (:c, :cmd, :out, :ec, :wd, :ms, :ok)""",
            dict(c=client_name, cmd=command,
                 out=result["output"],
                 ec=result["exit_code"], wd=result["cwd"],
                 ms=result["duration_ms"],
                 ok=1 if result["exit_code"] == 0 else 0),
        )
        await self.db.commit()

    async def audit_log(self, client_name, command, output, exit_code,
                        cwd, duration_ms, success):
        """Write a generic audit record — shared by exec() and data_transfer."""
        await self.db.execute(
            """INSERT INTO audit_log (client_name, command, output,
               exit_code, cwd, duration_ms, success)
               VALUES (:c, :cmd, :out, :ec, :wd, :ms, :ok)""",
            dict(c=client_name, cmd=command, out=output,
                 ec=exit_code, wd=cwd, ms=duration_ms,
                 ok=1 if success else 0),
        )
        await self.db.commit()

    # ── Clients ───────────────────────────────────────────────────

    async def add(self, name, host, user, password, port=22, enabled=True,
                  safe_rm=False, via=None):
        if name in self._sessions:
            raise ValueError(f"客户端 '{name}' 已存在。")
        if self._would_create_cycle(name, via):
            raise ValueError(f"无法添加 '{name}'：不能将 '{via}' 设为跳板，这会产生循环引用。")
        await self.db.execute(
            """INSERT INTO clients (name, host, port, "user", password, enabled, safe_rm, via)
               VALUES (:n, :h, :p, :u, :pw, :e, :sr, :via)""",
            dict(n=name, h=host, p=port, u=user, pw=password, e=int(enabled),
                 sr=int(safe_rm), via=via),
        )
        await self.db.commit()

        s = RemoteSession(name=name, host=host, port=port, user=user,
                          password=password, enabled=enabled, safe_rm=safe_rm,
                          via=via)
        s.set_audit_callback(self._on_audit)
        s.set_tunnel_resolver(self._resolve_tunnel)
        self._sessions[name] = s
        return self._sessions[name].to_dict()

    async def remove(self, name):
        if name not in self._sessions:
            raise KeyError(f"客户端 '{name}' 不存在。")

        # Block removal if other clients depend on this one as a jump host.
        dependents = [n for n, s in self._sessions.items()
                      if s._via == name]
        if dependents:
            raise ValueError(
                f"无法删除 '{name}'，以下客户端依赖它作为跳板: "
                f"{', '.join(dependents)}。"
                f"请先删除或修改这些客户端。"
            )

        await self.close_terminal(name)
        await self._sessions.pop(name).disconnect()
        await self.db.execute("DELETE FROM clients WHERE name=?", (name,))
        await self.db.commit()

    async def update(self, name, **fields):
        if name not in self._sessions:
            raise KeyError(f"客户端 '{name}' 不存在。")

        # Validate before persisting.
        allowed = {"host", "port", "user", "password", "enabled", "safe_rm", "via"}
        updates = {k: v for k, v in fields.items() if k in allowed}
        if "via" in updates and self._would_create_cycle(name, updates["via"]):
            raise ValueError(
                f"无法更新 '{name}'：不能将 '{updates['via']}' 设为跳板，"
                f"这会产生循环引用。"
            )
        if updates:
            updates["name"] = name
            cols = ", ".join(f"{k}=:{k}" for k in updates)
            await self.db.execute(
                f"UPDATE clients SET {cols}, updated_at=datetime('now') WHERE name=:name",
                updates,
            )
            await self.db.commit()

        # DB write succeeded — now safe to update in-memory state.
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
        if "via" in fields:
            s._via = fields["via"]

        return self._sessions[name].to_dict()

    def get(self, name):
        if name not in self._sessions:
            enabled = self.list_enabled()
            hint = ""
            if enabled:
                names = ", ".join(c["name"] for c in enabled)
                hint = f" 已启用的客户端: {names}。"
            raise KeyError(f"客户端 '{name}' 不存在。{hint}")
        return self._sessions[name]

    # ── Browser terminals ──────────────────────────────────────────

    async def get_or_create_terminal(self, name):
        """Return a live PersistentShell for the browser terminal.

        Each client gets at most one terminal shell, reused across WebSocket
        reconnects (so closing the terminal tab doesn't lose shell state).
        If the shell died it is transparently restarted.
        """
        session = self.get(name)
        shell = self._terminals.get(name)

        if shell is not None and not shell.alive:
            try:
                await shell.close()
            except Exception:
                pass
            shell = None

        if shell is None:
            shell = await session.open_terminal_shell()
            self._terminals[name] = shell

        return shell

    async def close_terminal(self, name):
        """Tear down the browser-terminal shell for a client, if any."""
        shell = self._terminals.pop(name, None)
        if shell is not None:
            try:
                await shell.close()
            except Exception:
                pass

    def list_all(self):
        return [self._sessions[n].to_dict() for n in self._sessions]

    def list_enabled(self):
        return [self._sessions[n].to_dict() for n, s in self._sessions.items() if s.enabled]

    # ── Audit queries ─────────────────────────────────────────────

    @staticmethod
    def _build_audit_where(client_name=None, after=None, before=None):
        """Build WHERE clause fragments and params for audit queries.

        Returns ``(clauses: list[str], params: list)``.
        """
        where = []
        params = []
        if client_name:
            where.append("client_name=?")
            params.append(client_name)
        after = ConnectionManager._iso_to_sqlite(after)
        before = ConnectionManager._iso_to_sqlite(before)
        if after:
            where.append("created_at>=?")
            params.append(after)
        if before:
            where.append("created_at<?")
            params.append(before)
        return where, params

    @staticmethod
    def _iso_to_sqlite(iso: str | None) -> str | None:
        """Normalize ISO 8601 to SQLite datetime format for string comparison.

        ``2026-06-12T11:30:00.123Z`` → ``2026-06-12 11:30:00``.
        """
        if not iso:
            return None
        s = iso.strip()
        if "T" in s:
            parts = s.split("T")
            s = parts[0] + " " + parts[1][:8]
        return s

    async def audit_list(self, client_name=None, after=None, before=None, limit=200, offset=0):
        where, params = self._build_audit_where(client_name, after, before)
        sql = "SELECT * FROM audit_log"
        if where:
            sql += " WHERE " + " AND ".join(where)
        sql += " ORDER BY created_at DESC LIMIT ? OFFSET ?"
        params.extend([limit, offset])
        cur = await self.db.execute(sql, params)
        return [dict(r) for r in await cur.fetchall()]

    async def audit_count(self, client_name=None, after=None, before=None):
        where, params = self._build_audit_where(client_name, after, before)
        sql = "SELECT COUNT(*) AS cnt FROM audit_log"
        if where:
            sql += " WHERE " + " AND ".join(where)
        cur = await self.db.execute(sql, params)
        return (await cur.fetchone())["cnt"]

    async def audit_delete(self, entry_id=None, entry_ids=None, client_name=None, before_id=None):
        if entry_id is not None:
            cur = await self.db.execute("DELETE FROM audit_log WHERE id=?", (entry_id,))
        elif entry_ids:
            placeholders = ",".join("?" * len(entry_ids))
            cur = await self.db.execute(
                f"DELETE FROM audit_log WHERE id IN ({placeholders})", entry_ids)
        elif client_name is not None:
            cur = await self.db.execute("DELETE FROM audit_log WHERE client_name=?", (client_name,))
        elif before_id is not None:
            cur = await self.db.execute("DELETE FROM audit_log WHERE id < ?", (before_id,))
        else:
            return 0
        await self.db.commit()
        return cur.rowcount

    # ── Internal ──────────────────────────────────────────────────

    async def _resolve_tunnel(self, name: str):
        """Resolve a tunnel connection for jump-host chaining.

        Returns the live ``SSHClientConnection`` for *name*, connecting it
        lazily if needed.  Ignores the ``enabled`` flag — the tunnel is
        infrastructure plumbing, not a user-facing endpoint.  ``enabled``
        only controls whether the host appears in ``list_remote_clients``
        and accepts direct commands.
        """
        import asyncssh

        session = self.get(name)
        async with session._connect_lock:
            if not session.connected:
                session._conn = await asyncssh.connect(
                    session._host, port=session._port, username=session._user,
                    password=session._password, client_keys=[], known_hosts=None,
                    keepalive_interval=30, keepalive_count_max=3,
                )
                session._cwd = "~"
        return session._conn

    def _would_create_cycle(self, name: str, via: str | None) -> bool:
        """Return True if setting *name*'s jump host to *via* creates a cycle.

        Only guards against single-hop loops: self-reference (A via A) and
        mutual pairs (A via B while B via A).  Deeper chains are not
        currently supported so they don't need to be checked.
        """
        if via is None:
            return False
        if via == name:
            return True  # self-reference
        # Check for mutual pair: A via B and B via A
        via_session = self._sessions.get(via)
        if via_session is not None and via_session._via == name:
            return True
        return False
