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
```

There are no tests or lint/type-check configurations yet. Use `uv add <pkg>` / `uv remove <pkg>` to manage dependencies.

## Architecture

### App assembly (`remotebash/app.py`)

FastAPI and FastMCP coexist on the same port via `fastmcp.utilities.lifespan.combine_lifespans`. The FastMCP HTTP ASGI app is mounted at `/mcp`; the dashboard and REST API live at `/` and `/api`. A module-level global `_server_manager` bridges the FastMCP lifespan (which passes the manager as context to tools) and the FastAPI lifespan (which attaches it to `app.state.manager`). The FastAPI lifespan initializes the SQLite database and loads persisted clients via `ConnectionManager.load()`, then shuts down all connections on exit.

### CWD tracking (`remotebash/core/session.py`)

Each command is wrapped in a shell snippet before execution:

```
cd <quoted_cwd> 2>/dev/null && {command}; echo __RBSH__:<uuid>:EC:$?:CWD:$(pwd) >&2
```

The anchor line (tagged with a per-call UUID) is appended to **stderr**. After the command runs, `exec()` finds the anchor, parses `exit_code` and the new `$PWD`, strips the anchor from stderr, and stores the new CWD for the next call. This is NOT a subshell — `cd` effects persist across calls. The public result (`stdout`, `stderr`, `exit_code`, `cwd`, `duration_ms`) is byte-for-byte what the remote command produced, plus metadata.

### Connection lifecycle (`remotebash/core/session.py`, `remotebash/core/manager.py`)

- **Lazy connect** — `exec()` calls `connect()` only when the session is not already connected.
- **Idle timeout** — if `time.monotonic() - last_activity > 3600` seconds, the session disconnects before reconnecting transparently.
- **Keepalive** — asyncssh is configured with `keepalive_interval=30, keepalive_count_max=3`.
- **Error recovery** — any `asyncssh.Error`, `OSError`, or `TimeoutError` during execution triggers an immediate disconnect; the next call will reconnect.
- **`test_connection()`** creates a fresh independent connection (10s timeout) to verify credentials without touching the existing session.

### Database (`remotebash/core/database.py`)

SQLite via aiosqlite with `row_factory = aiosqlite.Row`. Two tables:
- `clients` — SSH connection configs (name PK, host, port, user, password, label, enabled, timestamps).
- `audit_log` — full command history (client FK, command, stdout, stderr, exit_code, cwd, duration_ms, success, timestamp). Indexed on `client_name` and `created_at`.

The `open_db()` function creates parent directories if needed and runs schema migration on every open (all DDL uses `IF NOT EXISTS` / `IF NOT EXISTS`).

### Manager (`remotebash/core/manager.py`)

Holds an in-memory `dict[str, RemoteSession]` synchronized with the DB. `load()` reconstructs sessions from `clients` rows. Audit callbacks are registered per-session and write to `audit_log` on every command. `update()` only persists fields in the allowlist `{host, port, user, password, label, enabled}` and always bumps `updated_at`.

### MCP tools (`remotebash/api/tools.py`)

Three tools:
- `remote_shell(client_name, command, timeout=30)` — executes a command, returns `{stdout, stderr, exit_code, cwd}`.
- `data_transfer(client_name, src, dst, direction="remote2local")` — SFTP file transfer, returns `{success, direction, src, dst, size_bytes, duration_ms}`.
- `list_remote_clients()` — returns only **enabled** clients with `{name, host, port, user, cwd, label}`.

Both access the manager via `ctx.lifespan_context["manager"]`.

### REST API (`remotebash/api/routes.py`)

CRUD at `/api/clients` plus `/api/clients/{name}/connect|disconnect|test`. Client names are validated: only alphanumeric, hyphens, underscores. Audit query at `/api/audit` with pagination (`limit`, `offset`) and optional `client_name` filter. Audit deletion supports single entry, by client, or bulk before an ID.

### Web dashboard (`remotebash/web/`)

Two pages: dashboard (`/`) and audit (`/audit`). Both extend `base.html.j2` with a dark Tailwind theme (custom `surface`, `border`, `accent` colors). Tailwind CSS is loaded from CDN (no build step). The dashboard is a SPA — all interactions go through the REST API via vanilla JS (`app.js` and `audit.js`). Templates are split into partials (`header.html.j2`, `add_form.html.j2`, `client_list.html.j2`).

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
