"""PersistentShell — one long-lived interactive bash shell per SSH connection.

Unlike one-shot ``/bin/bash -c`` execs, a PersistentShell stays alive across
commands, so **all** shell state (CWD, ``export``, functions, aliases,
``umask``, history, …) persists exactly like a real terminal session.  A PTY
is allocated so programs behave as if attached to a real terminal (colour
output, ``isatty`` checks, interactive prompts).

Command framing
---------------
A PTY is an unbounded byte stream with no built-in "command finished" signal.
For the MCP command path, every command is wrapped in a one-time tokenized
frame printed by bash itself::

    __RBSH_START__:<token>__
    <command output>
    __RBSH_DONE__:<token>:<exit_code>:CWD:<abs_path>__

The reader loop scans the byte buffer for the current command token.  When the
full frame is found, only the bytes between the start and done markers are
returned as command output.

Two consumption modes
---------------------
* :meth:`run`  — used by the MCP ``remote_bash`` tool.  Returns clean,
  ANSI-stripped ``{output, exit_code, cwd}`` for one command.
* :meth:`feed_raw` + :meth:`attach_tap` — used by the in-browser xterm.js
  terminal.  Bytes flow through raw (colours preserved); the browser renders.
  The terminal path uses a human-readable PS1 and a one-shot readiness
  marker, so the user never sees machine framing in their prompt.
"""

import asyncio
import logging
import re
import time
import unicodedata
import uuid

import asyncssh

logger = logging.getLogger(__name__)

# Default PTY dimensions — wide enough that line wrapping rarely triggers.
# The browser terminal resizes live via :meth:`resize`.
_DEFAULT_COLS = 200
_DEFAULT_ROWS = 50

# A shorter marker echoed once during browser-terminal shell init.  It replaces
# the full prompt-template on that path so the user never sees machine framing
# in their terminal — they get a normal, human-readable PS1 instead.
_READY_MARKER = b"__RBSH_READY__"

_START_PREFIX = "__RBSH_START__"
_DONE_PREFIX = "__RBSH_DONE__"
_TTY_OP_ECHO = 53  # RFC 4254 PTY mode opcode for ECHO.

# Human-readable PS1 for the browser terminal (colourised, xterm.js renders
# the ANSI escapes).  Uses bash-specific escapes (\\e, \\u, \\h, \\w).
# Layout:  user@host /path\\n➜
_TERMINAL_PS1 = (
    "\\[\\e[1;36m\\]\\u\\[\\e[0m\\]@\\[\\e[1;33m\\]\\h\\[\\e[0m\\]"
    " \\[\\e[1;37m\\]\\w\\[\\e[0m\\]\\n\\[\\e[1;32m\\]➜\\[\\e[0m\\] "
)

# Max bytes to buffer in ``_buf`` when the terminal path is active (no prompt
# scanning to drain it).  Above this threshold we discard the oldest half.
_TERMINAL_BUF_MAX = 512 * 1024  # 512 KiB


def strip_ansi(data: bytes) -> bytes:
    """Strip ANSI escape sequences (colours, cursor moves, etc.).

    A lightweight regex sufficient for typical command output (``ls``
    colouring, ``grep --color``, prompts).  We intentionally do not link a
    full terminfo parser — the goal is "clean text for an AI to read", not
    perfect rendering fidelity.  The browser path uses raw bytes and lets
    xterm.js handle the escapes.
    """
    # CSI sequences:  ESC [ ... letter   (colours, cursor, erase)
    # OSC sequences:  ESC ] ... BEL/ST   (title, hyperlink)
    # Other common:   ESC ( B  (charset),  ESC = / ESC >  (keypad),  ESC M
    return re.sub(
        rb"\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)"      # OSC ... BEL / ST
        rb"|\x1b[@-Z\\-_]"                          # single-char ESC X
        rb"|\x1b\[[0-9;?]*[ -/]*[@-~]",            # CSI ...
        b"",
        data,
    )


class PersistentShell:
    """A long-lived interactive PTY shell on a single SSH connection."""

    def __init__(self, conn=None, *, cols=_DEFAULT_COLS, rows=_DEFAULT_ROWS,
                 read_chunk=4096, safe_rm=False, process_factory=None,
                 init_script="", prompt_template=None):
        # ``process_factory`` lets tests inject a _FakeProcess without a real
        # asyncssh connection.  In production ``conn`` is an
        # SSHClientConnection and we call ``conn.create_process(...)``.
        # ``init_script`` is extra shell code (e.g. the safe-rm shim); the
        # caller owns it to avoid a circular import (it lives in session.py).
        # ``prompt_template`` enables browser-terminal mode. A one-shot
        # readiness marker is echoed before the nice prompt.
        #
        # ``safe_rm`` is stored so ``RemoteSession._ensure_shell()`` can
        # detect a dashboard toggle of the setting while the shell is alive
        # and transparently rebuild with the updated shim.  The actual
        # behaviour is driven by ``init_script``; this is only for change
        # detection.
        self._conn = conn
        self._process_factory = process_factory
        self._cols = cols
        self._rows = rows
        self._read_chunk = read_chunk
        self._safe_rm = safe_rm
        self._init_script = init_script
        self._prompt_template = prompt_template

        self._proc = None               # asyncssh.SSHClientProcess | _FakeProcess
        self._buf = bytearray()         # raw stdout bytes not yet consumed
        self._reader_task: asyncio.Task | None = None
        self._ready: asyncio.Event = asyncio.Event()  # set after init frame/marker
        self._pending: asyncio.Future | None = None   # resolves next run()
        self._pending_pattern: re.Pattern | None = None
        self._taps: list = []           # raw-byte callbacks for browser term

    # ── lifecycle ─────────────────────────────────────────────────

    async def start(self):
        r"""Open the PTY bash shell and initialise the selected mode.

        RemoteBash requires ``/bin/bash`` — shell functions (the ``safe_rm``
        shim), ``_TERMINAL_PS1`` escapes (``\u``, ``\h``, ``\e``, ``\[``,
        …), and Unicode PS1 characters all depend on bash-specific features.

        ``encoding=None`` keeps I/O as raw bytes so we can strip ANSI
        ourselves and so the browser path gets the original stream.

        When ``prompt_template`` was passed to the constructor, this is the
        browser-terminal path: we echo a one-shot readiness marker, then set a
        human-readable PS1.

        Without ``prompt_template``, this is the MCP command path. It starts
        the same interactive bash users get in the browser terminal, so normal
        startup files and shell customizations load. It does not parse PS1;
        instead, :meth:`run` appends a tokenized sentinel after each command
        and waits for that exact token.
        """

        if self._process_factory is not None:
            self._proc = self._process_factory()
        else:
            command = "/bin/bash"
            kwargs = {}
            if self._prompt_template is None:
                kwargs["term_modes"] = {_TTY_OP_ECHO: 0}
            self._proc = await self._conn.create_process(
                command=command,
                term_type="xterm-256color",
                term_size=(self._cols, self._rows),
                request_pty=True,
                encoding=None,
                **kwargs,
            )

        self._reader_task = asyncio.create_task(self._reader_loop())

        # Step 1 — Kill echo in its own command BEFORE the init payload.
        #
        # ``stty -echo`` only takes effect AFTER the current input line is
        # consumed.  If it were inlined in the init block below, the entire
        # init line (hundreds of bytes of PS1 + safe_rm shim) would be PTY-
        # echoed back to the terminal.  By putting it in its own one-shot,
        # only this tiny line is ever visible even on a fresh PTY where echo
        # is still on.
        await self._send_init("stty -echo 2>/dev/null")

        # Step 2 — Mode-specific init.
        # Echo stays OFF throughout so none of this is visible.
        # The ``stty echo`` re-enable is deferred to Step 4 below
        # so the safe_rm shim (Step 3) is also invisible.
        if self._prompt_template:
            # Browser-terminal path: one-shot readiness marker → nice PS1.
            # The marker is printed via printf then erased visually with ANSI
            # (carriage-return + clear-line) before the PS1 renders.
            # Bash expands \e, \u, \h, \w, \[, \] in PS1 — full dynamic prompt.
            ps1_cmd = f"export PS1='{self._prompt_template}'"
            init = (
                f"printf '%s' '{_READY_MARKER.decode()}'; "
                f"{ps1_cmd}; "
                "export PS2=''; export PROMPT_COMMAND=; "
                "printf '\\r\\033[2K'; "
                "true"
            )
        else:
            # MCP path: do not rewrite PS1/PROMPT_COMMAND. The goal is the
            # same initialized shell environment as a real terminal; command
            # completion is framed by run(), not by prompt parsing.
            init = "true"
        await self._send_init(init)

        # Step 3 — Optional init script (e.g. safe_rm shim). Sent while echo
        # is still OFF so the script body is never echoed to the terminal.
        #
        # A preceding ``unalias rm`` is sent on its own line BEFORE the
        # shim, because bash alias expansion is a lexical pass that scans
        # the entire input line BEFORE executing any commands on it.  If
        # ``unalias rm`` and ``rm(){...}`` were on the same line (or
        # inside the same ``eval`` string), the parser would expand ``rm``
        # to ``rm -i`` before executing ``unalias``, producing
        # ``rm -i(){...}`` — a syntax error silently swallowed by
        # ``2>/dev/null``.
        if self._init_script:
            await self._send_init("unalias rm 2>/dev/null")
            escaped = self._init_script.replace("'", "'\\''")
            await self._send_init(
                f"builtin eval '{escaped}' 2>/dev/null; true"
            )

        # Step 4 — Re-enable echo (browser-terminal path only).  Isolated
        # in its own command so none of the init payload is ever echoed.
        if self._prompt_template:
            await self._send_init("stty echo 2>/dev/null")

        if self._prompt_template is None:
            try:
                await self._run_framed("true", timeout=15)
            except Exception as exc:
                await self.close()
                raise RuntimeError(
                    "Remote bash command framing failed during initialization"
                ) from exc
            self._ready.set()
            return self

        # Poll for the first prompt.  Check periodically whether the
        # underlying process has already died so we can fail fast instead
        # of waiting out the full timeout.
        deadline = time.monotonic() + 15
        while not self._ready.is_set():
            if self._proc.is_closing():
                await self.close()
                raise RuntimeError(
                    "Remote shell exited during initialization — "
                    "the host may not have /bin/bash")
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                break
            try:
                await asyncio.wait_for(
                    self._ready.wait(), timeout=min(remaining, 0.3))
            except asyncio.TimeoutError:
                pass  # loop around, check is_closing() again

        if not self._ready.is_set():
            await self.close()
            raise RuntimeError(
                "Remote shell did not produce a prompt within 15s — "
                "the host may not have /bin/bash")
        return self

    async def _send_init(self, line: str):
        """Write an initialisation line; not user-visible (echo is off)."""
        self._proc.stdin.write((line + "\n").encode())
        await self._proc.stdin.drain()

    async def _reader_loop(self):
        """Background loop: pull PTY bytes, drive taps and run() futures."""
        try:
            while not self._proc.is_closing():
                chunk = await self._proc.stdout.read(self._read_chunk)
                if not chunk:
                    break  # b'' ⇒ EOF (both real SSHReader and fake)

                # Browser terminal always sees the raw stream (colours intact).
                for tap in list(self._taps):
                    try:
                        tap(chunk)
                    except Exception:  # a flaky tap must not kill the loop
                        logger.exception("terminal tap callback raised")

                self._buf.extend(chunk)
                self._scan()
        except (asyncssh.Error, OSError) as exc:
            logger.warning("reader_loop for shell ended (%s)", exc)
        except asyncio.CancelledError:
            raise
        except Exception:
            logger.exception("reader_loop crashed")
        finally:
            # If a command is still outstanding, fail it so run() doesn't hang.
            if self._pending is not None and not self._pending.done():
                self._pending.set_exception(
                    RuntimeError("Remote shell closed before command finished"))

    def _scan(self):
        """Scan ``_buf`` and resolve readiness or a pending command."""
        if self._pending is None or self._pending.done():
            # No active command — browser-terminal startup still needs its
            # readiness marker so ``start()`` can return.
            if not self._ready.is_set():
                idx = self._buf.find(_READY_MARKER)
                if idx >= 0:
                    # Consume through the end of the line containing the marker.
                    nl = self._buf.find(b"\n", idx)
                    end = (nl + 1) if nl >= 0 else idx + len(_READY_MARKER)
                    del self._buf[:end]
                    self._ready.set()
                    return

            # No command is waiting for output. Keep memory bounded. For MCP,
            # stale bytes are intentionally discarded before the next run().
            if self._ready.is_set() and len(self._buf) > _TERMINAL_BUF_MAX:
                self._buf = self._buf[-_TERMINAL_BUF_MAX // 2:]
            return

        if self._pending_pattern is None:
            return

        m = self._pending_pattern.search(self._buf)
        if not m:
            return

        # IMPORTANT: extract groups BEFORE mutating _buf.  re.Match holds a
        # reference to the underlying bytearray, so deleting from it would
        # corrupt group() results (they'd return empty/garbage bytes).
        raw_output = bytes(m.group(1))
        exit_code = int(m.group(2))
        cwd = m.group(3).decode("utf-8", "replace").rstrip()
        del self._buf[:m.end()]

        if not self._ready.is_set():
            self._ready.set()

        fut = self._pending
        self._pending = None
        self._pending_pattern = None
        fut.set_result((raw_output, exit_code, cwd))

    # ── command execution (MCP path) ──────────────────────────────

    async def run(self, command: str, *, timeout: float = 30):
        """Run one command on the persistent shell and return its result.

        Returns ``{output, exit_code, cwd, duration_ms}``. ``exit_code`` comes
        from ``$?`` captured immediately after the command, so it is exact even
        for builtins like ``cd``.
        """
        t0 = time.monotonic()
        raw_output, exit_code, cwd, token = await self._run_framed(
            command, timeout=timeout)
        elapsed = int((time.monotonic() - t0) * 1000)

        # PTY cooked mode can echo the typed wrapper if the remote ignores our
        # echo-off setup. Strip that defensively, then remove ANSI codes.
        clean = self._clean_output(raw_output, command, token=token)

        return {
            "output": clean,
            "exit_code": exit_code,
            "cwd": cwd,
            "duration_ms": elapsed,
        }

    async def _run_framed(self, command: str, *, timeout: float = 30):
        """Run command text and wait for its private sentinel frame."""
        if self._reader_task is None or self._reader_task.done():
            raise RuntimeError("PersistentShell is not running — call start()")

        if self._pending is not None and not self._pending.done():
            raise RuntimeError("Another command is already running on this shell")

        # Drop stale idle bytes (login banners, old background output, prompts)
        # before starting a command-oriented MCP frame.
        self._buf.clear()

        token = uuid.uuid4().hex
        loop = asyncio.get_running_loop()
        self._pending = loop.create_future()
        self._pending_pattern = self._done_pattern(token)

        self._proc.stdin.write(self._build_payload(command, token))
        await self._proc.stdin.drain()

        try:
            raw_output, exit_code, cwd = await asyncio.wait_for(
                asyncio.shield(self._pending), timeout=timeout)
        except asyncio.TimeoutError:
            # The command hung.  Capture any partial output that accumulated
            # in _buf before the timeout so the caller can diagnose WHY the
            # command hung (password prompt, slow network, etc.) instead of
            # getting a completely opaque error.
            partial = self._clean_partial_output(
                bytes(self._buf), command, token) if self._buf else ""

            # Shell state is now ambiguous — tear it down.
            self._pending = None
            self._pending_pattern = None
            await self.close()
            detail = (
                f"Command timed out after {timeout}s.\n"
                "remote_bash cannot answer interactive prompts. The command "
                "may be waiting for input; retry with non-interactive flags or "
                "include the required input in the command.\n"
                "The remote shell session was reset."
            )
            if partial:
                detail += f"\nOutput captured before timeout:\n{partial}"
            raise RuntimeError(detail) from None

        return raw_output, exit_code, cwd, token

    @staticmethod
    def _quote_for_bash(value: str) -> str:
        """Single-quote arbitrary command text for bash eval."""
        return "'" + value.replace("'", "'\\''") + "'"

    @classmethod
    def _build_payload(cls, command: str, token: str) -> bytes:
        quoted = cls._quote_for_bash(command)
        # eval runs in the current shell so cd/export/functions persist. The
        # sentinel has a per-command token, preventing accidental matches in
        # normal output.
        payload = (
            "__rbsh_opts=$-; "
            "case $__rbsh_opts in *e*) builtin set +e; __rbsh_restore_e=1;; *) __rbsh_restore_e=0;; esac; "
            f"__rbsh_cmd={quoted}; "
            f"builtin printf '{_START_PREFIX}:{token}__\\n'; "
            "builtin eval \"$__rbsh_cmd\"; "
            "__rbsh_ec=$?; "
            f"builtin printf '{_DONE_PREFIX}:{token}:%s:CWD:%s__\\n' "
            "\"$__rbsh_ec\" \"$PWD\"; "
            "[ \"$__rbsh_restore_e\" = 1 ] && builtin set -e; "
            "builtin unset __rbsh_cmd __rbsh_ec __rbsh_opts __rbsh_restore_e\n"
        )
        return payload.encode()

    @staticmethod
    def _done_pattern(token: str) -> re.Pattern:
        return re.compile(
            re.escape(f"{_START_PREFIX}:{token}__".encode())
            + rb" ?\r?\n"
            + rb"(.*?)"
            + re.escape(f"{_DONE_PREFIX}:{token}:".encode())
            + rb"(-?\d+):CWD:(.*?)__ ?\r?\n",
            re.DOTALL,
        )

    @classmethod
    def _clean_partial_output(cls, raw: bytes, command: str, token: str) -> str:
        """Clean output captured before a command timed out.

        During a normal run, the done-frame regex returns only bytes between
        the private start and done frames. On timeout, ``_buf`` still contains
        the start frame, so remove it before applying the normal text cleanup.
        """
        start = f"{_START_PREFIX}:{token}__".encode()
        idx = raw.find(start)
        if idx >= 0:
            line_end = raw.find(b"\n", idx)
            raw = raw[(line_end + 1) if line_end >= 0 else idx + len(start):]
        return cls._clean_output(raw, command, token=token)

    @staticmethod
    def _clean_output(raw: bytes, command: str, token: str | None = None) -> str:
        """Normalise command output for the MCP/text path.

        1. Render carriage returns like a terminal: ``\\r+\\n`` is one
           newline, while standalone ``\\r`` returns to column 0 and lets
           following bytes overwrite the current line.
        2. Drop the leading PTY echo of the command itself, **only** when the
           shell is in a transient state where echo might leak through
           (e.g. before ``stty -echo`` has fully taken effect).  We detect
           this by checking whether ``raw`` begins with the sent command
           line — if it does, PTY echo is (unexpectedly) present and we
           strip it.  Otherwise we leave the first line alone so we never
           accidentally drop legitimate output that happens to match the
           command text.
        3. Strip ANSI colour/escape sequences.
        4. Decode to ``str`` without trimming command output.
        """
        text = PersistentShell._render_terminal_text(raw)
        lines = text.split("\n")

        # Only strip echoed input when PTY echo is actually present.
        while lines:
            first = lines[0].strip()
            if first == command.strip():
                lines = lines[1:]
                continue
            if token is not None and token in first and first.startswith("__rbsh_cmd="):
                lines = lines[1:]
                continue
            break
        return "\n".join(lines)

    @staticmethod
    def _render_terminal_text(raw: bytes) -> str:
        """Approximate terminal rendering for the MCP text path.

        PTYs often produce CRLF, and CRLF file content can become CRCRLF.
        A terminal treats those as a single visual newline. Standalone CR is
        carriage return: it moves the cursor to column 0 without advancing the
        row, so later text overwrites the existing line. ANSI escape sequences
        do not occupy cells; basic erase/cursor controls are applied so common
        progress bars and coloured output reduce to readable final text.
        """
        text = raw.decode("utf-8", "replace")
        lines = [[]]
        row = 0
        col = 0
        i = 0
        while i < len(text):
            ch = text[i]

            if ch == "\x1b":
                i, row, col = PersistentShell._apply_escape_sequence(
                    text, i, lines, row, col)
                continue

            if ch == "\r":
                j = i
                while j < len(text) and text[j] == "\r":
                    j += 1
                if j < len(text) and text[j] == "\n":
                    lines.append([])
                    row += 1
                    col = 0
                    i = j + 1
                else:
                    col = 0
                    i = j
                continue
            if ch == "\n":
                lines.append([])
                row += 1
                col = 0
                i += 1
                continue
            if ch == "\b":
                col = max(0, col - 1)
                i += 1
                continue
            if ch == "\t":
                next_tab = ((col // 8) + 1) * 8
                line = lines[row]
                while len(line) < next_tab:
                    line.append(" ")
                col = next_tab
                i += 1
                continue
            if unicodedata.category(ch)[0] == "C":
                i += 1
                continue

            col = PersistentShell._put_terminal_char(lines[row], col, ch)
            i += 1

        return "\n".join(PersistentShell._cells_to_text(line) for line in lines)

    @staticmethod
    def _put_terminal_char(line: list[str], col: int, ch: str) -> int:
        if unicodedata.combining(ch):
            for idx in range(min(col, len(line)) - 1, -1, -1):
                if line[idx]:
                    line[idx] += ch
                    break
            return col

        width = 2 if unicodedata.east_asian_width(ch) in {"F", "W"} else 1
        while len(line) < col:
            line.append(" ")
        for offset in range(width):
            idx = col + offset
            if idx < len(line):
                line[idx] = ch if offset == 0 else ""
            else:
                line.append(ch if offset == 0 else "")
        return col + width

    @staticmethod
    def _cells_to_text(line: list[str]) -> str:
        return "".join(cell for cell in line if cell)

    @staticmethod
    def _apply_escape_sequence(text: str, start: int, lines: list[list[str]],
                               row: int, col: int) -> tuple[int, int, int]:
        if start + 1 >= len(text):
            return start + 1, row, col

        introducer = text[start + 1]
        if introducer == "]":  # OSC, e.g. title/hyperlink
            end_bel = text.find("\x07", start + 2)
            end_st = text.find("\x1b\\", start + 2)
            ends = [pos for pos in (end_bel, end_st) if pos >= 0]
            if not ends:
                return len(text), row, col
            end = min(ends)
            return end + (2 if end == end_st else 1), row, col

        if introducer != "[":
            return start + 2, row, col

        end = start + 2
        while end < len(text) and not ("@" <= text[end] <= "~"):
            end += 1
        if end >= len(text):
            return len(text), row, col

        params = text[start + 2:end]
        final = text[end]
        values = PersistentShell._parse_csi_params(params)
        line = lines[row]

        if final == "K":
            mode = values[0] if values else 0
            if mode == 0:
                del line[col:]
            elif mode == 1:
                for idx in range(min(col + 1, len(line))):
                    line[idx] = " "
            elif mode == 2:
                line.clear()
                col = 0
        elif final == "G":
            col = max(0, (values[0] if values else 1) - 1)
        elif final == "C":
            col += values[0] if values else 1
        elif final == "D":
            col = max(0, col - (values[0] if values else 1))
        # Colour/style and unsupported cursor/screen operations are ignored.

        return end + 1, row, col

    @staticmethod
    def _parse_csi_params(params: str) -> list[int]:
        values = []
        for part in params.lstrip("?").split(";"):
            if not part:
                values.append(0)
                continue
            try:
                values.append(int(part))
            except ValueError:
                values.append(0)
        return values

    # ── raw passthrough (browser terminal path) ───────────────────

    def attach_tap(self, cb):
        """Register a ``cb(bytes)`` callback that receives raw PTY output.

        Used by the WebSocket endpoint to stream bytes to xterm.js.  Returns
        an unsubscribe handle.
        """
        self._taps.append(cb)

        def detach():
            try:
                self._taps.remove(cb)
            except ValueError:
                pass
        return detach

    def clear_buffer(self):
        """Discard buffered PTY output from before the current attach.

        Called by the WebSocket handler right after attaching a new tap so
        stale bytes from the no-tap gap don't pollute the reconnected view.
        """
        self._buf.clear()

    def feed_raw(self, data: bytes):
        """Pass-through keystrokes from the browser terminal to the PTY.

        Unlike :meth:`run`, this does no framing/echo-stripping — the browser
        owns the interaction.  NOTE: mixing :meth:`feed_raw` with :meth:`run`
        on the same shell is undefined; the manager keeps separate shells per
        consumption mode (MCP vs browser) precisely to avoid this.
        """
        if self._proc is None:
            raise RuntimeError("PersistentShell is not running")
        self._proc.stdin.write(data)

    async def resize(self, cols: int, rows: int):
        """Resize the PTY (browser window resize)."""
        self._cols, self._rows = cols, rows
        if self._proc is not None:
            self._proc.change_terminal_size(cols, rows)

    # ── teardown ──────────────────────────────────────────────────

    @property
    def alive(self):
        return (self._proc is not None and not self._proc.is_closing()
                and self._reader_task is not None and not self._reader_task.done())

    async def close(self):
        """Tear everything down without raising."""
        if self._reader_task is not None and not self._reader_task.done():
            self._reader_task.cancel()
            try:
                await self._reader_task
            except (asyncio.CancelledError, Exception):
                pass
            self._reader_task = None
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
        self._pending = None
        self._ready.clear()
