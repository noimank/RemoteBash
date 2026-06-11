"""MCP tools — RemoteShell() and ListRemoteClients()."""

from fastmcp.dependencies import CurrentContext
from fastmcp.server.context import Context


def register_tools(mcp):

    @mcp.tool
    async def RemoteShell(client_name: str, command: str, timeout: int = 30,
                          ctx: Context = CurrentContext()) -> dict:
        """Execute a shell command on a remote host.

        The working directory persists between calls.  Use ``cd /path`` and
        subsequent calls run from that directory.

        Idle-timed-out sessions are automatically reconnected — this is
        transparent to the caller.

        Args:
            client_name: The client name (from ``ListRemoteClients``).
            command:     Shell command to execute.
            timeout:     Max execution time in seconds (default 30).

        Returns:
            ``{stdout, stderr, exit_code, cwd}``.
        """
        mgr = ctx.lifespan_context["manager"]
        result = await mgr.get(client_name).exec(command, timeout=timeout)
        return {k: result[k] for k in ("stdout", "stderr", "exit_code", "cwd")}

    @mcp.tool
    def ListRemoteClients(ctx: Context = CurrentContext()) -> list[dict]:
        """List all enabled remote hosts.

        Only enabled clients are listed.  Connection state is handled
        transparently — any enabled client is ready to use (lazy connect).

        Returns a list with keys: ``name``, ``host``, ``port``, ``user``,
        ``cwd``, ``label``.
        """
        mgr = ctx.lifespan_context["manager"]
        return [{k: c[k] for k in ("name", "host", "port", "user", "cwd", "label")}
                for c in mgr.list_enabled()]
