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

import asyncio
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
    " _o=\"\";_n=0;_e=false;_f=0;"
    " for _a;do"
    "  $_e && {"
    "   _n=1;mv \"$_a\" \"$_t/\" 2>/dev/null||"
    "    { command rm $_o \"$_a\"&&echo \"rm: removed $_a (trash failed)\">&2;}||_f=1;"
    "   continue;"
    "  };"
    "  case \"$_a\" in"
    "   --) _e=true;;"
    "   -*) _o=\"$_o $_a\";;"
    "   *) _n=1;mv \"$_a\" \"$_t/\" 2>/dev/null||"
    "       { command rm $_o \"$_a\"&&echo \"rm: removed $_a (trash failed)\">&2;}||_f=1;"
    "  esac;"
    " done;"
    " [ \"$_n\" -eq 0 ]&&command rm $_o 2>/dev/null;"
    " return $_f;"
    "}; "
)


_RUNNER_SCRIPT = (
    "__rbsh_token=$1;"
    "__rbsh_cid=$2;"
    '__rbsh_cwd_arg=$(printf "%b" "$3");'
    "__rbsh_safe_rm=$4;"
    '__rbsh_command=$(printf "%b" "$5");'
    "__rbsh_done=0;"
    "__rbsh_track_cwd=0;"
    "__rbsh_emit(){"
    " __rbsh_ec=$1;"
    ' [ "$__rbsh_done" = 1 ]&&return;'
    " __rbsh_done=1;"
    ' if [ "$__rbsh_track_cwd" = 1 ];then __rbsh_cwd=$(pwd);else __rbsh_cwd=;fi;'
    " printf '%s:%s:EC:%s:CWD:%s\\n'"
    ' "$__rbsh_token" "$__rbsh_cid" "$__rbsh_ec" "$__rbsh_cwd" >&2;'
    "};"
    "trap '__rbsh_emit $?' EXIT;"
    'if [ "$__rbsh_cwd_arg" = "~" ];then cd 2>/dev/null;else cd "$__rbsh_cwd_arg" 2>/dev/null;fi;'
    "__rbsh_ec=$?;"
    'if [ "$__rbsh_ec" -ne 0 ];then __rbsh_emit "$__rbsh_ec";exit "$__rbsh_ec";fi;'
    "__rbsh_track_cwd=1;"
    'if [ "$__rbsh_safe_rm" = 1 ];then '
    f"{_SAFE_RM_SHIM}"
    "fi;"
    'eval "$__rbsh_command";'
    "__rbsh_ec=$?;"
    '__rbsh_emit "$__rbsh_ec";'
    'exit "$__rbsh_ec"'
)


_BOOTSTRAP_SCRIPT = (
    "__rbsh_bash=$(command -v bash 2>/dev/null); "
    'if [ -n "$__rbsh_bash" ]; then '
    'exec "$__rbsh_bash" -c "$1" rbsh "$2" "$3" "$4" "$5" "$6"; '
    'else exec /bin/sh -c "$1" rbsh "$2" "$3" "$4" "$5" "$6"; fi'
)


def _build_remote_command(command, cwd, cid, safe_rm=False):
    """Build the SSH exec command for one remote shell execution."""
    if "\x00" in command or "\x00" in cwd:
        raise ValueError("Remote commands and CWD paths cannot contain NUL bytes.")

    encoded_cwd = _encode_shell_bytes(cwd)
    encoded_command = _encode_shell_bytes(command)
    args = [
        "/bin/sh",
        "-c",
        _BOOTSTRAP_SCRIPT,
        "rbsh",
        _RUNNER_SCRIPT,
        _TOKEN,
        cid,
        encoded_cwd,
        "1" if safe_rm else "0",
        encoded_command,
    ]
    return " ".join(shlex.quote(arg) for arg in args)


def _encode_shell_bytes(value):
    """Encode text as a single-line POSIX printf %b payload."""
    return "".join(f"\\{byte:03o}" for byte in value.encode())


def _parse_exec_output(name, cid, stdout, stderr, fallback_exit_code, cwd):
    """Strip the metadata anchor from stderr and return execution metadata."""
    exit_code = fallback_exit_code
    new_cwd = cwd
    anchor = f"{_TOKEN}:{cid}:EC:"
    idx = stderr.rfind(anchor)

    if idx < 0:
        logger.warning("CWD tracking anchor not found for '%s' — "
                       "cwd may be stale (command used exec, was killed, "
                       "or replaced the EXIT trap before exiting)", name)
        return stdout, stderr, exit_code, new_cwd

    line_end = stderr.find("\n", idx)
    if line_end < 0:
        marker = stderr[idx:]
        suffix = ""
    else:
        marker = stderr[idx:line_end]
        suffix = stderr[line_end + 1:]

    marker_body = marker[len(anchor):]
    m = re.match(r'(-?\d+):CWD:(.*)\Z', marker_body)
    if not m:
        logger.warning("Malformed CWD tracking anchor for '%s'", name)
        return stdout, stderr, exit_code, new_cwd

    stderr = stderr[:idx] + suffix
    exit_code = int(m.group(1))
    marker_cwd = m.group(2)
    if marker_cwd != "":
        new_cwd = marker_cwd

    return stdout, stderr, exit_code, new_cwd


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
            connect_timeout=3,
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
        wrapped = _build_remote_command(command, self._cwd, cid, self.safe_rm)

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
        fallback_exit_code = result.returncode if result.returncode is not None else -1
        stdout, stderr, exit_code, self._cwd = _parse_exec_output(
            self.name, cid, stdout, stderr, fallback_exit_code, self._cwd
        )

        r = {
            "stdout": stdout,
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
