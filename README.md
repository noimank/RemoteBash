# RemoteBash

通过 MCP 将 SSH 连接暴露给 AI 智能体（Claude、Cursor、Codex 等），在远程主机上执行命令。

- **零依赖**：单一静态 Go 二进制，无需任何运行时
- **MCP 原生**：接入 Claude Desktop / Cursor / OpenCode / Codex / Goose
- **常驻 Shell**：PTY 交互式 bash，`cd`/`export`/别名/函数跨命令保留
- **文件传输**：SFTP 上传/下载，自动展开 `~`
- **Web 仪表盘**：可视化管理 SSH 主机，暗色主题
- **浏览器终端**：xterm.js 真实 PTY 终端，支持颜色和交互程序
- **命令审计**：每次执行全量记录至 SQLite

## 快速开始

从 [GitHub Releases](https://github.com/noimank/RemoteBash/releases) 下载二进制，或从源码构建（Go ≥ 1.22）：

```bash
go build -ldflags="-s -w" -o remotebash ./cmd/remotebash/
./remotebash --port 24587
```

浏览器打开 `http://localhost:24587` 进入仪表盘，添加 SSH 主机后即可使用。

## 命令行参数

```
--transport http|sse    传输协议（默认 http）
--host 127.0.0.1        监听地址
--port 24587            监听端口
--db <path>             SQLite 路径（默认 ~/.remotebash/remotebash.db）
--debug                 调试日志
--base-url-prefix       反向代理子路径前缀
```

默认监听 `127.0.0.1`，仅本机可访问。

## 接入 AI 智能体

启动后，在对应客户端中添加 HTTP MCP Server，地址为 `http://localhost:24587/mcp`：

**Claude Desktop / Claude Code**

```bash
claude mcp add --transport http RemoteBash http://localhost:24587/mcp
```

**Cursor** — 在 `.cursor/mcp.json` 中添加：

```json
{ "mcpServers": { "RemoteBash": { "url": "http://localhost:24587/mcp" } } }
```

**OpenCode** — `opencode mcp add`，选择 Remote (Streaming HTTP)，URL 输入上述地址。

**Codex (OpenAI)** — `codex mcp add RemoteBash --url http://localhost:24587/mcp`

**Goose** — `goose configure`，选择 Remote Extension，Endpoint URL 同上。

配置后可获得三个工具：`remote_shell`（执行命令）、`data_transfer`（文件传输）、`list_remote_clients`（列出主机）。

### 安全删除

在仪表盘中为主机开启「安全删除」后，`rm` 将被 shadow 为 mv 至 `/tmp/.rbsh_trash/`，防止误删。使用 `command rm` 或 `/bin/rm` 可绕过。

## Web 仪表盘

- 添加/编辑/删除 SSH 连接配置
- 一键测试连通性
- xterm.js 浏览器终端（弹窗关闭后会话保留）
- 审计日志浏览/筛选/删除

## REST API

```
GET    /api/clients                    列出客户端
POST   /api/clients                    添加客户端
PATCH  /api/clients/{name}             更新配置
DELETE /api/clients/{name}             删除客户端
POST   /api/clients/{name}/test        测试连接
WS     /api/clients/{name}/terminal    浏览器终端
GET    /api/audit                      查询审计日志（分页/过滤）
DELETE /api/audit                      删除审计日志
GET    /health                         健康检查
```

## 架构

```
┌──────────────┐   MCP (HTTP/SSE)   ┌──────────────┐
│  AI 智能体    │ ◄────────────────► │  RemoteBash   │
└──────────────┘                    │  Go 二进制    │
                                    └───────┬────────┘
┌──────────────┐   REST API / WS           │
│  浏览器       │ ◄─────────────────────────┘
└──────────────┘                            │
                                 x/crypto/ssh │ SSH / SFTP
                                              │
                                    ┌────────▼────────┐
                                    │  远程 Linux 主机  │
                                    └─────────────────┘
```

核心依赖：`golang.org/x/crypto/ssh`（SSH 客户端）、`modernc.org/sqlite`（纯 Go SQLite）、`mark3labs/mcp-go`（MCP 服务端）、`nhooyr.io/websocket`（终端桥接）。

每条 MCP 命令运行在**常驻 PTY bash** 上，用私有标记帧界定命令边界，输出自动剥离 ANSI 转义序列。SSH 连接惰性建立，网络异常自动断开并在下次调用时重连。

## 部署

```bash
# 交叉编译 Linux 静态二进制
GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o remotebash ./cmd/remotebash/
scp remotebash server:/usr/local/bin/
ssh server 'remotebash --host 0.0.0.0 --port 24587'
```

配合 systemd 或 supervisord 可实现开机自启。反向代理子路径部署通过 `--base-url-prefix` 参数实现，详见 Web 仪表盘中的使用指南。

## 开发

```bash
go vet ./...
go test -race ./... -count=1
go build -ldflags="-s -w" -o remotebash ./cmd/remotebash/
```

项目结构详见 [CLAUDE.md](CLAUDE.md)。

## 许可证

MIT
