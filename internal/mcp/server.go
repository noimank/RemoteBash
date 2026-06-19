package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"remotebash/internal/manager"
	"remotebash/internal/models"
)

// MCPBridge holds the MCP server and its tool registrations.
type MCPBridge struct {
	server       *server.MCPServer
	mgr          *manager.ConnectionManager
	dashboardURL string
}

// NewMCPBridge creates and configures the MCP server with all tools.
func NewMCPBridge(mgr *manager.ConnectionManager, dashboardURL string) *MCPBridge {
	b := &MCPBridge{
		mgr:          mgr,
		dashboardURL: dashboardURL,
	}
	b.server = server.NewMCPServer("RemoteBash", "2.0.0")
	b.registerTools()
	return b
}

// HTTPHandler returns an http.Handler for the given transport.
// transport: "http" (default) → streamable HTTP (2025 MCP spec).
// transport: "sse" → Server-Sent Events (legacy transport, two-endpoint).
func (b *MCPBridge) HTTPHandler(transport string) http.Handler {
	switch transport {
	case "sse":
		// SSE uses two endpoints:
		//   GET  /mcp           → SSE event stream (server→client)
		//   POST /mcp/messages  → JSON-RPC messages (client→server)
		// The handler does internal routing based on request path.
		sseSrv := server.NewSSEServer(b.server,
			server.WithSSEEndpoint("/mcp"),
			server.WithMessageEndpoint("/mcp/messages"),
			server.WithBaseURL(b.dashboardURL),
		)
		return sseSrv
	default:
		// Streamable HTTP uses a single /mcp endpoint for all methods.
		return server.NewStreamableHTTPServer(b.server)
	}
}

func (b *MCPBridge) registerTools() {
	// ── remote_shell ──────────────────────────────────────────
	toolRB := mcp.NewTool("remote_shell",
		mcp.WithDescription(
			"Execute a shell command on a remote host. "+
				"Runs against a long-lived, PTY-backed interactive shell per host, "+
				"so working directory, environment, shell functions, aliases, and history "+
				"persist across calls. Commands must be non-interactive — prompts that wait "+
				"for input (rm -i, password prompts, top, vim) block until timeout and then "+
				"reset the session; use non-interactive flags instead. "+
				"Use data_transfer for file uploads/downloads. "+
				"Returns {output, exit_code, cwd}."),
		mcp.WithString("client_name",
			mcp.Required(),
			mcp.Description("Remote host name from list_remote_clients."),
		),
		mcp.WithString("command",
			mcp.Required(),
			mcp.Description("Shell command to execute. Runs in a persistent interactive shell, "+
				"so pipes, redirects, builtins, aliases, functions, and the current working "+
				"directory behave as in a real terminal."),
		),
		mcp.WithNumber("timeout",
			mcp.Description("Timeout in seconds. Increase for long-running builds, installs, "+
				"or diagnostics."),
		),
	)

	b.server.AddTool(toolRB, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, ok := req.Params.Arguments.(map[string]any)
		if !ok {
			return mcpError("invalid arguments: expected object"), nil
		}
		clientName := getString(args, "client_name")
		if clientName == "" {
			return mcpError("缺少必填参数 client_name。请使用 list_remote_clients 获取可用的远程主机名称。"), nil
		}
		command := getString(args, "command")
		timeout := getInt(args, "timeout", 30)

		sess, err := b.mgr.Get(clientName)
		if err != nil {
			slog.Warn("MCP 客户端未找到", "client", clientName)
			return mcpError(fmt.Sprintf("客户端 '%s' 不存在。%v", clientName, err)), nil
		}

		result, err := sess.Exec(command, time.Duration(timeout)*time.Second)
		if err != nil {
			return mcpError(fmt.Sprintf("SSH command failed: %v", err)), nil
		}

		output := models.MCPRemoteBashOutput{
			Output:   result.Output,
			ExitCode: result.ExitCode,
			Cwd:      result.Cwd,
		}
		return mcpResult(output)
	})

	// ── list_remote_clients ───────────────────────────────────
	toolList := mcp.NewTool("list_remote_clients",
		mcp.WithDescription(
			"List all enabled remote hosts. "+
				"Returns [{client_name, host, port, user, cwd, safe_rm, shell_type}, ...]. "+
				"shell_type is the detected remote shell (e.g. ash, bash, dash, zsh). "+
				"Raises an error if no hosts are configured yet."),
	)

	b.server.AddTool(toolList, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		clients := b.mgr.ListEnabled()

		if len(clients) == 0 {
			where := ""
			if b.dashboardURL != "" {
				where = fmt.Sprintf(" at %s", b.dashboardURL)
			}
			return mcpError(fmt.Sprintf(
				"No remote hosts configured yet. Open the RemoteBash dashboard%s "+
					"to add your SSH hosts, then run list_remote_clients again.", where)), nil
		}

		info := make([]models.RemoteClientInfo, 0, len(clients))
		for _, c := range clients {
			info = append(info, models.RemoteClientInfo{
				ClientName: c.Name,
				Host:       c.Host,
				Port:       c.Port,
				User:       c.User,
				Cwd:        c.Cwd,
				SafeRm:     c.SafeRm,
				ShellType:  c.ShellType,
			})
		}
		return mcpResult(info)
	})

	// ── data_transfer ─────────────────────────────────────────
	toolDT := mcp.NewTool("data_transfer",
		mcp.WithDescription(
			"Transfer files between this server and a remote host via SFTP. "+
				"Use remote_shell for command execution. "+
				"Returns {success, direction, src, dst, size_bytes, duration_ms}."),
		mcp.WithString("client_name",
			mcp.Required(),
			mcp.Description("Remote host name from list_remote_clients."),
		),
		mcp.WithString("src",
			mcp.Required(),
			mcp.Description("Source file path. When direction is 'local2remote', this is a path on "+
				"the server machine; when 'remote2local', this is a path on the remote SSH host."),
		),
		mcp.WithString("dst",
			mcp.Required(),
			mcp.Description("Destination file path. When direction is 'local2remote', this is a path on "+
				"the remote SSH host; when 'remote2local', this is a path on the server machine."),
		),
		mcp.WithString("direction",
			mcp.Description("Transfer direction: 'local2remote' (upload local → remote) "+
				"or 'remote2local' (download remote → local). Default: 'local2remote'."),
		),
	)

	b.server.AddTool(toolDT, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, ok := req.Params.Arguments.(map[string]any)
		if !ok {
			return mcpError("invalid arguments: expected object"), nil
		}
		clientName := getString(args, "client_name")
		if clientName == "" {
			return mcpError("缺少必填参数 client_name。请使用 list_remote_clients 获取可用的远程主机名称。"), nil
		}
		src := getString(args, "src")
		dst := getString(args, "dst")
		direction := getString(args, "direction", "local2remote")

		if direction != "remote2local" && direction != "local2remote" {
			return mcpError(fmt.Sprintf("Invalid direction '%s'. Expected 'remote2local' or 'local2remote'.", direction)), nil
		}

		sess, err := b.mgr.Get(clientName)
		if err != nil {
			slog.Warn("MCP 客户端未找到", "client", clientName)
			return mcpError(fmt.Sprintf("客户端 '%s' 不存在。%v", clientName, err)), nil
		}

		directionLabel := "上传"
		if direction == "remote2local" {
			directionLabel = "下载"
		}
		cmdSummary := fmt.Sprintf("[SFTP %s] %s → %s", directionLabel, src, dst)

		t0 := time.Now()
		result, err := sess.Transfer(src, dst, direction)
		if err != nil {
			elapsed := int(time.Since(t0).Milliseconds())
			b.mgr.LogAudit(clientName, cmdSummary,
				fmt.Sprintf("error: %v", err), -1, direction, elapsed, false)
			return mcpError(fmt.Sprintf("SFTP transfer failed: %v", err)), nil
		}

		b.mgr.LogAudit(clientName, cmdSummary,
			fmt.Sprintf("success: true\nsrc: %s\ndst: %s\nsize_bytes: %d\nduration_ms: %d",
				result.Src, result.Dst, result.SizeBytes, result.DurationMs),
			0, direction, result.DurationMs, true)

		output := models.MCPDataTransferOutput{
			Success:    result.Success,
			Direction:  result.Direction,
			Src:        result.Src,
			Dst:        result.Dst,
			SizeBytes:  result.SizeBytes,
			DurationMs: result.DurationMs,
		}
		return mcpResult(output)
	})
}

// ── Helpers ───────────────────────────────────────────────────────────

func getString(args map[string]any, key string, defaults ...string) string {
	if args == nil {
		if len(defaults) > 0 {
			return defaults[0]
		}
		return ""
	}
	v, ok := args[key]
	if !ok {
		if len(defaults) > 0 {
			return defaults[0]
		}
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

func getInt(args map[string]any, key string, defaultVal int) int {
	if args == nil {
		return defaultVal
	}
	v, ok := args[key]
	if !ok {
		return defaultVal
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	default:
		return defaultVal
	}
}

func mcpResult(v any) (*mcp.CallToolResult, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal result: %w", err)
	}
	text := string(data)
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.TextContent{Type: "text", Text: text},
		},
	}, nil
}

func mcpError(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{
			mcp.TextContent{Type: "text", Text: msg},
		},
	}
}
