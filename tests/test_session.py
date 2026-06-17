"""Tests for RemoteSession command execution behavior."""

import asyncio
import unittest

from remotebash.core.session import RemoteSession


def _run(coro):
    loop = asyncio.new_event_loop()
    try:
        return loop.run_until_complete(coro)
    finally:
        loop.close()


class _FakeShell:
    alive = True
    _safe_rm = False

    def __init__(self, exc):
        self.exc = exc

    async def run(self, command, timeout=30):
        raise self.exc


class _SerialFakeShell:
    """Fake shell that proves exec() serializes concurrent callers.

    ``run()`` records peak concurrency (must stay 1) and the start order of
    commands.  The ``asyncio.sleep`` holds the shell long enough that, without
    serialization, two concurrent exec() calls would overlap on it.
    """
    alive = True
    _safe_rm = False

    def __init__(self):
        self._active = 0
        self.peak = 0
        self.started = []

    async def run(self, command, timeout=30):
        self._active += 1
        self.peak = max(self.peak, self._active)
        self.started.append(command)
        await asyncio.sleep(0.02)
        self._active -= 1
        return {"output": command, "exit_code": 0, "cwd": "/root",
                "duration_ms": 1}


class _SessionWithFakeShell(RemoteSession):
    def __init__(self, shell):
        super().__init__(
            name="test-client",
            host="example.invalid",
            port=22,
            user="root",
            password="secret",
        )
        self._shell = shell
        self._cwd = "/root"

    async def _ensure_shell(self):
        return self._shell


class RemoteSessionAuditTest(unittest.TestCase):

    def test_exec_audits_runtime_error_with_partial_output(self):
        async def go():
            exc = RuntimeError(
                "Command timed out after 1s.\n"
                "remote_bash cannot answer interactive prompts. The command "
                "may be waiting for input; retry with non-interactive flags or "
                "include the required input in the command.\n"
                "The remote shell session was reset.\n"
                "Output captured before timeout:\n"
                "rm: remove regular file 'k8s.zip'?"
            )
            session = _SessionWithFakeShell(_FakeShell(exc))
            entries = []

            async def audit(client_name, command, result):
                entries.append((client_name, command, result))

            session.set_audit_callback(audit)

            with self.assertRaises(RuntimeError):
                await session.exec("rm k8s.zip", timeout=1)

            return entries

        entries = _run(go())
        self.assertEqual(len(entries), 1)
        client_name, command, result = entries[0]
        self.assertEqual(client_name, "test-client")
        self.assertEqual(command, "rm k8s.zip")
        self.assertEqual(result["exit_code"], -1)
        self.assertEqual(result["cwd"], "/root")
        self.assertGreaterEqual(result["duration_ms"], 0)
        self.assertIn("Command timed out after 1s", result["output"])
        self.assertIn("remote_bash cannot answer interactive prompts", result["output"])
        self.assertIn("rm: remove regular file 'k8s.zip'?", result["output"])


class RemoteSessionConcurrencyTest(unittest.TestCase):

    def test_exec_queues_concurrent_commands_instead_of_failing(self):
        async def go():
            session = _SessionWithFakeShell(_SerialFakeShell())
            # Fire two commands concurrently. Before serialization the second
            # would raise "Another command is already running on this shell".
            results = await asyncio.gather(
                session.exec("cmd-a"),
                session.exec("cmd-b"),
            )
            return session._shell, results

        shell, results = _run(go())
        # Both completed successfully — no error.
        self.assertEqual([r["output"] for r in results], ["cmd-a", "cmd-b"])
        # They never overlapped on the shell.
        self.assertEqual(shell.peak, 1)
        # And they ran in arrival order.
        self.assertEqual(shell.started, ["cmd-a", "cmd-b"])
