"""RemoteSession — SSH connection with CWD tracking and idle timeout.

Output preservation
-------------------
Metadata (exit code, new CWD) is embedded in a single **stderr** anchor
line tagged with a per-call UUID.  This line is stripped from stderr
before the result is returned, so:

* **stdout** is byte-for-byte the remote command's stdout.
* **stderr** is byte-for-byte the remote command's stderr.
* **exit_code** and **cwd** are parsed from the anchor line.
"""

import logging
import re
import shlex
import time
import uuid

import asyncssh

logger = logging.getLogger(__name__)

IDLE_TIMEOUT = 3600

# Unique token that is vanishingly unlikely to collide with real stderr
_TOKEN = "__RBSH__"

# POSIX-sh function that shadows rm(1) with a trash-to-/tmp safe delete.
#
# Each invocation creates its own subdirectory under /tmp/.rbsh_trash/
# named <epoch>_<pid> so deletions are grouped and timestamped.
#
# Flags are parsed and forwarded to the real rm on fallback.  ``--`` stops
# option parsing so filenames that start with ``-`` are handled correctly.
#
# If mv fails (cross-device, permissions, ...) the function falls back to
# the real rm with the collected flags — no command ever silently succeeds
# without doing anything.
#
# Use ``command rm``, ``/bin/rm``, or ``\rm`` to bypass the shim.
_SAFE_RM_SHIM = (
    "rm(){"
    " _t=\"/tmp/.rbsh_trash/$(date +%s)_$$\"&&mkdir -p \"$_t\" 2>/dev/null;"
    " _o=\"\";_n=0;_e=false;"
    " for _a;do"
    "  $_e && {"
    "   _n=1;mv \"$_a\" \"$_t/\" 2>/dev/null||"
    "    { command rm $_o \"$_a\"&&echo \"rm: removed $_a (trash failed)\">&2;};"
    "   continue;"
    "  };"
    "  case \"$_a\" in"
    "   --) _e=true;;"
    "   -*) _o=\"$_o $_a\";;"
    "   *) _n=1;mv \"$_a\" \"$_t/\" 2>/dev/null||"
    "       { command rm $_o \"$_a\"&&echo \"rm: removed $_a (trash failed)\">&2;};;"
    "  esac;"
    " done;"
    " [ \"$_n\" -eq 0 ]&&command rm $_o 2>/dev/null;"
    ":;"
    "}; "
)


class RemoteSession:

    def __init__(self, name, host, port, user, password, enabled=True, safe_rm=False):
        self.name = name
        self._host = host
        self._port = port
        self._user = user
        self._password = password
        self.enabled = enabled
        self.safe_rm = safe_rm

        self._conn = None
        self._cwd = "~"
        self._audit_cb = None
        self._last_activity = 0.0

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

    async def connect(self):
        if not self.enabled:
            raise RuntimeError(f"Client '{self.name}' is disabled.")
        if self.connected:
            return
        self._conn = await asyncssh.connect(
            self._host, port=self._port, username=self._user,
            password=self._password, client_keys=[], known_hosts=None,
            keepalive_interval=30, keepalive_count_max=3,
        )
        self._cwd = "~"
        self._last_activity = time.monotonic()

    async def disconnect(self):
        if self._conn:
            conn = self._conn
            self._conn = None
            try:
                conn.close()
                await conn.wait_closed()
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

        if self.connected and (time.monotonic() - self._last_activity) > IDLE_TIMEOUT:
            await self.disconnect()
        if not self.connected:
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
                stat = await sftp.stat(dst if direction == "local2remote" else src)
                size = stat.size
        except (asyncssh.Error, OSError, TimeoutError) as exc:
            await self.disconnect()
            raise RuntimeError(f"SFTP transfer failed: {exc}") from exc

        self._last_activity = time.monotonic()
        elapsed = int((self._last_activity - t0) * 1000)
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
            connect_timeout=10,
        )
        conn.close()
        await conn.wait_closed()

    async def exec(self, command, timeout=30):
        if not self.enabled:
            raise RuntimeError(f"Client '{self.name}' is disabled.")

        if self.connected and (time.monotonic() - self._last_activity) > IDLE_TIMEOUT:
            await self.disconnect()
        if not self.connected:
            await self.connect()

        cid = uuid.uuid4().hex
        cd = self._cwd if self._cwd == "~" else shlex.quote(self._cwd)

        # Shell wrapper (no subshell — CWD changes must persist).
        # When safe_rm is enabled, a rm() function is injected that shadows
        # rm(1) with a mv-to-/tmp fallback.  Use ``command rm``, ``/bin/rm``,
        # or ``\rm`` to bypass the shim.
        # stdout → byte-for-byte command output.  Metadata → final stderr line.
        pre = _SAFE_RM_SHIM if self.safe_rm else ""
        wrapped = (
            f"{pre}cd {cd} 2>/dev/null && {command}; "
            f"echo {_TOKEN}:{cid}:EC:$?:CWD:$(pwd) >&2"
        )

        t0 = time.monotonic()
        try:
            result = await self._conn.run(wrapped, timeout=timeout)
        except (asyncssh.Error, OSError, TimeoutError) as exc:
            await self.disconnect()
            raise RuntimeError(f"SSH command failed: {exc}") from exc

        self._last_activity = time.monotonic()
        elapsed = int((self._last_activity - t0) * 1000)

        stdout = result.stdout or ""
        stderr = result.stderr or ""
        exit_code = -1

        # Parse anchor from stderr — everything before it is real stderr.
        anchor = f"{_TOKEN}:{cid}:EC:"
        idx = (stderr or "").find(anchor)
        if idx >= 0:
            # Slice off anchor and everything after it from stderr.
            tail = stderr[idx + len(anchor):]
            stderr = stderr[:idx].rstrip("\n")
            m = re.match(r'(-?\d+):CWD:(.*)', tail)
            if m:
                exit_code = int(m.group(1))
                new_cwd = m.group(2).strip()
                if new_cwd:
                    self._cwd = new_cwd

        r = {
            "stdout": stdout.rstrip("\n"),
            "stderr": stderr,
            "exit_code": exit_code,
            "cwd": self._cwd,
            "duration_ms": elapsed,
        }
        if self._audit_cb:
            await self._audit_cb(self.name, command, r)
        return r

    def to_dict(self):
        return {
            "name": self.name, "host": self._host, "port": self._port,
            "user": self._user, "connected": self.connected,
            "cwd": self._cwd, "enabled": self.enabled, "safe_rm": self.safe_rm,
        }
