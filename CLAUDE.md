# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
# Install dependencies
uv sync

# Run the server (HTTP transport — dashboard + MCP on same port)
uv run python main.py --port 24587

# Run with SSE transport
uv run python main.py --transport sse --port 24587

# Run with debug logging
uv run python main.py --debug

# Custom database path
uv run python main.py --db /path/to/remotebash.db

# Run as installed package entry point
uv run remotebash --port 24587

# Run unit tests (stdlib unittest, no network needed)
uv run python -m unittest discover -s tests
```

Unit tests live under `tests/` and use stdlib `unittest`. They exercise `PersistentShell` via a fake asyncssh process (no real SSH). Use `uv add <pkg>` / `uv remove <pkg>` to manage dependencies.

## Architecture

### App assembly (`remotebash/app.py`)

FastAPI and FastMCP coexist on the same port via `fastmcp.utilities.lifespan.combine_lifespans`. The FastMCP HTTP ASGI app is mounted at `/mcp`; the dashboard and REST API live at `/` and `/api`. A module-level global `_server_manager` bridges the FastMCP lifespan (which passes the manager as context to tools) and the FastAPI lifespan (which attaches it to `app.state.manager`). The FastAPI lifespan initializes the SQLite database and loads persisted clients via `ConnectionManager.load()`, then shuts down all connections on exit.

### Persistent interactive shell (`remotebash/core/persistent_shell.py`, `remotebash/core/session.py`)

Commands run on a **single long-lived interactive bash shell with an allocated PTY**, not a fresh `/bin/bash -c` per call. This makes execution behave like a real terminal session: `cd`, `export`, shell functions, aliases, `umask` and history all persist across commands, and programs see a real terminal (so `ls` colouring, `isatty()` checks and interactive prompts behave normally).

`PersistentShell` manages one PTY-backed shell on an SSH connection:

- Opens the shell via `conn.create_process(term_type="xterm-256color", term_size=(cols, rows), encoding=None)`. `encoding=None` keeps I/O as raw bytes so the reader loop can strip ANSI itself.
- Leaves the user's prompt alone. `run(command)` executes in the current bash via `builtin eval`, wraps output between private `__RBSH_START__:<token>__` and `__RBSH_DONE__:<token>:<exit_code>:CWD:<cwd>__` sentinels, and waits for that exact token.
- The wrapper temporarily disables `errexit` (`set -e`) around command execution and restores it afterwards, so non-zero commands still produce the done sentinel instead of killing the shell before framing completes.
- Output is ANSI-stripped, echoed wrapper input is removed defensively, and CRLF is normalised; command output newlines are otherwise preserved.
- `feed_raw` + `attach_tap` provide a raw byte pass-through path for the in-browser terminal (colours preserved, xterm.js renders).
- Command timeout cancels the pending run and hard-closes the shell; the next `_ensure_shell()` rebuilds it.

`RemoteSession._ensure_shell()` lazily starts the shell on first use and tears it down on idle timeout or if the process died. An `asyncio.Lock` serialises concurrent `exec()` callers so two simultaneous invocations cannot create duplicate shells. `exec()` is a thin wrapper over `shell.run()`. The optional `safe_rm` shim is injected at shell start via the `init_script` constructor arg (defined in `session.py` to avoid a circular import).

### Browser terminal (`remotebash/api/ws.py`)

A WebSocket at `/api/clients/{name}/terminal` bridges xterm.js to a separate `PersistentShell` owned by `ConnectionManager._terminals`. Browser→server binary frames are fed to `shell.feed_raw`; server→browser frames carry raw PTY output via a `tap` callback. Resize messages adjust the PTY. The terminal shell is **independent** of the MCP `exec` shell (they can't share one PTY without corrupting framing) but both reuse the same SSH connection. The shell survives WebSocket disconnect so closing/reopening the terminal tab keeps state.

### Connection lifecycle (`remotebash/core/session.py`, `remotebash/core/manager.py`)

- **Lazy connect** — `exec()` calls `connect()` only when the session is not already connected.
- **Idle timeout** — if `time.monotonic() - last_activity > 3600` seconds, the session disconnects before reconnecting transparently.
- **Keepalive** — asyncssh is configured with `keepalive_interval=30, keepalive_count_max=3`.
- **Error recovery** — any `asyncssh.Error`, `OSError`, or `TimeoutError` during execution triggers an immediate disconnect; the next call will reconnect.
- **`test_connection()`** creates a fresh independent connection (10s timeout) to verify credentials without touching the existing session.

### Database (`remotebash/core/database.py`)

SQLite via aiosqlite with `row_factory = aiosqlite.Row`. Two tables:
- `clients` — SSH connection configs (name PK, host, port, user, password, label, enabled, timestamps).
- `audit_log` — full command history (client FK, command, output, exit_code, cwd, duration_ms, success, timestamp). Indexed on `client_name` and `created_at`.

The `open_db()` function creates parent directories if needed and runs schema migration on every open (all DDL uses `IF NOT EXISTS` / `IF NOT EXISTS`).

### Manager (`remotebash/core/manager.py`)

Holds an in-memory `dict[str, RemoteSession]` synchronized with the DB, plus a `dict[str, PersistentShell]` of browser-terminal shells (`_terminals`). `load()` reconstructs sessions from `clients` rows. Audit callbacks are registered per-session and write to `audit_log` on every command. `get_or_create_terminal(name)` returns (and lazily starts) the terminal shell for a client; idle terminal shells are torn down on next access (same 3600s policy as the MCP path). `close_terminal(name)` / `close()` tear them down. `update()` persists to the DB first, then updates in-memory state on success (so a DB failure leaves memory unchanged). Allowed update fields: `{host, port, user, password, enabled, safe_rm}`.

### MCP tools (`remotebash/api/tools.py`)

Three tools:
- `remote_shell(client_name, command, timeout=30)` — executes a command, returns `{output, exit_code, cwd}`.
- `data_transfer(client_name, src, dst, direction="remote2local")` — SFTP file transfer, returns `{success, direction, src, dst, size_bytes, duration_ms}`.
- `list_remote_clients()` — returns only **enabled** clients with `{client_name, host, port, user, cwd, safe_rm}`.

Both access the manager via `ctx.lifespan_context["manager"]`.

### REST API (`remotebash/api/routes.py`, `remotebash/api/ws.py`)

CRUD at `/api/clients` plus `/api/clients/{name}/connect|disconnect|test`. Client names are validated: only alphanumeric, hyphens, underscores. Audit query at `/api/audit` with pagination (`limit`, `offset`) and optional `client_name` filter. Audit deletion supports single entry, by client, or bulk before an ID. The WebSocket terminal lives at `/api/clients/{name}/terminal` (`ws.py`).

### Web dashboard (`remotebash/web/`)

Two pages: dashboard (`/`) and audit (`/audit`). Both extend `base.html.j2` with a dark Tailwind theme (custom `surface`, `border`, `accent` colors). Tailwind CSS is vendored locally as `static/tailwind.js` (no build step, no CDN dependency). The dashboard is a SPA — all management interactions go through the REST API via vanilla JS (`app.js` and `audit.js`). Templates are split into partials (`header.html.j2`, `add_form.html.j2`, `client_list.html.j2`).

The **in-browser terminal** is a full interactive PTY session rendered by xterm.js (vendored locally at `static/js/vendor/xterm.js` + `xterm-addon-fit.js`, CSS at `static/css/xterm.css`). `terminal.js` opens a WebSocket to the terminal endpoint, forwards keystrokes as binary frames, renders incoming bytes, and sends resize messages. Each client row has a 「终端」 button that opens the terminal modal (`partials/terminal_modal.html.j2`). Terminal I/O is **not** written to the audit log (only MCP `remote_shell` calls are audited).

### Config (`remotebash/config.py`)

`ServerConfig` dataclass with fields: `transport` (http/sse), `host`, `port` (default 24587), `debug`, `db_path`. The default DB path is `~/.remotebash/remotebash.db` on all platforms.

## Key dependencies

| Package | Role |
|---------|------|
| `fastmcp` ≥ 3.0 | MCP tool server |
| `asyncssh` ≥ 2.0 | Async SSH client |
| `fastapi[standard]` ≥ 0.100 | Web dashboard & REST API |
| `aiosqlite` ≥ 0.20 | Async SQLite driver |
| `jinja2` ≥ 3.0 | Dashboard templating |
| `uvicorn[standard]` ≥ 0.30 | ASGI server (used programmatically) |
