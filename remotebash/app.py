"""App factory — FastAPI + FastMCP ASGI application.

Usage:
    uv run python main.py --transport http --port 8000
    uv run python main.py --transport sse  --port 9090
"""

import argparse
import logging
from contextlib import asynccontextmanager
from pathlib import Path

logger = logging.getLogger("remotebash")

from fastapi import FastAPI
from fastapi.staticfiles import StaticFiles
from fastmcp import FastMCP
from fastmcp.utilities.lifespan import combine_lifespans

from .api.audit_router import router as audit_router
from .api.routes import router as api_router
from .api.tools import register_tools
from .config import DEFAULT_DB, ServerConfig
from .core.database import open_db
from .core.manager import ConnectionManager
from .web.dashboard import router as dashboard_router

_STATIC = Path(__file__).parent / "web" / "static"

# ═══════════════════════════════════════════════════════════════════════
# FastMCP server
# ═══════════════════════════════════════════════════════════════════════

_server_manager: ConnectionManager | None = None


@asynccontextmanager
async def _mcp_lifespan(server: FastMCP):
    yield {"manager": _server_manager}


mcp = FastMCP("RemoteBash", lifespan=_mcp_lifespan)
register_tools(mcp)

# ═══════════════════════════════════════════════════════════════════════
# App factory
# ═══════════════════════════════════════════════════════════════════════

def _mcp_asgi(config: ServerConfig):
    return mcp.http_app(path="/", transport=config.transport)


@asynccontextmanager
async def _app_lifespan(app: FastAPI):
    global _server_manager
    db = await open_db(app.state.db_path)
    _server_manager = ConnectionManager(db)
    await _server_manager.load()
    app.state.manager = _server_manager

    # Print startup info to stderr for visibility alongside uvicorn logs
    import sys
    print(f"  📂 数据库：{app.state.db_path}", file=sys.stderr)
    print(f"  🌐 仪表盘：http://localhost:{app.state.port}", file=sys.stderr)
    print(f"  🔌 MCP 端点：http://localhost:{app.state.port}/mcp（传输：{app.state.transport}）", file=sys.stderr)
    print(f"  🖥️  已加载 {len(_server_manager._sessions)} 个客户端", file=sys.stderr)
    logger.info("RemoteBash 就绪 — 仪表盘 http://localhost:%d  MCP http://localhost:%d/mcp",
                 app.state.port, app.state.port)

    try:
        yield
    finally:
        await _server_manager.close()
        await db.close()
        _server_manager = None


def create_app(config: ServerConfig) -> FastAPI:
    mcp_asgi = _mcp_asgi(config)

    app = FastAPI(
        title="RemoteBash", version="0.2.0",
        lifespan=combine_lifespans(_app_lifespan, mcp_asgi.lifespan),
    )
    app.state.db_path = str(config.db_path)
    app.state.port = config.port
    app.state.transport = config.transport

    if _STATIC.is_dir():
        app.mount("/static", StaticFiles(directory=str(_STATIC)), name="static")

    app.include_router(dashboard_router)
    app.include_router(api_router)
    app.include_router(audit_router)
    app.mount("/mcp", mcp_asgi)
    return app


# ═══════════════════════════════════════════════════════════════════════
# CLI entry point
# ═══════════════════════════════════════════════════════════════════════

def main() -> None:
    parser = argparse.ArgumentParser(description="RemoteBash — MCP server with web dashboard.")
    parser.add_argument("--transport", choices=["http", "sse"], default="http")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=24587)
    parser.add_argument("--db", default=None,
                        help=f"SQLite path (default: {DEFAULT_DB})")
    parser.add_argument("--debug", action="store_true", default=False)
    args = parser.parse_args()

    config = ServerConfig(
        transport=args.transport, host=args.host, port=args.port,
        debug=args.debug,
        db_path=Path(args.db) if args.db else DEFAULT_DB,
    )

    import uvicorn
    import sys
    print(f"🚀 RemoteBash 启动中（传输：{config.transport}，端口：{config.port}）…", file=sys.stderr)
    uvicorn.run(create_app(config), host=config.host, port=config.port,
                log_level="debug" if config.debug else "info")


if __name__ == "__main__":
    main()
