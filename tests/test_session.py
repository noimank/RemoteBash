import unittest

from remotebash.core.session import (
    _TOKEN,
    _build_remote_command,
    _parse_exec_output,
)


class RemoteSessionHelpersTest(unittest.TestCase):

    def test_parse_anchor_preserves_output_and_updates_metadata(self):
        cid = "abc123"
        stdout, stderr, exit_code, cwd = _parse_exec_output(
            "test",
            cid,
            "out\n\n",
            f"err\n{_TOKEN}:{cid}:EC:7:CWD:/tmp/work\n",
            0,
            "~",
        )

        self.assertEqual(stdout, "out\n\n")
        self.assertEqual(stderr, "err\n")
        self.assertEqual(exit_code, 7)
        self.assertEqual(cwd, "/tmp/work")

    def test_parse_anchor_preserves_stderr_after_marker(self):
        cid = "abc123"
        _, stderr, exit_code, cwd = _parse_exec_output(
            "test",
            cid,
            "",
            f"before\n{_TOKEN}:{cid}:EC:3:CWD:/x\nafter\n",
            0,
            "~",
        )

        self.assertEqual(stderr, "before\nafter\n")
        self.assertEqual(exit_code, 3)
        self.assertEqual(cwd, "/x")

    def test_malformed_anchor_is_not_removed(self):
        cid = "abc123"
        original_stderr = f"before\n{_TOKEN}:{cid}:EC:not-int:CWD:/x\nafter\n"
        with self.assertLogs("remotebash.core.session", level="WARNING"):
            _, stderr, exit_code, cwd = _parse_exec_output(
                "test", cid, "", original_stderr, 42, "/old"
            )

        self.assertEqual(stderr, original_stderr)
        self.assertEqual(exit_code, 42)
        self.assertEqual(cwd, "/old")

    def test_empty_cwd_marker_keeps_previous_cwd(self):
        cid = "abc123"
        _, stderr, exit_code, cwd = _parse_exec_output(
            "test",
            cid,
            "",
            f"{_TOKEN}:{cid}:EC:2:CWD:\n",
            0,
            "/old",
        )

        self.assertEqual(stderr, "")
        self.assertEqual(exit_code, 2)
        self.assertEqual(cwd, "/old")

    def test_remote_command_is_single_line_and_argv_based(self):
        command = "printf 'x y\\n'\ncd /tmp\n"
        remote_command = _build_remote_command(command, "~", "abc123", safe_rm=True)

        self.assertNotIn("\n", remote_command)
        self.assertIn("/bin/sh -c", remote_command)
        self.assertIn("__RBSH__", remote_command)
        self.assertIn("printf", remote_command)
        self.assertNotIn("bash -s", remote_command)


if __name__ == "__main__":
    unittest.main()
