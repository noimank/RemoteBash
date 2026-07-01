package server

import (
	"context"
	"database/sql"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"remotebash/internal/api"
	"remotebash/internal/config"
	"remotebash/internal/database"
	"remotebash/internal/manager"
	"remotebash/internal/mcp"
	"remotebash/web"
)

// Server is the assembled RemoteBash HTTP+MCP server.
type Server struct {
	cfg        *config.ServerConfig
	db         *sql.DB
	mgr        *manager.ConnectionManager
	mcp        *mcp.MCPBridge
	http       *http.Server
	tmpl       *template.Template
	staticFS   http.Handler
	mux        http.Handler
	logHandler *database.DBHandler
}

// New creates the assembled server (without starting it).
func New(cfg *config.ServerConfig) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config validation: %w", err)
	}

	db, err := database.Open(cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Replace the stderr-only slog handler with a dual handler that also
	// writes to the database, so all application logs appear in the web UI.
	level := slog.LevelInfo
	if cfg.Debug {
		level = slog.LevelDebug
	}
	logHandler := database.NewDBHandler(db, level)
	slog.SetDefault(slog.New(logHandler))

	mgr := manager.New(db)
	if err := mgr.Load(); err != nil {
		db.Close()
		return nil, fmt.Errorf("load clients: %w", err)
	}

	// Kick off async connection warm-up so shellType is populated for
	// list_remote_clients without waiting for a first command.
	mgr.WarmUp()

	tmpl, err := web.ParseTemplates()
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("parse templates: %w", err)
	}

	staticFS, err := web.StaticHTTPFS()
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("static fs: %w", err)
	}

	s := &Server{
		cfg:        cfg,
		db:         db,
		mgr:        mgr,
		tmpl:       tmpl,
		staticFS:   http.FileServer(staticFS),
		logHandler: logHandler,
	}
	s.mcp = mcp.NewMCPBridge(mgr, cfg.DashboardURL())
	s.mux = s.buildMux()

	return s, nil
}

// buildMux constructs the HTTP router with all routes.
// Go 1.22+ ServeMux handles method-based routing and path parameters.
func (s *Server) buildMux() http.Handler {
	mux := http.NewServeMux()

	// Health endpoint.
	mux.HandleFunc("GET /health", s.healthHandler)

	// API routes.
	apiRoutes := &api.Routes{Mgr: s.mgr}
	apiRoutes.Register(mux)

	// WebSocket terminal.
	terminalHandler := &api.TerminalHandler{Mgr: s.mgr}
	mux.HandleFunc("GET /api/clients/{name}/terminal", terminalHandler.ServeHTTP)

	// MCP endpoint.
	// Streamable HTTP: single /mcp for GET (SSE optional), POST, DELETE.
	// SSE (legacy): GET /mcp is the event stream, POST /mcp/messages is the
	//   JSON-RPC message endpoint. The handler does internal path routing.
	mcpHandler := s.mcp.HTTPHandler(s.cfg.Transport)
	mux.Handle("GET /mcp", mcpHandler)
	mux.Handle("POST /mcp", mcpHandler)
	mux.Handle("DELETE /mcp", mcpHandler)
	mux.Handle("POST /mcp/messages", mcpHandler)

	// Dashboard pages.
	mux.HandleFunc("GET /{$}", s.dashboardPage)
	mux.HandleFunc("GET /audit", s.auditPage)
	mux.HandleFunc("GET /guide", s.guidePage)
	mux.HandleFunc("GET /logs", s.logPage)

	// Static files.
	mux.Handle("GET /static/", http.StripPrefix("/static/", s.staticFS))

	// Wrap: trailing-slash redirect → recovery.
	// 反向代理子路径部署（BaseURLPrefix 非空）时，外层再包 rootRedirect（裸前缀→根）
	// 与 StripPrefix（剥离前缀），使内部路由与 handler（含 mcp-go）始终看到根路径。
	h := trailingSlashRedirect(recoveryMiddleware(mux), s.cfg.BaseURLPrefix)
	if s.cfg.BaseURLPrefix != "" {
		h = rootRedirect(http.StripPrefix(s.cfg.BaseURLPrefix, h), s.cfg.BaseURLPrefix)
	}
	return h
}

// healthHandler returns a simple health check response.
func (s *Server) healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}

// dashboardPage renders the dashboard HTML page.
func (s *Server) dashboardPage(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"BaseURLPrefix":     s.cfg.BaseURLPrefix,
		"DashboardURL": s.cfg.RequestDashboardURL(r),
		"ActivePage":   "dashboard",
	}
	if err := s.tmpl.ExecuteTemplate(w, "base.gohtml", data); err != nil {
		slog.Error("渲染面板失败", "err", err)
	}
}

// auditPage renders the audit log HTML page.
func (s *Server) auditPage(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"BaseURLPrefix":     s.cfg.BaseURLPrefix,
		"DashboardURL": s.cfg.RequestDashboardURL(r),
		"ActivePage":   "audit",
	}
	if err := s.tmpl.ExecuteTemplate(w, "base.gohtml", data); err != nil {
		slog.Error("渲染审计页失败", "err", err)
	}
}

// guidePage renders the usage guide HTML page. The template uses the live
// dashboard URL and configured transport so MCP endpoint snippets and config
// examples are always correct without manual editing.
func (s *Server) guidePage(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"BaseURLPrefix":     s.cfg.BaseURLPrefix,
		"DashboardURL": s.cfg.RequestDashboardURL(r),
		"Transport":    s.cfg.Transport,
		"ActivePage":   "guide",
	}
	if err := s.tmpl.ExecuteTemplate(w, "base.gohtml", data); err != nil {
		slog.Error("渲染使用指南失败", "err", err)
	}
}

// logPage renders the server log viewer HTML page.
func (s *Server) logPage(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"BaseURLPrefix":     s.cfg.BaseURLPrefix,
		"DashboardURL": s.cfg.RequestDashboardURL(r),
		"ActivePage":   "logs",
	}
	if err := s.tmpl.ExecuteTemplate(w, "base.gohtml", data); err != nil {
		slog.Error("渲染日志页失败", "err", err)
	}
}

// Run starts the HTTP server and blocks until a signal is received.
func (s *Server) Run() error {
	s.http = &http.Server{
		Addr:         s.cfg.Addr(),
		Handler:      s.mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0, // no write timeout — WebSocket connections are long-lived
		IdleTimeout:  120 * time.Second,
	}

	slog.Info("RemoteBash 启动中",
		"db", s.cfg.DBPath,
		"dashboard", s.cfg.DashboardURL(),
		"mcp", s.cfg.DashboardURL()+"/mcp",
		"transport", s.cfg.Transport,
	)

	errCh := make(chan error, 1)
	go func() {
		slog.Info("监听中", "addr", s.cfg.Addr())
		if err := s.http.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	// Ready: surface actionable URLs so the operator knows where to point the
	// browser and the MCP client without digging through the config.
	dashboard := s.cfg.DashboardURL()
	slog.Info("控制面板已就绪: 浏览器打开 " + dashboard + " 管理客户端连接")
	slog.Info("MCP 接入地址: " + dashboard + "/mcp (transport=" + s.cfg.Transport + ")")

	// Wait for signal or error.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	select {
	case sig := <-quit:
		slog.Info("正在关闭", "signal", sig.String())
	case err := <-errCh:
		return fmt.Errorf("server error: %w", err)
	}

	// Graceful shutdown with 10s deadline.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := s.http.Shutdown(shutdownCtx); err != nil {
		slog.Warn("强制关闭", "err", err)
	}

	s.mgr.Close()
	slog.Info("服务已停止")
	s.logHandler.Shutdown()
	s.db.Close()
	return nil
}

// recoveryMiddleware catches panics in HTTP handlers and returns 500.
func recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic 已恢复",
					"method", r.Method,
					"path", r.URL.Path,
					"panic", rec,
					"stack", string(debug.Stack()),
				)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// trailingSlashRedirect redirects /path/ → /path (301 Moved Permanently).
// Only applies to non-root paths that are not prefix-pattern routes like /static/.
// baseURLPrefix is prepended to the redirect Location so it stays correct under a
// reverse-proxy sub-path; when empty the behaviour is unchanged.
func trailingSlashRedirect(next http.Handler, baseURLPrefix string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Only redirect paths ending with "/" that are not the root.
		if path == "/" || !strings.HasSuffix(path, "/") {
			next.ServeHTTP(w, r)
			return
		}

		// Skip prefix-pattern routes that need the trailing slash.
		if strings.HasPrefix(path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}

		// Build redirect target: strip trailing slash, restore the base-url
		// prefix, and preserve the query string.
		target := baseURLPrefix + strings.TrimSuffix(path, "/")
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}

		http.Redirect(w, r, target, http.StatusMovedPermanently)
	})
}

// rootRedirect redirects the bare prefix (e.g. /remotebash) to prefix+"/" so
// that after StripPrefix the request lands on "/" and renders the dashboard.
// All other requests pass through to the inner handler (StripPrefix).
func rootRedirect(next http.Handler, baseURLPrefix string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == baseURLPrefix {
			target := baseURLPrefix + "/"
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusMovedPermanently)
			return
		}
		next.ServeHTTP(w, r)
	})
}
