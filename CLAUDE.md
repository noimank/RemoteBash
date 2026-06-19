# CLAUDE.md

This file provides guidance to Claude Code when working with code in this repository.

## Commands

```bash
# Build the server (single static binary, no runtime deps)
go build -o remotebash ./cmd/remotebash/

# Build with stripped debug info (smaller binary)
go build -ldflags="-s -w" -o remotebash ./cmd/remotebash/

# Cross-compile for all platforms (output to build/)
./scripts/build.sh              # Linux/macOS
.\scripts\build.ps1             # Windows PowerShell

# Cross-compile specific platforms only
./scripts/build.sh dev build "linux/amd64,windows/amd64"

# Run the server (HTTP transport — dashboard + MCP on same port)
./remotebash --port 24587

# Run with SSE transport
./remotebash --transport sse --port 24587

# Run with debug logging (slog LevelDebug)
./remotebash --debug

# Custom database path
./remotebash --db /path/to/remotebash.db

# Vet and test everything
go vet ./...
go test ./... -count=1

# Cross-compile for Linux
GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o remotebash ./cmd/remotebash/
```

Tests live alongside source under `internal/`. Use `go get <pkg>` / `go mod tidy` to manage dependencies.

## Architecture

### Project layout

```
cmd/remotebash/main.go     CLI entry point (flag parsing, slog setup)
internal/
  config/config.go          ServerConfig, flag parsing, Validate()
  server/server.go          HTTP server assembly, routing, health, recovery
  api/
    routes.go               REST API handlers (clients CRUD, audit)
    ws_terminal.go          WebSocket xterm.js terminal bridge
  mcp/server.go             MCP server + tool registration (3 tools)
  ssh/
    shell.go                PersistentShell — PTY bash, framing, ANSI rendering
    client.go               RemoteSession — SSH connect, shell lifecycle, SFTP
    relay.go                Jump-host TCP relay (socat/nc/python3 fallback)
    safe_rm.go              bash safe-delete shim constant
  manager/
    manager.go              ConnectionManager — session registry, lifecycle, audit
  database/sqlite.go        SQLite schema, migrations, CRUD
  models/models.go          Shared data types (Client, AuditEntry, API request/response)
web/
  embed.go                  //go:embed templates + static files
  templates/                Go html/template partials
  static/                   Vendor JS/CSS (xterm.js, tailwind.js, app.js, …)
```

### Server assembly (`internal/server/server.go`)

A single `net/http.ServeMux` (Go 1.22+ method routing) hosts everything on one port: dashboard at `/`, audit at `/audit`, REST API at `/api/*`, MCP at `/mcp`, WebSocket terminal at `/api/clients/{name}/terminal`, static files at `/static/*`, and `/health` for readiness probes. The MCP server is created via `mark3labs/mcp-go` and mounted as an `http.Handler`. Recovery middleware catches panics in handlers.

### Persistent interactive shell (`internal/ssh/shell.go`, `internal/ssh/client.go`)

Commands run on a **single long-lived interactive bash shell with an allocated PTY**, not a fresh `/bin/bash -c` per call. `PersistentShell` manages this:

- Opens the shell via `gossh.Session.RequestPty("xterm-256color", rows, cols, modes)` then `Session.Shell()`.
- `Run(command, timeout)` wraps the command between `__RBSH_START__:<token>__` and `__RBSH_DONE__:<token>:<exit_code>:CWD:<cwd>__` sentinels and waits for the done token via a regex scan in the reader goroutine.
- The wrapper temporarily disables `errexit` around command execution and restores it afterwards, so non-zero commands still produce the done sentinel.
- Output is ANSI-stripped via `renderTerminalText` (a full terminal cell-grid renderer that handles CRLF, CSI erase/cursor, tabs, backspace, wide characters). Echoed wrapper input is removed defensively.
- `FeedRaw` + `AttachTap` provide raw byte pass-through for the in-browser terminal (colours preserved, xterm.js renders).
- Command timeout captures partial output, then tears down the shell so the next caller rebuilds it.

**Concurrency model**: A single `sync.Mutex` (`mu`) protects all mutable state (`buf`, `pending`, `ready`, `closed`). `Run()` holds `mu` while setting `pending`, then releases it while waiting on the result channel, then re-acquires. The `readerLoop` goroutine acquires `mu` briefly to append bytes and call `scanLocked()`. This avoids the deadlock of holding `mu` across I/O waits.

`RemoteSession.EnsureShell()` lazily starts the shell on first use and rebuilds it if the process died or `safe_rm` was toggled. `ExecLock` (`sync.Mutex`) serialises concurrent `Exec()` callers. The `safe_rm` shim is injected at shell start as an inline bash function.

### Browser terminal (`internal/api/ws_terminal.go`)

A WebSocket at `/api/clients/{name}/terminal` bridges xterm.js to a separate `PersistentShell` owned by `ConnectionManager.terminals`. Uses `nhooyr.io/websocket` with binary frame passthrough and JSON resize commands. WebSocket ping/pong every 30s keeps the connection alive through proxies. The terminal shell is **independent** of the MCP exec shell but reuses the same SSH connection. Shell state survives WebSocket disconnect.

### Connection lifecycle (`internal/ssh/client.go`, `internal/manager/manager.go`)

- **Lazy connect** — `Exec()` calls `Connect()` only when not already connected.
- **Error recovery** — SSH errors during execution trigger `Disconnect()`; the next call reconnects.
- **`TestConnection()`** — calls `Connect()` which stays alive for subsequent use.
- **Host key logging** — host keys are logged (SHA256 fingerprint) and accepted. For production, add a `known_hosts` verifier.

### Database (`internal/database/sqlite.go`)

Pure-Go SQLite via `modernc.org/sqlite` (no CGO). Two tables:
- `clients` — SSH connection configs (name PK, host, port, user, password, label, enabled, safe_rm, via, timestamps).
- `audit_log` — full command history (client FK, command, output, exit_code, cwd, duration_ms, success, timestamp). Indexed on `client_name` and `created_at`.

Schema migration runs on every open (all DDL uses `IF NOT EXISTS`). Column names in dynamic UPDATE are validated against an `allowedColumns` whitelist.

### Manager (`internal/manager/manager.go`)

Holds a `map[string]*RemoteSession` synchronised with the DB, plus a `map[string]*PersistentShell` of browser-terminal shells. `Load()` reconstructs sessions from `clients` rows. Audit callbacks write to `audit_log` on every command. `Update()` persists to DB first, then updates in-memory state. Jump-host cycle detection uses DFS through the `via` chain.

### MCP tools (`internal/mcp/server.go`)

Three tools registered via `mcp-go`:
- `remote_shell(client_name, command, timeout=30)` — returns `{output, exit_code, cwd}`.
- `list_remote_clients()` — returns only enabled clients with `{client_name, host, port, user, cwd, safe_rm}`.
- `data_transfer(client_name, src, dst, direction="local2remote")` — SFTP file transfer.

### REST API (`internal/api/routes.go`)

CRUD at `/api/clients` plus `/api/clients/{name}/connect|disconnect|test`. Client names validated: alphanumeric, hyphens, underscores. Audit at `/api/audit` with pagination and filters. Request body limited to 16KB. Error responses use `models.ErrorResponse`.

### Web dashboard (`web/`)

Two pages: dashboard (`/`) and audit (`/audit`). Dark Tailwind theme with custom `surface`/`border`/`accent` colors. Tailwind CSS and xterm.js are vendored locally (no build step, no CDN). The dashboard is a SPA — all interactions go through the REST API via vanilla JS (`app.js`, `audit.js`, `terminal.js`). Templates are Go `html/template` partials. All static assets and templates are embedded in the binary via `//go:embed`.

## Key Go dependencies

| Package | Role |
|---------|------|
| `github.com/mark3labs/mcp-go` | MCP server (tool registration, HTTP/SSE transport) |
| `golang.org/x/crypto/ssh` | SSH client (Dial, Session, PTY, SFTP) |
| `github.com/pkg/sftp` | SFTP client (upload/download) |
| `modernc.org/sqlite` | Pure-Go SQLite driver (no CGO) |
| `nhooyr.io/websocket` | WebSocket (xterm.js terminal bridge) |

Standard library: `net/http` (router/mux), `html/template` (templates), `log/slog` (structured logging), `embed` (static assets).
