"""App factory — FastAPI + FastMCP ASGI application.

Usage:
    uv run python main.py --transport http --port 8000
    uv run python main.py --transport sse  --port 9090
"""

import argparse
from contextlib import asynccontextmanager
from pathlib import Path

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
    parser.add_argument("--host", default="0.0.0.0")
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
    uvicorn.run(create_app(config), host=config.host, port=config.port,
                log_level="debug" if config.debug else "info")


if __name__ == "__main__":
    main()
