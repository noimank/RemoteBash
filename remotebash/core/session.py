"""RemoteSession — SSH connection with a persistent interactive shell.

Execution model
---------------
Commands run on a **single long-lived interactive shell with a PTY**, not a
fresh ``/bin/bash -c`` per call.  This makes the experience identical to a real
terminal session: ``cd``, ``export``, shell functions, aliases, ``umask`` and
history all persist across commands, and programs see a real terminal (so
``ls`` colouring, ``isatty()`` checks and interactive prompts behave normally).

The machinery lives in :class:`remotebash.core.persistent_shell.PersistentShell`:
it allocates a PTY, runs commands in a long-lived bash process, and appends a
private completion sentinel after each MCP command to capture output + exit
code + new CWD. ANSI colour codes are stripped so the MCP caller gets clean
text.

Output preservation
-------------------
* **output** is the merged PTY output, ANSI-stripped and decoded.
* **exit_code** and **cwd** come from the completion sentinel (exact, even for
  builtins like ``cd``).
"""

import asyncio
import logging
import time

import asyncssh

from .persistent_shell import PersistentShell, _TERMINAL_PS1

logger = logging.getLogger(__name__)

# Bash function that shadows rm(1) with a trash-to-/tmp safe delete.
#
# Each invocation creates its own subdirectory under /tmp/.rbsh_trash/
# named <epoch>_<pid> so deletions are grouped and timestamped.
#
# Three-tier per-file strategy:
#   1. ``mv -- $_a $_t/``           — fast, atomic, handles most cases
#   2. ``cp -r && command rm -r``   — cross-device safe: data lands in
#      trash BEFORE the original is removed
#   3. ``command rm $_o -- $_a``    — last resort (only when trash is
#      unwritable or both mv and cp fail)
#
# Flags are collected into ``$_o``.  When ``--`` is encountered it is
# appended to ``$_o`` so the fallback ``command rm`` also stops option
# parsing at the right point.
#
# Use ``command rm`` or ``/bin/rm`` to bypass the shim.
_SAFE_RM_SHIM = (
    "rm(){"
    " _t=\"/tmp/.rbsh_trash/$(date +%s)_$$\"&&mkdir -p \"$_t\" 2>/dev/null;"
    " _o=\"\";_n=0;_e=false;_f=0;"
    " for _a;do"
    "  $_e && {"
    "   _n=1;mv -- \"$_a\" \"$_t/\" 2>/dev/null||"
    "   { cp -r -- \"$_a\" \"$_t/\" 2>/dev/null&&command rm -r -- \"$_a\" 2>/dev/null;}||"
    "   { command rm $_o -- \"$_a\" 2>/dev/null||_f=1;};"
    "   continue;"
    "  };"
    "  case \"$_a\" in"
    "   --) _e=true;_o=\"$_o --\";;"
    "   -*) _o=\"$_o $_a\";;"
    "   *) _n=1;mv -- \"$_a\" \"$_t/\" 2>/dev/null||"
    "       { cp -r -- \"$_a\" \"$_t/\" 2>/dev/null&&command rm -r -- \"$_a\" 2>/dev/null;}||"
    "       { command rm $_o -- \"$_a\" 2>/dev/null||_f=1;};"
    "  esac;"
    " done;"
    " [ \"$_n\" -eq 0 ]&&command rm $_o 2>/dev/null;"
    " return $_f;"
    "}; "
)


class RemoteSession:

    def __init__(self, name, host, port, user, password, enabled=True,
                 safe_rm=False, via=None):
        self.name = name
        self._host = host
        self._port = port
        self._user = user
        self._password = password
        self.enabled = enabled
        self.safe_rm = safe_rm
        self._via = via                  # jump host client name, None = direct

        self._conn = None
        self._shell = None              # PersistentShell — lazily started
        self._shell_lock = asyncio.Lock()  # guards _ensure_shell() against races
        self._exec_lock = asyncio.Lock()   # serialises concurrent exec() callers
        self._connect_lock = asyncio.Lock()  # guards connect() against TOCTOU races
        self._cwd = "~"
        self._audit_cb = None
        self._tunnel_resolver = None     # callable: name → SSHClientConnection

    @property
    def connected(self):
        return self._conn is not None and not self._conn.is_closed()

    @property
    def host(self):
        return self._host

    @host.setter
    def host(self, val):
        self._host = val

    @property
    def port(self):
        return self._port

    @port.setter
    def port(self, val):
        self._port = int(val)

    @property
    def user(self):
        return self._user

    @user.setter
    def user(self, val):
        self._user = val

    @property
    def password(self):
        return self._password

    @password.setter
    def password(self, val):
        self._password = val

    def set_audit_callback(self, cb):
        self._audit_cb = cb

    def set_tunnel_resolver(self, resolver):
        """Set a callable ``name → SSHClientConnection`` for jump-host support.

        Called by the manager so ``connect()`` can resolve a jump host's live
        connection at connect time.
        """
        self._tunnel_resolver = resolver

    async def connect(self):
        if not self.enabled:
            raise RuntimeError(f"Client '{self.name}' is disabled.")
        async with self._connect_lock:
            if self.connected:
                return
            kwargs: dict = dict(
                host=self._host, port=self._port, username=self._user,
                password=self._password, client_keys=[], known_hosts=None,
                keepalive_interval=30, keepalive_count_max=3,
            )
            if self._via:
                if self._tunnel_resolver is None:
                    raise RuntimeError(
                        f"Client '{self.name}' requires jump host '{self._via}' "
                        "but no tunnel resolver is configured."
                    )
                via_conn = await self._tunnel_resolver(self._via)
                kwargs["tunnel"] = via_conn
                logger.info("Connecting to %s:%d via jump host '%s'",
                            self._host, self._port, self._via)
            self._conn = await asyncssh.connect(**kwargs)
            self._cwd = "~"

    async def disconnect(self):
        if self._shell is not None:
            try:
                await self._shell.close()
            except Exception:
                pass
            self._shell = None
        if self._conn:
            conn = self._conn
            self._conn = None
            try:
                conn.close()
                await asyncio.wait_for(conn.wait_closed(), timeout=5)
            except Exception:
                pass

    async def _expand_remote_path(self, path):
        """Resolve ``~`` in a remote path.

        SFTP does not expand ``~`` and asyncssh ``run()`` does not invoke a
        shell.  We fetch ``$HOME`` and replace the leading ``~`` ourselves.
        """
        if "~" not in path:
            return path
        result = await self._conn.run("echo $HOME", timeout=5)
        home = (result.stdout or "").strip()
        if home:
            if path == "~":
                return home
            if path.startswith("~/"):
                return home + path[1:]
        return path  # includes ~user/ fallback — leave as-is

    async def transfer(self, src, dst, direction):
        """Transfer a file via SFTP.

        ``direction``: ``remote2local`` (download) or ``local2remote`` (upload).
        Remote paths may use ``~`` which will be shell-expanded.

        Returns ``{success, direction, src, dst, size_bytes}``.
        """
        if not self.enabled:
            raise RuntimeError(f"Client '{self.name}' is disabled.")

        # transfer uses the raw SFTP channel, not the interactive shell.
        await self.connect()

        # Resolve ~ in the remote-side path (SFTP doesn't expand it).
        if direction == "remote2local":
            src = await self._expand_remote_path(src)
        else:
            dst = await self._expand_remote_path(dst)

        t0 = time.monotonic()
        try:
            async with self._conn.start_sftp_client() as sftp:
                if direction == "remote2local":
                    await sftp.get(src, dst)
                elif direction == "local2remote":
                    await sftp.put(src, dst)
                else:
                    raise ValueError(
                        f"Invalid direction '{direction}'. "
                        "Expected 'remote2local' or 'local2remote'."
                    )
                # Best-effort: stat the remote file for size reporting.
                # The transfer itself succeeded regardless of whether stat
                # works (permissions, race with deletion, etc.).
                size = 0
                try:
                    st = await sftp.stat(dst if direction == "local2remote" else src)
                    size = st.size
                except (asyncssh.Error, OSError):
                    pass
        except (asyncssh.Error, OSError, TimeoutError) as exc:
            await self.disconnect()
            raise RuntimeError(f"SFTP transfer failed: {exc}") from exc

        elapsed = int((time.monotonic() - t0) * 1000)
        logger.info("Transfer %s -> %s (%s) done in %d ms, %d bytes",
                    src, dst, direction, elapsed, size)
        return {
            "success": True,
            "direction": direction,
            "src": src,
            "dst": dst,
            "size_bytes": size,
            "duration_ms": elapsed,
        }

    async def test_connection(self):
        conn = await asyncssh.connect(
            self._host, port=self._port, username=self._user,
            password=self._password, client_keys=[], known_hosts=None,
            connect_timeout=3,
        )
        conn.close()
        await conn.wait_closed()

    async def _ensure_shell(self) -> PersistentShell:
        """Return a live PersistentShell, (re)starting it if needed.

        The shell is started lazily on first use and rebuilt if the
        underlying process died.
        ``safe_rm`` is honoured at shell-start time: the shim is injected
        into the persistent shell, so it shadows ``rm`` for every command.

        An ``asyncio.Lock`` serialises concurrent callers so two
        simultaneous ``exec()`` invocations cannot create duplicate shells.

        When ``safe_rm`` was toggled via the dashboard while the shell is
        alive, the existing shell is torn down and re-created so the
        setting takes effect immediately.
        """
        await self.connect()
        async with self._shell_lock:
            # Rebuild if the shell died, or if safe_rm was toggled while
            # the shell was running (the shim is baked-in at start time,
            # so a live shell can't pick up the change on the fly).
            if self._shell is not None and self._shell.alive and self._shell._safe_rm == self.safe_rm:
                return self._shell
            if self._shell is not None:
                try:
                    await self._shell.close()
                except Exception:
                    pass
                self._shell = None
            self._shell = await PersistentShell(
                self._conn, safe_rm=self.safe_rm,
                init_script=_SAFE_RM_SHIM if self.safe_rm else "",
            ).start()
            self._cwd = "~"
            return self._shell

    async def exec(self, command, timeout=30):
        """Run ``command`` on the persistent interactive shell.

        Returns ``{output, exit_code, cwd, duration_ms}``.

        The persistent shell can only execute one command at a time, so
        concurrent ``exec()`` callers are serialised: the second one queues on
        ``_exec_lock`` and runs once the first finishes, instead of racing on
        the shell and failing.  Execution order follows arrival order.
        """
        if not self.enabled:
            raise RuntimeError(f"Client '{self.name}' is disabled.")

        async with self._exec_lock:
            # _ensure_shell() stays INSIDE the lock: a queued caller must
            # re-check the shell after waiting, since the prior command may
            # have torn it down (timeout / SSH error) while it was queued.
            try:
                shell = await self._ensure_shell()
            except (asyncssh.Error, OSError, RuntimeError) as exc:
                await self.disconnect()
                raise RuntimeError(f"SSH shell setup failed: {exc}") from exc

            t0 = time.monotonic()
            try:
                r = await shell.run(command, timeout=timeout)
            except (asyncssh.Error, OSError) as exc:
                elapsed = int((time.monotonic() - t0) * 1000)
                if self._audit_cb:
                    await self._audit_cb(self.name, command, {
                        "output": f"SSH command failed: {exc}",
                        "exit_code": -1,
                        "cwd": self._cwd,
                        "duration_ms": elapsed,
                    })
                await self.disconnect()
                raise RuntimeError(f"SSH command failed: {exc}") from exc
            except RuntimeError as exc:
                elapsed = int((time.monotonic() - t0) * 1000)
                if self._audit_cb:
                    await self._audit_cb(self.name, command, {
                        "output": str(exc),
                        "exit_code": -1,
                        "cwd": self._cwd,
                        "duration_ms": elapsed,
                    })
                raise

            self._cwd = r.get("cwd", self._cwd)
            if self._audit_cb:
                await self._audit_cb(self.name, command, r)
            return r

    async def open_terminal_shell(self) -> PersistentShell:
        """Open a **separate** PersistentShell for the browser terminal.

        This is independent of the MCP ``exec`` shell (``self._shell``): the
        browser terminal is free-form interaction, while exec() is strict
        one-command-per-call framing.  Sharing one PTY between both would
        corrupt the framing, so each consumption mode owns its own shell.
        Both reuse the same underlying SSH connection.

        The terminal shell uses a human-readable PS1. The MCP shell leaves the
        user's prompt alone and frames command completion with a private
        sentinel appended to each command.
        """
        if not self.enabled:
            raise RuntimeError(f"Client '{self.name}' is disabled.")
        await self.connect()
        # Terminal shells are managed by the caller (ConnectionManager); we
        # only construct + start one here on the shared connection.
        return await PersistentShell(
            self._conn, safe_rm=self.safe_rm,
            init_script=_SAFE_RM_SHIM if self.safe_rm else "",
            prompt_template=_TERMINAL_PS1,
        ).start()

    def to_dict(self):
        return {
            "name": self.name, "host": self._host, "port": self._port,
            "user": self._user, "connected": self.connected,
            "cwd": self._cwd, "enabled": self.enabled, "safe_rm": self.safe_rm,
            "via": self._via,
        }
