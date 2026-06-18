# RemoteBash

通过 MCP 将 SSH 连接暴露给 Claude 等 AI 智能体，在远程主机上执行命令。

- **MCP 原生**：一键接入 Claude Desktop，无需配置
- **文件传输**：通过 SFTP 上传/下载，自动展开 `~` 路径
- **Web 仪表盘**：可视化管理多台 SSH 主机
- **浏览器终端**：内置 xterm.js 终端，直接在浏览器里操作远程主机，体验与本地一致
- **会话级状态持久**：常驻交互式 PTY shell，`cd`、`export`、别名、函数等全部跨命令保留
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
| `remote_bash` | 在远程主机执行 bash 命令，工作目录跨命令持久 |
| `data_transfer` | SFTP 文件传输（上传/下载），支持通配符和自动展开 `~` |
| `list_remote_clients` | 列出所有已启用的 SSH 主机 |

### 安全删除（safe_rm）

RemoteBash 内置了 `safe_rm` 安全删除机制——开启后会 shadow 原生的 `rm` 命令，将删除操作改为 mv 至 `/tmp/.rbsh_trash/` 下按时间戳和 PID 分类的回收目录，而非直接删除文件。

**特性：**
- 每次 `rm` 调用创建独立回收目录（`<epoch>_<pid>`），方便按操作批次恢复
- 支持标准 `rm` 选项（如 `-rf`、`-v` 等），选项会原样传递
- mv 失败时（如跨设备）自动回退为真正的 `rm`，并输出提示，不会静默失败
- 需要绕过 shim 时，使用 `command rm` 或 `/bin/rm` 即可调用原生 `rm`

**按需开启：** 在仪表盘添加或编辑主机时勾选「安全删除」即可。`list_remote_clients` 返回结果中也会包含 `safe_rm` 字段，AI 智能体可据此感知该主机是否启用了安全删除。

> **注意**：`/tmp/.rbsh_trash/` 中的文件不会自动清理，请定期检查并手动删除不需要的回收文件。

## Web 仪表盘

启动后访问 `http://localhost:24587`：

- **主机管理**：添加 / 编辑 / 删除 SSH 连接配置
- **连接测试**：一键测试 SSH 连通性，无需手动执行命令
- **连接状态**：实时显示每台主机是否在线
- **浏览器终端**：点击「终端」按钮即可打开一个完整的交互式终端（基于 xterm.js），分配真实 PTY，支持颜色、交互程序（`top`、`vim` 等），关闭弹窗后会话状态保留，重新打开可继续

仪表盘采用暗色主题，对移动端友好。

## 管理接口

仪表盘提供 RESTful API 管理客户端和审计日志：

```
GET    /api/clients              列出所有客户端
POST   /api/clients              添加客户端
PUT    /api/clients/{name}       更新客户端配置
DELETE /api/clients/{name}       删除客户端
POST   /api/clients/{name}/test  测试 SSH 连接
WS     /api/clients/{name}/terminal  浏览器终端（双向 PTY 字节流，由 xterm.js 消费）
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

### 会话状态持久（常驻 PTY shell）

RemoteBash 为每个 SSH 连接维护一个**常驻的交互式 bash shell（分配真实 PTY）**，而不是每条命令 fork 一个一次性的 `/bin/bash -c`。这让通过 `remote_bash` 工具执行命令的体验与直接登录远程机器**完全一致**：

- `cd`、`export`、shell 函数、别名、`umask`、`history` 等**所有 shell 状态**跨命令持久
- 程序被分配 PTY，「以为自己在真终端」→ 颜色输出、`isatty()` 判断、交互式提示（`read`、`top`、`vim`）行为全部正确
- 输出会剥离 ANSI 颜色码，让 AI 拿到干净文本

**命令边界机制**：PTY 是无界的字节流，没有天然的「一条命令结束」信号。RemoteBash 不解析或改写远端 `PS1`；每条 MCP 命令会在同一个 bash 中执行，并用带一次性 token 的私有 start/done 标记包住命令输出。读到完整标记帧即知命令结束、退出码与新 CWD。命令超时会重置该 shell，下次调用自动重建。

### 连接管理

- **惰性连接**：只在真正执行命令时才建立 SSH 连接
- **空闲断开**：超过 1 小时无活动自动断开，下次使用时自动重连
- **断线恢复**：执行期间任何网络错误都会触发断开并允许下次自动重连

## 常见问题

### 如何让 AI 智能体只能访问特定主机？

在仪表盘中添加主机后，默认处于**启用**状态。如需限制，可将不需要的主机设为**禁用**——`list_remote_clients` 只返回已启用的主机。

### 命令审计日志保存在哪里？

默认保存在 `~/.remotebash/remotebash.db`（SQLite）。可通过 `--db` 参数指定其他路径。

### 如何在多台机器上共享配置？

将 `~/.remotebash/remotebash.db` 复制到目标机器，或通过 `--db` 指向共享目录中的数据库文件。

### 支持哪些 SSH 认证方式？

当前支持密码认证。密钥认证可以通过 SSH agent 转发实现。

## 许可证

MIT
