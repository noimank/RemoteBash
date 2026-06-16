"""Tests for PersistentShell — driven by a fake asyncssh process (no network)."""

import asyncio
import re
import unittest

from remotebash.core.persistent_shell import (
    PersistentShell,
    strip_ansi,
)


class _FakeReaderWriter:
    """Minimal async reader/writer duck-type for tests.

    ``SSHReader`` exposes ``read(n)`` (returns up to n bytes, b'' at EOF);
    ``SSHWriter`` exposes ``write(data)`` + ``drain()``.  Tests push bytes in
    via :meth:`feed` and capture writes via :attr:`written`.
    """

    def __init__(self):
        self._buf = bytearray()
        self._eof = False
        self.written = bytearray()

    # ── reader side ───────────────────────────────────────────────
    def feed(self, data: bytes):
        self._buf.extend(data)

    def feed_eof(self):
        self._eof = True

    def at_eof(self):
        return self._eof and not self._buf

    async def read(self, n: int = -1):
        while not self._buf:
            if self._eof:
                return b""
            await asyncio.sleep(0.005)
        if n is None or n < 0:
            data, self._buf = bytes(self._buf), bytearray()
            return data
        chunk = bytes(self._buf[:n])
        del self._buf[:n]
        return chunk

    # ── writer side ───────────────────────────────────────────────
    def write(self, data: bytes):
        self.written.extend(data)

    async def drain(self):
        await asyncio.sleep(0)

    def close(self):
        self._eof = True


class _FakeProcess:
    """Stand-in for ``asyncssh.SSHClientProcess`` used by tests.

    Mirrors the subset of the real API that :class:`PersistentShell` touches:
    ``stdin``, ``stdout``, ``is_closing``, ``change_terminal_size``,
    ``exit_status``, ``close``, ``wait_closed``.
    """

    def __init__(self):
        self.stdin = _FakeReaderWriter()
        self.stdout = _FakeReaderWriter()
        self._closing = False
        self._exit_status = None
        self.resizes = []

    @property
    def exit_status(self):
        return self._exit_status

    def is_closing(self):
        return self._closing

    def change_terminal_size(self, width, height, pixwidth=0, pixheight=0):
        self.resizes.append((width, height))

    def close(self):
        self._closing = True

    async def wait_closed(self):
        await asyncio.sleep(0)


class _FakeConn:
    def __init__(self, proc):
        self.proc = proc
        self.calls = []

    async def create_process(self, **kwargs):
        self.calls.append(kwargs)
        return self.proc


def _run(coro):
    """Run a coroutine on a fresh loop and close the loop afterwards.

    Pending tasks (each shell's reader loop, blocked on stdout.read) are
    cancelled before the loop closes so we don't leak "Task was destroyed".
    """
    loop = asyncio.new_event_loop()
    try:
        result = loop.run_until_complete(coro)
        # Cancel any lingering background tasks the coroutine spun up.
        _cancel_all_tasks(loop)
        return result
    finally:
        loop.close()


def _cancel_all_tasks(loop):
    to_cancel = [t for t in asyncio.all_tasks(loop) if not t.done()]
    for t in to_cancel:
        t.cancel()
    for t in to_cancel:
        try:
            loop.run_until_complete(t)
        except (asyncio.CancelledError, Exception):
            pass


_TOKEN_RE = re.compile(rb"__RBSH_START__:([0-9a-f]+)__")


def _latest_done_token(proc):
    matches = _TOKEN_RE.findall(bytes(proc.stdin.written))
    if not matches:
        raise AssertionError("No __RBSH_START__ token was written to stdin")
    return matches[-1].decode()


def _feed_done(proc, exit_code, cwd, output=b"", before=b""):
    """Push `<before><start-frame><output><done-frame>` into fake stdout."""
    token = _latest_done_token(proc)
    proc.stdout.feed(
        before
        + f"__RBSH_START__:{token}__\r\n".encode()
        + output
        + f"__RBSH_DONE__:{token}:{exit_code}:CWD:{cwd}__\r\n".encode()
    )


async def _make_started_shell(proc):
    """Build a fake-backed PersistentShell and wait for its init frame."""
    shell = PersistentShell(process_factory=lambda: proc)
    start_task = asyncio.ensure_future(shell.start())
    await asyncio.sleep(0.02)
    _feed_done(proc, 0, "/root")
    await start_task
    return shell


class StripAnsiTest(unittest.TestCase):

    def test_strips_colour_codes(self):
        colored = b"\x1b[32mgreen\x1b[0m \x1b[1;31mred\x1b[0m"
        self.assertEqual(strip_ansi(colored), b"green red")

    def test_strips_cursor_and_clear(self):
        seq = b"abc\x1b[2K\x1b[1;1Hdef\x1b[0J"
        self.assertEqual(strip_ansi(seq), b"abcdef")

    def test_leaves_plain_text(self):
        self.assertEqual(strip_ansi(b"plain text"), b"plain text")

    def test_strips_osc_title(self):
        # xterm title / hyperlink sequences
        seq = b"\x1b]0;title\x07hello\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\"
        self.assertEqual(strip_ansi(seq), b"hellolink")


class PersistentShellRunTest(unittest.TestCase):

    def test_start_consumes_init_done_frame(self):
        # Verify start() returns once the init sentinel is consumed.  We run
        # in-loop so we can inspect the shell's state before it's closed.
        loop = asyncio.new_event_loop()

        async def go():
            proc = _FakeProcess()
            shell = await _make_started_shell(proc)
            # Init frame consumed ⇒ reader alive and ready.
            self.assertTrue(shell.alive)
            await shell.close()
        loop.run_until_complete(go())
        loop.close()

    def test_mcp_starts_real_interactive_bash(self):
        async def go():
            proc = _FakeProcess()
            conn = _FakeConn(proc)
            shell = PersistentShell(conn)
            start_task = asyncio.ensure_future(shell.start())
            await asyncio.sleep(0.02)
            _feed_done(proc, 0, "/root")
            await start_task
            await shell.close()
            return conn.calls[0]

        call = _run(go())
        self.assertEqual(call["command"], "/bin/bash")
        self.assertNotIn("--noprofile", call["command"])
        self.assertNotIn("--norc", call["command"])
        self.assertTrue(call["request_pty"])

    def test_run_returns_clean_output_and_metadata(self):
        async def go():
            proc = _FakeProcess()
            shell = await _make_started_shell(proc)
            run_task = asyncio.ensure_future(shell.run("ls"))
            await asyncio.sleep(0.02)
            # Echo the command back (PTY line discipline), then real output.
            _feed_done(proc, 0, "/root", output=b"file1\nfile2\n",
                       before=b"\r\n\r\n$ ")
            return await run_task
        r = _run(go())
        self.assertEqual(r["exit_code"], 0)
        self.assertEqual(r["cwd"], "/root")
        self.assertEqual(r["output"], "file1\nfile2\n")

    def test_run_strips_ansi_from_output(self):
        async def go():
            proc = _FakeProcess()
            shell = await _make_started_shell(proc)
            run_task = asyncio.ensure_future(shell.run("ls"))
            await asyncio.sleep(0.02)
            _feed_done(proc, 0, "/root",
                       output=b"\x1b[32mgreen\x1b[0m\n")
            return await run_task
        r = _run(go())
        self.assertEqual(r["output"], "green\n")

    def test_run_reports_nonzero_exit_code(self):
        async def go():
            proc = _FakeProcess()
            shell = await _make_started_shell(proc)
            run_task = asyncio.ensure_future(shell.run("false"))
            await asyncio.sleep(0.02)
            _feed_done(proc, 1, "/root", output=b"err\n")
            return await run_task
        r = _run(go())
        self.assertEqual(r["exit_code"], 1)
        self.assertIn("err", r["output"])

    def test_wrapper_disables_and_restores_errexit(self):
        payload = PersistentShell._build_payload("false", "abc123")
        self.assertIn(b"__rbsh_opts=$-", payload)
        self.assertIn(b"builtin set +e", payload)
        self.assertIn(b"builtin eval \"$__rbsh_cmd\"", payload)
        self.assertIn(b"builtin printf '__RBSH_DONE__:abc123:%s:CWD:%s__\\n'", payload)
        self.assertIn(b"[ \"$__rbsh_restore_e\" = 1 ] && builtin set -e", payload)

    def test_run_updates_cwd(self):
        async def go():
            proc = _FakeProcess()
            shell = await _make_started_shell(proc)
            run_task = asyncio.ensure_future(shell.run("cd /var/log"))
            await asyncio.sleep(0.02)
            _feed_done(proc, 0, "/var/log")
            return await run_task
        r = _run(go())
        self.assertEqual(r["exit_code"], 0)
        self.assertEqual(r["cwd"], "/var/log")

    def test_run_timeout_resets_shell(self):
        loop = asyncio.new_event_loop()

        async def go():
            proc = _FakeProcess()
            shell = await _make_started_shell(proc)
            # Never feed a prompt → run() must time out.
            await shell.run("sleep 999", timeout=0.3)
            await shell.close()
        with self.assertRaises(RuntimeError):
            loop.run_until_complete(go())
        loop.close()

    def test_run_timeout_includes_partial_interactive_prompt(self):
        async def go():
            proc = _FakeProcess()
            shell = await _make_started_shell(proc)
            run_task = asyncio.ensure_future(shell.run("rm k8s.zip", timeout=0.3))
            await asyncio.sleep(0.02)
            token = _latest_done_token(proc)
            proc.stdout.feed(
                f"__RBSH_START__:{token}__\r\n".encode()
                + b"rm: remove regular file 'k8s.zip'?"
            )
            try:
                await run_task
            finally:
                await shell.close()

        with self.assertRaisesRegex(
            RuntimeError,
            "rm: remove regular file 'k8s\\.zip'\\?",
        ) as cm:
            _run(go())
        self.assertIn("remote_shell cannot answer interactive prompts", str(cm.exception))
        self.assertIn("Output captured before timeout:", str(cm.exception))
        self.assertNotIn("__RBSH_START__", str(cm.exception))

    def test_run_writes_command_to_stdin(self):
        async def go():
            proc = _FakeProcess()
            shell = await _make_started_shell(proc)
            run_task = asyncio.ensure_future(shell.run("echo hi"))
            await asyncio.sleep(0.02)
            _feed_done(proc, 0, "/root", output=b"hi\n")
            await run_task
            return proc
        proc = _run(go())
        # The command must have been sent to the PTY inside the bash eval frame.
        self.assertIn(b"__rbsh_cmd='echo hi'", bytes(proc.stdin.written))
        self.assertIn(b"__RBSH_DONE__", bytes(proc.stdin.written))

    def test_run_strips_stale_leading_whitespace(self):
        """A stray idle newline must not leak into the next command result."""
        async def go():
            proc = _FakeProcess()
            shell = await _make_started_shell(proc)
            # Inject a stray newline into _buf, then run a real command.
            proc.stdout.feed(b"\n")
            await asyncio.sleep(0.02)  # let _reader_loop consume it into _buf
            run_task = asyncio.ensure_future(shell.run("ls"))
            await asyncio.sleep(0.02)
            _feed_done(proc, 0, "/root", output=b"file1\nfile2\n")
            return await run_task
        r = _run(go())
        self.assertEqual(r["exit_code"], 0)
        # Output must NOT have a leading newline from idle bytes.
        self.assertEqual(r["output"], "file1\nfile2\n")


class PersistentShellTapTest(unittest.TestCase):

    def test_tap_receives_raw_bytes_with_color(self):
        received = bytearray()

        async def go():
            proc = _FakeProcess()
            shell = PersistentShell(process_factory=lambda: proc)
            shell.attach_tap(lambda chunk: received.extend(chunk))
            start_task = asyncio.ensure_future(shell.start())
            await asyncio.sleep(0.02)
            # Feed colored bytes + done frame.
            _feed_done(proc, 0, "/root", output=b"\x1b[31mraw\x1b[0m\n")
            await start_task
            await shell.close()
        _run(go())
        self.assertIn(b"\x1b[31mraw\x1b[0m", bytes(received))


class PersistentShellFeedRawTest(unittest.TestCase):

    def test_feed_raw_writes_bytes_to_stdin(self):
        async def go():
            proc = _FakeProcess()
            shell = await _make_started_shell(proc)
            shell.feed_raw(b"ls -la\r")
            await shell.close()
            return proc
        proc = _run(go())
        self.assertIn(b"ls -la\r", bytes(proc.stdin.written))


class PersistentShellResizeTest(unittest.TestCase):

    def test_resize_propagates_to_process(self):
        async def go():
            proc = _FakeProcess()
            shell = await _make_started_shell(proc)
            await shell.resize(120, 40)
            await shell.close()
            return proc
        proc = _run(go())
        self.assertEqual(proc.resizes[-1], (120, 40))


if __name__ == "__main__":
    unittest.main()
