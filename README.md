# RemoteBash

通过 MCP 将 SSH 连接暴露给 Claude 等 AI 智能体，在远程主机上执行命令。

- **零依赖二进制**：单一静态 Go 二进制，无需 Python、uv、pip 或任何运行时
- **MCP 原生**：一键接入 Claude Desktop / Cursor / OpenCode / Codex / Goose
- **文件传输**：SFTP 上传/下载，自动展开 `~` 路径
- **Web 仪表盘**：可视化管理多台 SSH 主机，Tailwind 暗色主题
- **浏览器终端**：内置 xterm.js 终端，直接在浏览器里操作远程 PTY bash
- **会话状态持久**：常驻交互式 shell，`cd`/`export`/别名/函数跨命令保留
- **命令审计**：每次执行全量记录至 SQLite

## 目录

- [快速开始](#快速开始)
- [命令行参数](#命令行参数)
- [接入各 AI 智能体](#接入各-ai-智能体)
- [Web 仪表盘](#web-仪表盘)
- [管理接口](#管理接口)
- [开发](#开发)
- [架构](#架构)
- [常见问题](#常见问题)
- [许可证](#许可证)

## 快速开始

### 下载二进制（推荐）

从 GitHub Releases 下载对应平台的二进制，直接运行：

```bash
# Linux x86_64
./remotebash --port 24587

# Windows
remotebash.exe --port 24587
```

### 从源码构建

需要 **Go ≥ 1.22**：

```bash
git clone https://github.com/luyuankang/remotebash.git
cd remotebash
go build -o remotebash ./cmd/remotebash/
./remotebash --port 24587
```

### 交叉编译

```bash
# Linux 静态二进制（在任意平台编译）
GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o remotebash ./cmd/remotebash/

# macOS (Apple Silicon)
GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o remotebash ./cmd/remotebash/

# Windows
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o remotebash.exe ./cmd/remotebash/
```

启动后浏览器打开 `http://localhost:24587` 即可进入仪表盘，添加 SSH 主机后开始使用。

## 命令行参数

```
remotebash [OPTIONS]

  --transport http|sse   传输协议，默认 http
  --host 127.0.0.1      监听地址
  --port 24587          监听端口
  --db <path>           SQLite 路径（默认 ~/.remotebash/remotebash.db）
  --debug               调试日志
```

> **安全提示**：默认监听 `127.0.0.1`，仅本机可访问。如需让局域网内其他设备访问，请使用 `--host 0.0.0.0`。

## 接入各 AI 智能体

以下均以 **HTTP 模式**（默认端口 24587）为例。

### Claude Desktop / Claude Code

```bash
# CLI 一键添加
claude mcp add --transport http RemoteBash http://localhost:24587/mcp
```

或在 `.mcp.json` / Claude Desktop 配置中：

```json
{
  "mcpServers": {
    "RemoteBash": {
      "type": "http",
      "url": "http://localhost:24587/mcp"
    }
  }
}
```

### Cursor

在 `.cursor/mcp.json` 或 Cursor Settings → MCP 中添加：

```json
{
  "mcpServers": {
    "RemoteBash": {
      "url": "http://localhost:24587/mcp"
    }
  }
}
```

### OpenCode

```bash
opencode mcp add
# Name 输入: RemoteBash。按交互提示选择 Remote (Streaming HTTP)
# URL 输入: http://localhost:24587/mcp
```

或手动在 `opencode.json` 中：

```json
{
  "mcp": {
    "RemoteBash": {
      "type": "remote",
      "url": "http://localhost:24587/mcp"
    }
  }
}
```

### Codex (OpenAI)

```bash
codex mcp add RemoteBash --url http://localhost:24587/mcp
```

或手动在 `~/.codex/config.toml` 中：

```toml
[mcp_servers.RemoteBash]
url = "http://localhost:24587/mcp"
```

### Goose

```bash
goose configure
# 选择 "Remote Extension (Streaming HTTP)"
# Endpoint URL: http://localhost:24587/mcp
```

### CLI 一键添加汇总

| AI 智能体 | CLI 命令 |
|-----------|---------|
| Claude Code | `claude mcp add --transport http RemoteBash http://localhost:24587/mcp` |
| OpenCode | `opencode mcp add`（交互式引导） |
| Codex (OpenAI) | `codex mcp add RemoteBash --url http://localhost:24587/mcp` |
| Goose | `goose configure`（交互式引导） |

配置后可获得以下工具：

| 工具 | 说明 |
|------|------|
| `remote_shell` | 在远程主机执行 shell 命令，工作目录跨命令持久 |
| `data_transfer` | SFTP 文件传输（上传/下载），自动展开 `~` |
| `list_remote_clients` | 列出所有已启用的 SSH 主机 |

### 安全删除（safe_rm）

开启后 shadow 原生的 `rm` 命令，将删除改为 mv 至 `/tmp/.rbsh_trash/` 下按时间戳和 PID 分类的回收目录。在仪表盘添加或编辑主机时勾选「安全删除」即可。使用 `command rm` 或 `/bin/rm` 可绕过 shim。

> `/tmp/.rbsh_trash/` 中的文件不会自动清理，请定期手动删除。

## Web 仪表盘

启动后访问 `http://localhost:24587`：

- **主机管理**：添加 / 编辑 / 删除 SSH 连接配置
- **连接测试**：一键测试 SSH 连通性
- **浏览器终端**：基于 xterm.js 的完整交互式终端，分配真实 PTY，支持颜色和交互程序。关闭弹窗后会话保留，重新打开可继续
- **审计日志**：浏览/筛选/删除命令历史记录
- **暗色主题**：Tailwind 暗色主题，移动端友好

## 管理接口

RESTful API：

```
GET    /api/clients              列出所有客户端
POST   /api/clients              添加客户端
PATCH  /api/clients/{name}       更新客户端配置
DELETE /api/clients/{name}       删除客户端
POST   /api/clients/{name}/connect     建立 SSH 连接
POST   /api/clients/{name}/disconnect  断开连接
POST   /api/clients/{name}/test        测试连接
WS     /api/clients/{name}/terminal    浏览器终端（双向 PTY 字节流）
GET    /api/audit                查询审计日志（分页/过滤）
DELETE /api/audit                删除审计日志（单条/按主机/批量）
GET    /health                   健康检查
```

## 开发

```bash
# 运行所有测试
go test ./... -count=1

# 代码检查
go vet ./...

# 带调试日志运行
go run ./cmd/remotebash/ --debug

# 构建
go build -ldflags="-s -w" -o remotebash ./cmd/remotebash/
```

### 项目结构

```
cmd/remotebash/main.go     CLI 入口
internal/
  config/config.go          ServerConfig, flag 解析
  server/server.go          HTTP 服务器组装、健康检查、recovery
  api/
    routes.go               REST API 处理器
    ws_terminal.go          WebSocket 终端桥接
  mcp/server.go             MCP 服务器 + 工具注册
  ssh/
    shell.go                PersistentShell — PTY bash、帧解析、ANSI 渲染
    client.go               RemoteSession — SSH 连接、shell 生命周期、SFTP
    relay.go                跳板机 TCP 中继（socat/nc/python3 回退）
    safe_rm.go              bash 安全删除 shim
  manager/manager.go        ConnectionManager — 会话注册、生命周期、审计
  database/sqlite.go        SQLite schema、迁移、CRUD
  models/models.go          共享数据类型
web/
  embed.go                  //go:embed 模板 + 静态资源
  templates/                Go html/template 分部
  static/                   前端资源（xterm.js, Tailwind, app.js, …）
```

## 架构

```
┌──────────────┐   MCP (HTTP/SSE)   ┌──────────────┐
│  AI 智能体    │ ◄────────────────► │  RemoteBash   │
│  (Claude等)  │                    │  Go 二进制    │
└──────────────┘                    │  net/http     │
                                    │  + MCP Server │
┌──────────────┐   REST API         │  + Dashboard  │
│  浏览器       │ ◄────────────────► │               │
│  (仪表盘)     │                    └───────┬────────┘
└──────────────┘                            │
                                 x/crypto/ssh │ SSH / SFTP
                                              │
                                    ┌────────▼────────┐
                                    │  远程 Linux 主机  │
                                    └─────────────────┘
```

- **Go `net/http`** — HTTP 路由（Go 1.22+ 方法路由）、静态文件、WebSocket 升级
- **`mark3labs/mcp-go`** — MCP 协议端点 (`/mcp`)，支持 HTTP 和 SSE 两种传输
- **`golang.org/x/crypto/ssh`** — 原生 SSH 客户端（Dial、Session、PTY、SFTP）
- **`modernc.org/sqlite`** — 纯 Go SQLite（无 CGO）
- **`nhooyr.io/websocket`** — WebSocket（xterm.js 终端桥接）

### 会话状态持久（常驻 PTY shell）

为每个 SSH 连接维护一个**常驻交互式 bash shell（分配真实 PTY）**，`cd`、`export`、shell 函数、别名、`umask`、`history` 等所有 shell 状态跨命令持久。输出会剥离 ANSI 颜色码，让 AI 拿到干净文本。

**命令边界机制**：每条 MCP 命令用带一次性 token 的私有 `__RBSH_START__` / `__RBSH_DONE__` 标记包住命令输出，读到完整标记帧即知命令结束、退出码与新 CWD。

### 连接管理

- **惰性连接** — 只在真正执行命令时才建立 SSH 连接
- **断线恢复** — 网络错误触发断开，下次调用自动重连
- **shell 按需重建** — PTY 进程异常退出后自动重建
- **Host key 日志** — 主机密钥通过 SHA256 指纹记录

## 常见问题

### Go 版本依赖？

Go ≥ 1.22（使用了 `http.ServeMux` 方法路由）。编译工具链通常由 `go.mod` 中的 `go` 指令自动管理。

### 如何让 AI 智能体只能访问特定主机？

在仪表盘中将无需访问的主机设为**禁用**——`list_remote_clients` 只返回已启用的主机。

### 如何迁移 Python 版本的数据库？

数据库 schema 完全兼容。直接将 `~/.remotebash/remotebash.db` 复制到 Go 版本（或保持同一路径），所有配置和审计日志自动可用。

### 如何部署到远程服务器？

```bash
# 交叉编译 Linux 静态二进制
GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o remotebash ./cmd/remotebash/

# 上传并运行
scp remotebash server:/usr/local/bin/
ssh server 'remotebash --host 0.0.0.0 --port 24587'
```

配合 systemd 或 supervisord 可实现开机自启。

## 许可证

MIT
