"""MCP tools — RemoteShell(), DataTransfer(), and ListRemoteClients()."""

import logging

from fastmcp.dependencies import CurrentContext
from fastmcp.exceptions import ToolError
from fastmcp.server.context import Context

logger = logging.getLogger(__name__)


def register_tools(mcp):

    @mcp.tool
    async def RemoteShell(client_name: str, command: str, timeout: int = 30,
                          ctx: Context = CurrentContext()) -> dict:
        """Execute a command on a remote host via SSH.

        Call ``ListRemoteClients`` first to get valid **client_name** values.
        Do not guess names — use exactly what the list returns.

        CWD is stateful across calls: ``cd /path`` persists.  To check the
        current directory, run ``pwd``.

        Increase **timeout** for long-running commands (build, install, large
        transfer); the default kills at 30 s.

        For file transfer prefer ``DataTransfer`` over shell commands
        like ``cp`` or ``scp``.

        Returns ``{stdout, stderr, exit_code, cwd}``.
        """
        mgr = ctx.lifespan_context["manager"]
        try:
            session = mgr.get(client_name)
        except KeyError as e:
            logger.warning("Client '%s' not found", client_name)
            raise ToolError(str(e), log_level=logging.INFO) from None
        result = await session.exec(command, timeout=timeout)
        return {k: result[k] for k in ("stdout", "stderr", "exit_code", "cwd")}

    @mcp.tool
    def ListRemoteClients(ctx: Context = CurrentContext()) -> list[dict]:
        """List configured remote hosts.

        Use the returned ``name`` as ``client_name`` for ``RemoteShell``.
        Fields: ``name``, ``host``, ``port``, ``user``, ``cwd``, ``safe_rm``.

        Empty list means nothing is configured — one item with
        ``_message`` guides the user to the web dashboard.
        """
        mgr = ctx.lifespan_context["manager"]
        clients = [{k: c[k] for k in ("name", "host", "port", "user", "cwd", "safe_rm")}
                    for c in mgr.list_enabled()]
        if not clients:
            return [{"_message": (
                "No remote hosts configured yet.  Open the RemoteBash dashboard at "
                "http://localhost:24587 to add your SSH hosts, then run "
                "ListRemoteClients again."
            )}]
        return clients

    @mcp.tool
    async def DataTransfer(client_name: str, src: str, dst: str,
                           direction: str = "remote2local",
                           ctx: Context = CurrentContext()) -> dict:
        """Transfer files between local and remote host via SFTP.

        Call ``ListRemoteClients`` first to get valid **client_name** values.

        **direction** must be one of:
        - ``remote2local`` — download **src** from remote to **dst** on local
        - ``local2remote`` — upload **src** from local to **dst** on remote

        Returns ``{success, direction, src, dst, size_bytes, duration_ms}``.
        """
        if direction not in ("remote2local", "local2remote"):
            raise ToolError(
                f"Invalid direction '{direction}'. "
                "Expected 'remote2local' or 'local2remote'."
            )
        mgr = ctx.lifespan_context["manager"]
        try:
            session = mgr.get(client_name)
        except KeyError as e:
            logger.warning("Client '%s' not found", client_name)
            raise ToolError(str(e), log_level=logging.INFO) from None
        result = await session.transfer(src, dst, direction)
        return {k: result[k] for k in ("success", "direction", "src", "dst",
                                        "size_bytes", "duration_ms")}
