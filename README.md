<p align="center">
  <img src="https://img.shields.io/badge/python-3.12+-blue" alt="Python 3.12+">
  <img src="https://img.shields.io/badge/fastmcp-3.0+-green" alt="FastMCP 3.0+">
  <img src="https://img.shields.io/badge/license-MIT-orange" alt="MIT">
</p>

# RemoteBash

远程 Shell 执行网关 —— 将 SSH 连接暴露为 MCP 工具，让 Claude 等 AI 智能体直接在远程主机上执行命令。

- **MCP 原生**，零配置接入 Claude Desktop
- **Web 仪表盘** 管理多台机器，实时增删启停
- **工作目录持久**，`cd` 后上下文自动保留
- **空闲自动回收**，连接一小时无活动即释放，再次调用透明重连
- **命令审计**，每次执行全量记录：命令、输出、耗时、退出码
- **SQLite 持久化**，重启后客户端配置完好无损

---

## 架构

```
Claude Desktop (MCP)
        │
        ▼
┌───────────────┐     ┌─────────────────┐
│   FastMCP     │────▶│  ConnectionMgr  │──── SSH ────▶ 远程主机群
│  (tools)      │     │  (session pool) │
├───────────────┤     └────────┬────────┘
│   FastAPI     │              │
│  (dashboard)  │     ┌───────▼────────┐
│  (REST API)   │     │    SQLite      │
└───────────────┘     │ (clients+audit)│
                      └────────────────┘
```

- **FastMCP** — 暴露 `RemoteShell()` 与 `ListRemoteClients()` 两个 MCP 工具
- **FastAPI** — 提供仪表盘 & REST 管理接口，与 MCP 共用端口
- **asyncssh** — 异步 SSH 长连接池，心跳保活
- **Jinja2 + Tailwind** — 服务端渲染的暗色仪表盘

---

## 快速开始

```bash
# 安装依赖
uv sync

# 启动（HTTP 传输 — 仪表盘 + MCP 共用端口）
uv run python main.py --port 24587
```

浏览器打开 `http://localhost:24587`，添加你的 SSH 主机，获得一个类似 `prod-web` 的名称，即可通过 MCP 调用。

### 配置 Claude Desktop

```json
{
  "mcpServers": {
    "RemoteBash": {
      "command": "uv",
      "args": [
        "run", "--project", "/path/to/RemoteBash",
        "python", "main.py", "--transport", "http"
      ]
    }
  }
}
```

MCP 端点位于 `http://localhost:24587/mcp`，Claude 连接后即获得 `RemoteShell` 和 `ListRemoteClients` 两个工具。

---

## 功能

### 工作目录持久

每次命令在之前的 CWD 下执行。`cd /var/log` 后紧接 `ls`，看到的是 `/var/log` 的内容。这是通过命令包装实现的——每次执行后捕获 `pwd` 输出，记录为下一次的上下文。

### 懒连接 & 空闲释放

连接不在启动时建立。首次调用 `RemoteShell()` 时自动连接，一小时无活动后自动断开。整个过程对 MCP 调用方透明——智能体感知不到断连与重连。

### 命令审计

每一次 `RemoteShell()` 调用都被记录到 SQLite，包含：客户端、命令、标准输出、标准错误、退出码、工作目录、耗时、时间戳。仪表盘提供分页浏览和按客户端筛选，也可一键清除。

### 启用 / 禁用

暂时不需要的主机可以禁用。禁用的客户端不会出现在 MCP 工具列表中，不会建立连接，但配置保留不变。

### 测试连接

仪表盘上点击 **Test**，异步验证 SSH 凭据可达性，不干扰现有连接。

---

## 配置

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `--transport` | `http` | 传输协议：`http` 或 `sse` |
| `--host` | `0.0.0.0` | 监听地址 |
| `--port` | `24587` | 监听端口 |
| `--db` | `{用户数据}/remotebash/remotebash.db` | SQLite 路径 |
| `--debug` | `false` | 调试日志 |

---

## 项目结构

```
remotebash/
├── app.py              应用入口 & FastAPI/FastMCP 装配
├── config.py           服务配置
├── core/
│   ├── database.py     SQLite 层 & 迁移
│   ├── session.py      SSH 会话 & CWD 追踪
│   └── manager.py      多会话注册表
├── api/
│   ├── tools.py        MCP 工具定义
│   ├── routes.py       REST 接口
│   └── audit_router.py 审计页面路由
└── web/
    ├── dashboard.py    仪表盘路由
    ├── templates/      Jinja2 模板
    └── static/         JavaScript 前端
```

---

## 许可证

MIT
