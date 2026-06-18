"""TCP relay through a jump host when SSH TCP forwarding is disabled.

When a jump host has ``AllowTcpForwarding no`` in its sshd_config,
asyncssh's ``tunnel=connection`` (which uses ``direct-tcpip`` channels)
is rejected with ``ChannelOpenError(code=1)``.

This module provides :class:`SocatTunnelRelay`, which falls back to
running a TCP relay process (``socat``, ``nc``, ``ncat``, or a small
Python 3 script) on the jump host and bridging its stdio to a local
``socket.socketpair``.  The resulting socket can be passed to
``asyncssh.connect(sock=...)`` for a connection that behaves identically
to a direct TCP one.
"""

import asyncio
import logging
import socket

import asyncssh

logger = logging.getLogger(__name__)

# Python 3 inline relay script used when socat / nc / ncat are unavailable.
# Uses select() for bidirectional relay — only runs on Linux/macOS jump
# hosts where select supports non-socket FDs.
_PY3_RELAY_SCRIPT = (
    "import socket,select,sys,os;"
    "s=socket.socket();"
    "s.connect(('{host}',{port}));"
    "si=sys.stdin.buffer;so=sys.stdout.buffer;"
    "sif=sys.stdin.fileno();sf=s.fileno();"
    "while 1:"
    " r,_,_=select.select([sif,sf],[],[]);"
    " if sif in r:"
    "  d=os.read(sif,4096);"
    "  if not d:break;"
    "  s.sendall(d);"
    " if sf in r:"
    "  d=s.recv(4096);"
    "  if not d:break;"
    "  so.write(d);so.flush();"
    "s.close()"
)

# Commands tried in order.  The first one whose create_process succeeds wins.
# Stderr is redirected to /dev/null on every command so a warning or error
# message can never fill the pipe buffer and block the relay.
_RELAY_CANDIDATES = [
    "socat STDIO TCP:{host}:{port},connect-timeout=10 2>/dev/null",
    "nc {host} {port} 2>/dev/null",
    "ncat {host} {port} 2>/dev/null",
    "python3 -c '" + _PY3_RELAY_SCRIPT + "' 2>/dev/null",
]

_READ_CHUNK = 65536


class TunnelRelayError(RuntimeError):
    """Raised when no relay tool (socat, nc, ncat, python3) is available."""


class SocatTunnelRelay:
    """Bridges a local socketpair through a jump-host TCP relay process.

    When the jump host's SSH server prohibits TCP forwarding, this opens
    a process (e.g. ``socat STDIO TCP:host:port``) on the jump host and
    relays bytes between its stdio and one end of a :func:`socket.socketpair`.
    The other socket end is returned by :meth:`connect` and can be passed
    to ``asyncssh.connect(sock=...)``.
    """

    def __init__(self, jump_host_conn, target_host, target_port=22):
        self._jh_conn = jump_host_conn          # SSHClientConnection
        self._target_host = target_host
        self._target_port = target_port

        self._proc = None                       # SSHClientProcess
        self._sock_b = None                     # bridge-side socket
        self._sock_a = None                     # caller-side socket
        self._tasks: list[asyncio.Task] = []
        self._closed = False

    async def connect(self) -> socket.socket:
        """Start the relay and return a socket for ``asyncssh.connect(sock=…)``.

        The returned socket is one end of a socketpair; the other end is
        bridged to the relay process running on the jump host.
        """
        if self._closed:
            raise RuntimeError("Relay is already closed")

        # ── Create the socket pair ──────────────────────────────
        sock_a, sock_b = socket.socketpair()
        sock_a.setblocking(False)
        sock_b.setblocking(False)
        self._sock_a = sock_a
        self._sock_b = sock_b

        # ── Start a relay process on the jump host ──────────────
        last_exc = None
        for cmd_template in _RELAY_CANDIDATES:
            cmd = cmd_template.format(host=self._target_host,
                                      port=self._target_port)
            try:
                # encoding=None → raw bytes, no PTY (pipes are cleaner for relay)
                self._proc = await self._jh_conn.create_process(
                    cmd, encoding=None)
                logger.debug("Relay process started on jump host: %s", cmd)
                break
            except (asyncssh.Error, OSError) as exc:
                last_exc = exc
                logger.debug("Relay tool failed (%s): %s",
                             cmd_template.split()[0], exc)
                continue

        if self._proc is None:
            self._cleanup_sockets()
            raise TunnelRelayError(
                f"No relay tool available on jump host. "
                f"Tried: socat, nc, ncat, python3. "
                f"Last error: {last_exc}"
            )

        # ── Start bidirectional bridge tasks ────────────────────
        self._tasks = [
            asyncio.create_task(self._bridge_stdout()),
            asyncio.create_task(self._bridge_stdin()),
        ]

        return sock_a

    async def close(self):
        """Tear down the relay — cancel tasks, close sockets, close process."""
        if self._closed:
            return
        self._closed = True

        for t in self._tasks:
            if not t.done():
                t.cancel()
        if self._tasks:
            await asyncio.gather(*self._tasks, return_exceptions=True)
        self._tasks.clear()

        self._cleanup_sockets()

        if self._proc is not None:
            try:
                self._proc.close()
            except Exception:
                pass
            try:
                await self._proc.wait_closed()
            except Exception:
                pass
            self._proc = None

    # ── Internal bridge coroutines ──────────────────────────────

    async def _bridge_stdout(self):
        """Relay: process stdout → socket_b (target → inner SSH)."""
        loop = asyncio.get_running_loop()
        try:
            while not self._proc.is_closing():
                data = await self._proc.stdout.read(_READ_CHUNK)
                if not data:  # EOF from relay process
                    break
                await loop.sock_sendall(self._sock_b, data)
        except (asyncssh.Error, OSError):
            pass
        except asyncio.CancelledError:
            pass
        finally:
            # Signal EOF to the inner SSH connection
            try:
                self._sock_b.shutdown(socket.SHUT_WR)
            except OSError:
                pass

    async def _bridge_stdin(self):
        """Relay: socket_b → process stdin (inner SSH → target)."""
        loop = asyncio.get_running_loop()
        try:
            while True:
                data = await loop.sock_recv(self._sock_b, _READ_CHUNK)
                if not data:  # EOF from inner SSH
                    break
                self._proc.stdin.write(data)
                await self._proc.stdin.drain()
        except (asyncssh.Error, OSError):
            pass
        except asyncio.CancelledError:
            pass

    def _cleanup_sockets(self):
        """Close both ends of the socketpair, ignoring errors."""
        for attr in ("_sock_a", "_sock_b"):
            sock = getattr(self, attr, None)
            if sock is not None:
                try:
                    sock.close()
                except OSError:
                    pass
                setattr(self, attr, None)
