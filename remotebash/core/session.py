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

import re
import shlex
import time
import uuid

import asyncssh

IDLE_TIMEOUT = 3600

# Unique token that is vanishingly unlikely to collide with real stderr
_TOKEN = "__RBSH__"


class RemoteSession:

    def __init__(self, name, host, port, user, password, enabled=True):
        self.name = name
        self._host = host
        self._port = port
        self._user = user
        self._password = password
        self.enabled = enabled

        self._conn = None
        self._cwd = "~"
        self._audit_cb = None
        self._last_activity = 0.0

    @property
    def connected(self):
        return self._conn is not None and not self._conn.is_closed()

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

        # Shell wrapper (no subshell — CWD changes must persist):
        #   cd <cwd> 2>/dev/null && {command}; echo <TOKEN>:<cid>:EC:$?:CWD:$PWD >&2
        #
        # stdout → byte-for-byte command output.  Metadata → final stderr line.
        wrapped = (
            f"cd {cd} 2>/dev/null && {command}; "
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
            "cwd": self._cwd, "enabled": self.enabled, "label": "",
        }
