// remotebash — MCP server for remote SSH command execution with web dashboard.
//
// A single static Go binary that embeds all web assets and templates.
// No Python runtime, virtual environment, or external dependencies needed.

package main

import (
	"fmt"
	"log/slog"
	"os"

	"remotebash/internal/config"
	"remotebash/internal/server"
)

func main() {
	// Set up structured logging.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	cfg := config.ParseFlags()

	if cfg.Debug {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		})))
	}

	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "配置错误: %v\n", err)
		os.Exit(1)
	}

	srv, err := server.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "初始化失败: %v\n", err)
		os.Exit(1)
	}

	if err := srv.Run(); err != nil {
		slog.Error("运行错误", "err", err)
		fmt.Fprintf(os.Stderr, "运行错误: %v\n", err)
		os.Exit(1)
	}
}
