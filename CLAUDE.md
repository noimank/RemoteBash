# CLAUDE.md

This file provides guidance to Claude Code when working with code in this repository.

## Commands

```bash
# Build
go build -o remotebash ./cmd/remotebash/

# Build stripped
go build -ldflags="-s -w" -o remotebash ./cmd/remotebash/

# Cross-compile (output to build/)
./scripts/build.sh                  # Linux/macOS
.\scripts\build.ps1                 # Windows

# Run
./remotebash --port 24587           # HTTP transport (dashboard + MCP on same port)
./remotebash --transport sse --port 24587
./remotebash --debug                # slog LevelDebug

# Test
go vet ./...
go test -race ./... -count=1        # race detector recommended
```

Tests live alongside source under `internal/`. Use `go get <pkg>` / `go mod tidy` for dependencies.

## Architecture

```
cmd/remotebash/main.go     CLI entry point
internal/
  config/                   Flag parsing, ServerConfig, Validate()
  server/                   HTTP server assembly, routing, recovery middleware
  api/                      REST handlers (clients CRUD, audit, logs) + WebSocket terminal
  mcp/                      MCP server + 3 tools (remote_shell, list_remote_clients, data_transfer)
  ssh/                      PersistentShell (PTY bash), RemoteSession (SSH lifecycle), relay, safe_rm
  manager/                  ConnectionManager — session registry, lifecycle, audit, warm-up
  database/                 Pure-Go SQLite (modernc.org/sqlite): schema, CRUD, slog→DB handler
  models/                   Shared types (Client, AuditEntry, API request/response)
web/                        //go:embed templates + vendored static files (xterm.js, Tailwind)
```

A single `net/http.ServeMux` hosts everything on one port: dashboard, audit, guide, logs, REST API, MCP, WebSocket terminal, and static files. Recovery middleware catches panics. `BASE_URL_PREFIX` env/flag mounts the app under a sub-path for reverse-proxy deployment.

### Persistent interactive shell

Commands run on a **single long-lived PTY bash shell**, not `/bin/bash -c` per call:

- `Run(command, timeout)` wraps the command between sentinel tokens; the reader goroutine scans for the done sentinel and captures exit code + cwd.
- MCP path: echo disabled, wide PTY (1000 cols), output cleaned via `normalizeOutput` (strip ANSI, normalise line endings, strip cursor-movement sequences).
- Browser terminal path: echo on, raw byte pass-through via `FeedRaw` + `AttachTap` — xterm.js renders colours/cursors.
- Runaway output bounded at 8 MiB (64 KiB head + notice + 32 KiB tail).
- Timeout captures partial output then tears down the shell.

**PersistentShell concurrency**: a single `sync.Mutex` protects all mutable state. `Run()` dispatches under the lock, then waits on the result channel *without* the lock. The `readerLoop` goroutine acquires the lock briefly to append bytes and scan.

### RemoteSession lock order (never violate)

```
execLock → connectLock → shellLock → connMu
```

- `execLock` serialises `Exec` callers
- `connectLock` serialises Connect vs Disconnect
- `shellLock` guards shell, shellType, homeCache, cwd
- `connMu` (RWMutex) guards conn + keepaliveDone; all reads via `snapshotConn()`

The persistent shell's own `mu` is independent of this chain.

### Connection lifecycle

Lazy connect on first `Exec()`. SSH errors trigger `Disconnect()`; next call reconnects. Two-layer keepalive: TCP `SO_KEEPALIVE` (15s) + SSH global request every 30s (`wantReply=false`). `WarmUp()` async-connects enabled clients at startup.

### Database

Pure-Go SQLite (no CGO) with WAL + foreign_keys + busy_timeout, single-writer pool. Three tables: `clients`, `audit_log`, `server_log`. Schema migration on every open (`IF NOT EXISTS`). Column names in dynamic UPDATE validated against whitelist.
