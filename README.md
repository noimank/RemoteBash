# RemoteBash

通过 MCP 将 SSH 连接暴露给 Claude 等 AI 智能体，在远程主机上执行命令。

- **MCP 原生**：一键接入 Claude Desktop，无需配置
- **Web 仪表盘**：可视化管理多台 SSH 主机
- **工作目录持久**：`cd` 后上下文自动保留
- **命令审计**：每次执行全量记录至 SQLite

## 快速开始

```bash
# 安装并启动服务器
uv sync
uv run python main.py
```

浏览器打开 `http://localhost:24587`，添加 SSH 主机。

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
| `RemoteShell` | 在远程主机执行命令 |
| `ListRemoteClients` | 列出所有已启用的主机 |

## 命令行参数

```
uv run python main.py [OPTIONS]

  --transport http|sse   传输协议，默认 http
  --host 0.0.0.0        监听地址
  --port 24587          监听端口
  --db <path>           SQLite 路径
  --debug               调试日志
```

## 管理接口

仪表盘提供 REST API 管理客户端：

```
GET    /api/clients           列出所有客户端
POST   /api/clients           添加客户端
PUT    /api/clients/{name}    更新客户端配置
DELETE /api/clients/{name}    删除客户端
POST   /api/clients/{name}/test     测试 SSH 连接
GET    /api/audit             查询命令审计日志
DELETE /api/audit             清除审计日志
```

## 许可证

MIT
