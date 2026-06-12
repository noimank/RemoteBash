# RemoteBash

通过 MCP 将 SSH 连接暴露给 Claude 等 AI 智能体，在远程主机上执行命令。

- **MCP 原生**：一键接入 Claude Desktop，无需配置
- **文件传输**：通过 SFTP 上传/下载，自动展开 `~` 路径
- **Web 仪表盘**：可视化管理多台 SSH 主机
- **工作目录持久**：`cd` 后上下文自动保留
- **命令审计**：每次执行全量记录至 SQLite

## 目录

- [环境准备](#环境准备)
- [快速开始](#快速开始)
- [命令行参数](#命令行参数)
- [接入各 AI 智能体](#接入各-ai-智能体)
- [Web 仪表盘](#web-仪表盘)
- [管理接口](#管理接口)
- [架构](#架构)
- [常见问题](#常见问题)
- [许可证](#许可证)

## 环境准备

RemoteBash 需要 **Python ≥ 3.12**，推荐使用 [uv](https://docs.astral.sh/uv/) 管理运行环境。

### 安装 uv

```bash
# macOS / Linux
curl -LsSf https://astral.sh/uv/install.sh | sh

# Windows (PowerShell)
powershell -ExecutionPolicy ByPass -c "irm https://astral.sh/uv/install.ps1 | iex"

# 或使用 pip 安装
pip install uv
```

验证安装：

```bash
uv --version
```

## 快速开始

### 一行启动（无需克隆仓库）

```bash
uvx --from git+http://git.cs2025.com/luyuankang/remotebash.git remotebash
```

`uvx` 会自动拉取仓库、安装依赖并启动服务。浏览器打开 `http://localhost:24587` 即可进入仪表盘。

### 使用 SSH 拉取

```bash
uvx --from git+ssh://git@git.cs2025.com/luyuankang/remotebash.git remotebash
```

> **注意**：使用 SSH 方式需要先配置好 SSH key 并添加到 git.cs2025.com 账户。

### 克隆后本地运行

```bash
git clone git@git.cs2025.com:luyuankang/remotebash.git
cd remotebash
uv sync
uv run remotebash
```

### 自定义参数

```bash
# 指定端口和传输协议
uvx --from git+http://git.cs2025.com/luyuankang/remotebash.git remotebash \
  --port 8080 --transport sse

# 自定义数据库路径
uv run remotebash --db ./my_remotebash.db

# 开启调试日志
uv run remotebash --debug
```

启动后在仪表盘中添加你的 SSH 主机，即可开始使用。

## 命令行参数

```
remotebash [OPTIONS]

  --transport http|sse   传输协议，默认 http
  --host 127.0.0.1      监听地址
  --port 24587          监听端口
  --db <path>           SQLite 路径（默认 ~/.remotebash/remotebash.db）
  --debug               调试日志
```

> **安全提示**：默认监听 `127.0.0.1`，仅本机可访问。如需让局域网内其他设备访问仪表盘和 MCP 端点，请使用 `--host 0.0.0.0`。

## 接入各 AI 智能体

以下均以 **HTTP 模式**（默认端口 24587）为例。SSE 模式只需将 `--transport` 改为 `sse`，端点不变。

### Claude Desktop / Claude Code

```bash
# Claude Code 命令行添加
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

在 `opencode.json` 中配置：

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

在 `~/.codex/config.toml` 中添加：

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

---

配置后可获得以下工具：

| 工具 | 说明 |
|------|------|
| `RemoteShell` | 在远程主机执行命令，自动追踪工作目录 |
| `DataTransfer` | SFTP 文件传输（上传/下载），支持通配符和自动展开 `~` |
| `ListRemoteClients` | 列出所有已启用的 SSH 主机 |

## Web 仪表盘

启动后访问 `http://localhost:24587`：

- **主机管理**：添加 / 编辑 / 删除 SSH 连接配置
- **连接测试**：一键测试 SSH 连通性，无需手动执行命令
- **连接状态**：实时显示每台主机是否在线
- **快速命令**：直接在仪表盘中向任意主机发送命令

仪表盘采用暗色主题，对移动端友好。

## 管理接口

仪表盘提供 RESTful API 管理客户端和审计日志：

```
GET    /api/clients              列出所有客户端
POST   /api/clients              添加客户端
PUT    /api/clients/{name}       更新客户端配置
DELETE /api/clients/{name}       删除客户端
POST   /api/clients/{name}/test  测试 SSH 连接
POST   /api/audit                查询命令审计日志
DELETE /api/audit                清除审计日志（支持单条 / 按主机 / 批量删除）
```

## 架构

```
┌──────────────┐   MCP (HTTP/SSE)   ┌──────────────┐
│  AI 智能体    │ ◄────────────────► │  RemoteBash   │
│  (Claude等)  │                    │  FastMCP      │
└──────────────┘                    │  + FastAPI    │
                                    │  + 仪表盘      │
┌──────────────┐   REST API         │               │
│  浏览器       │ ◄────────────────► │               │
│  (仪表盘)     │                    └───────┬────────┘
└──────────────┘                            │
                                     asyncssh │ SSH / SFTP
                                             │
                                    ┌────────▼────────┐
                                    │  远程 Linux 主机  │
                                    └─────────────────┘
```

- **FastMCP** 提供 MCP 协议端点 (`/mcp`)，供 AI 智能体调用
- **FastAPI** 提供 Web 仪表盘和 REST 管理 API
- **asyncssh** 管理与远程主机的 SSH 连接（惰性连接、空闲超时、断线重连）
- **SQLite** 存储主机配置和完整命令审计日志

### 工作目录追踪

`cd` 的效果会跨命令保留——在下一条 `RemoteShell` 命令中继续生效。实现方式是在每个命令前后插入轻量的 Shell 片段来追踪 `$PWD`，而非开启持久化 Shell 会话。

### 连接管理

- **惰性连接**：只在真正执行命令时才建立 SSH 连接
- **空闲断开**：超过 1 小时无活动自动断开，下次使用时自动重连
- **断线恢复**：执行期间任何网络错误都会触发断开并允许下次自动重连

## 常见问题

### 如何让 AI 智能体只能访问特定主机？

在仪表盘中添加主机后，默认处于**启用**状态。如需限制，可将不需要的主机设为**禁用**——`ListRemoteClients` 只返回已启用的主机。

### 命令审计日志保存在哪里？

默认保存在 `~/.remotebash/remotebash.db`（SQLite）。可通过 `--db` 参数指定其他路径。

### 如何在多台机器上共享配置？

将 `~/.remotebash/remotebash.db` 复制到目标机器，或通过 `--db` 指向共享目录中的数据库文件。

### 支持哪些 SSH 认证方式？

当前支持密码认证。密钥认证可以通过 SSH agent 转发实现。

## 许可证

MIT
