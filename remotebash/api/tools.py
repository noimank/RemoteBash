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
    """Result of a remote shell command execution."""

    stdout: str
    stderr: str
    exit_code: int
    cwd: str


class RemoteClientInfo(TypedDict):
    """SSH host configuration."""

    name: str
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
            str, Field(description="Name of the remote host. Call list_remote_clients first — do not guess.")
        ],
        command: Annotated[
            str, Field(description="Shell command to execute on the remote host. Pipes, redirects, and shell builtins all work.")
        ],
        timeout: Annotated[
            int, Field(description="Command timeout in seconds (default: 30). Increase for builds, installs, or large transfers.")
        ] = 30,
        ctx: Context = CurrentContext(),
    ) -> RemoteShellOutput:
        """Execute a command on a remote host via SSH.

        The working directory (CWD) is stateful across calls: ``cd /somewhere``
        persists, so the next command runs in that directory.  Use ``pwd`` to
        check the current directory at any time.

        For file uploads and downloads, prefer ``data_transfer`` (SFTP) over
        shell commands like ``cp``, ``scp``, or ``cat``.

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

    @mcp.tool(
        title="List Remote Hosts",
        annotations=ToolAnnotations(
            readOnlyHint=True,
            openWorldHint=False,
        ),
    )
    def list_remote_clients(ctx: Context = CurrentContext()) -> list[RemoteClientInfo]:
        """List all enabled remote hosts configured in the web dashboard.

        Use the returned ``name`` value as ``client_name`` for
        ``remote_shell`` and ``data_transfer``.

        Returns ``[{name, host, port, user, cwd, safe_rm}, ...]``.
        An empty list means no hosts are configured yet.
        """
        mgr = ctx.lifespan_context["manager"]
        clients = [{k: c[k] for k in ("name", "host", "port", "user", "cwd", "safe_rm")}
                    for c in mgr.list_enabled()]
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
            str, Field(description="Name of the remote host. Call list_remote_clients first — do not guess.")
        ],
        src: Annotated[
            str, Field(
                description="Source file path. When direction is 'local2remote', this is a "
                            "**local** path; when 'remote2local', this is a **remote** path."
            )
        ],
        dst: Annotated[
            str, Field(
                description="Destination file path. When direction is 'local2remote', this is a "
                            "**remote** path; when 'remote2local', this is a **local** path."
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
        """Transfer files between the local machine and a remote host via SFTP.

        For general command execution (shell commands, scripts, etc.), use
        ``remote_shell`` instead.

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
