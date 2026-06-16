"""MCP tools — remote_shell(), data_transfer(), and list_remote_clients()."""

import logging
from typing import Annotated, TypedDict

from fastmcp.dependencies import CurrentContext
from fastmcp.exceptions import ToolError
from fastmcp.server.context import Context
from mcp.types import ToolAnnotations
from pydantic import Field

logger = logging.getLogger(__name__)


class RemoteShellOutput(TypedDict):
    """Remote command result."""

    output: str
    exit_code: int
    cwd: str


class RemoteClientInfo(TypedDict):
    """Enabled SSH host."""

    client_name: str
    host: str
    port: int
    user: str
    cwd: str
    safe_rm: bool


class DataTransferOutput(TypedDict):
    """Result of an SFTP file transfer."""

    success: bool
    direction: str
    src: str
    dst: str
    size_bytes: int
    duration_ms: float


def register_tools(mcp):

    @mcp.tool(
        title="Execute Command on Remote Host",
        annotations=ToolAnnotations(
            destructiveHint=True,
            idempotentHint=False,
            openWorldHint=True,
        ),
    )
    async def remote_shell(
        client_name: Annotated[
            str, Field(description="Remote host name from list_remote_clients.")
        ],
        command: Annotated[
            str, Field(
                description=(
                    "Bash command to execute on the remote host. Pipes, redirects, "
                    "builtins, aliases, and functions work."
                )
            )
        ],
        timeout: Annotated[
            int, Field(
                description=(
                    "Timeout in seconds. Increase for long-running builds, installs, "
                    "or diagnostics."
                )
            )
        ] = 30,
        ctx: Context = CurrentContext(),
    ) -> RemoteShellOutput:
        """Run a bash command on a remote host.

        Use ``data_transfer`` for file uploads/downloads. Commands should be
        non-interactive.

        Returns ``{output, exit_code, cwd}``.
        """
        mgr = ctx.lifespan_context["manager"]
        try:
            session = mgr.get(client_name)
        except KeyError as e:
            logger.warning("Client '%s' not found", client_name)
            raise ToolError(str(e), log_level=logging.INFO) from None
        result = await session.exec(command, timeout=timeout)
        return {k: result[k] for k in ("output", "exit_code", "cwd")}

    @mcp.tool(
        title="List Remote Hosts",
        annotations=ToolAnnotations(
            readOnlyHint=True,
            openWorldHint=False,
        ),
    )
    def list_remote_clients(ctx: Context = CurrentContext()) -> list[RemoteClientInfo]:
        """List all enabled remote hosts.

        Returns ``[{client_name, host, port, user, cwd, safe_rm}, ...]``.
        An empty list means no hosts are configured yet.
        """
        mgr = ctx.lifespan_context["manager"]
        clients = [
            {
                "client_name": c["name"],
                "host": c["host"],
                "port": c["port"],
                "user": c["user"],
                "cwd": c["cwd"],
                "safe_rm": c["safe_rm"],
            }
            for c in mgr.list_enabled()
        ]
        if not clients:
            return [{"_message": (
                "No remote hosts configured yet.  Open the RemoteBash dashboard at "
                "http://localhost:24587 to add your SSH hosts, then run "
                "list_remote_clients again."
            )}]
        return clients

    @mcp.tool(
        title="Transfer Files via SFTP",
        annotations=ToolAnnotations(
            destructiveHint=True,
            idempotentHint=False,
            openWorldHint=True,
        ),
    )
    async def data_transfer(
        client_name: Annotated[
            str, Field(description="Remote host name from list_remote_clients.")
        ],
        src: Annotated[
            str, Field(
                description="Source file path. When direction is 'local2remote', this is a "
                            "path on the RemoteBash server machine; when 'remote2local', "
                            "this is a path on the remote SSH host."
            )
        ],
        dst: Annotated[
            str, Field(
                description="Destination file path. When direction is 'local2remote', this is a "
                            "path on the remote SSH host; when 'remote2local', this is a "
                            "path on the RemoteBash server machine."
            )
        ],
        direction: Annotated[
            str, Field(
                description="Transfer direction: 'local2remote' (upload local → remote) "
                            "or 'remote2local' (download remote → local). Default: 'local2remote'."
            )
        ] = "local2remote",
        ctx: Context = CurrentContext(),
    ) -> DataTransferOutput:
        """Transfer files between this server and a remote host via SFTP.

        Use ``remote_shell`` for command execution.

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
