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
                "remote_shell cannot answer interactive prompts. The command "
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
        self.assertIn("remote_shell cannot answer interactive prompts", result["output"])
        self.assertIn("rm: remove regular file 'k8s.zip'?", result["output"])
