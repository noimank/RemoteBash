package config

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultDB returns the platform-appropriate default database path.
func DefaultDB() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".remotebash", "remotebash.db")
}

// ServerConfig holds all CLI-configurable server settings.
type ServerConfig struct {
	Transport     string // "http" or "sse"
	Host          string
	Port          int
	Debug         bool
	DBPath        string
	BaseURLPrefix string // URL 子路径前缀，规范化后为 ""（根）或 "/seg[/seg...]"（无尾斜杠）
}

// ParseFlags parses CLI arguments and returns a ServerConfig.
func ParseFlags() *ServerConfig {
	cfg := &ServerConfig{}

	flag.StringVar(&cfg.Transport, "transport", "http", "MCP transport: http or sse")
	flag.StringVar(&cfg.Host, "host", "127.0.0.1", "Listen address")
	flag.IntVar(&cfg.Port, "port", 24587, "Listen port")
	flag.BoolVar(&cfg.Debug, "debug", false, "Enable debug logging")
	flag.StringVar(&cfg.DBPath, "db", DefaultDB(), "SQLite database path")
	// 默认值取 BASE_URL_PREFIX 环境变量（便于容器 -e 注入），命令行 --base-url-prefix 优先。
	flag.StringVar(&cfg.BaseURLPrefix, "base-url-prefix", os.Getenv("BASE_URL_PREFIX"),
		"URL 子路径前缀（反向代理部署用，如 /remotebash；默认空 = 部署在根）")
	flag.Parse()

	return cfg
}

// Validate checks the server configuration for errors.
func (c *ServerConfig) Validate() error {
	if c.Transport != "http" && c.Transport != "sse" {
		return fmt.Errorf("invalid transport %q: must be http or sse", c.Transport)
	}
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("invalid port %d: must be between 1 and 65535", c.Port)
	}

	c.BaseURLPrefix = normalizeBaseURLPrefix(c.BaseURLPrefix)
	for _, r := range c.BaseURLPrefix {
		switch {
		case r == '/' || r == '-' || r == '.' || r == '_':
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		default:
			return fmt.Errorf("invalid base-url-prefix %q: only letters, digits, '/', '-', '.', '_' are allowed", c.BaseURLPrefix)
		}
	}
	return nil
}

// normalizeBaseURLPrefix 规范化为 ""（根）或 "/seg[/seg...]"：保证前导斜杠、去除尾斜杠。
func normalizeBaseURLPrefix(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == "/" {
		return ""
	}
	if !strings.HasPrefix(s, "/") {
		s = "/" + s
	}
	s = strings.TrimSuffix(s, "/")
	if s == "" {
		return ""
	}
	return s
}

// DashboardURL returns the browser-reachable dashboard URL, normalising
// wildcard bind addresses to localhost and appending the configured prefix.
// Prefer RequestDashboardURL for page templates — it derives the URL from the
// incoming request and is more accurate when accessed from a different machine
// or through a reverse proxy.
func (c *ServerConfig) DashboardURL() string {
	display := c.Host
	if display == "" || display == "0.0.0.0" || display == "::" {
		display = "localhost"
	}
	return "http://" + display + ":" + strconv.Itoa(c.Port) + c.BaseURLPrefix
}

// RequestDashboardURL derives the dashboard URL from the incoming HTTP request.
// Uses r.Host (what the browser actually addressed) and detects TLS from the
// connection state or proxy headers. Falls back to the static DashboardURL()
// when r.Host is empty (should never happen in a real request, but defensive).
func (c *ServerConfig) RequestDashboardURL(r *http.Request) string {
	if r.Host == "" {
		return c.DashboardURL()
	}
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host + c.BaseURLPrefix
}

// isHTTPS detects whether the original browser request used HTTPS. Checks:
// 1. r.TLS — direct TLS (if Go handles it)
// 2. X-Forwarded-Proto — de facto standard (nginx, Traefik, Caddy, HAProxy, ALB)
// 3. X-Forwarded-Scheme — less common variant
// 4. X-Forwarded-Ssl / Front-End-Https — Azure, IIS, legacy proxies
// 5. Referer — browser-sent full URL (e.g. "https://..."), unfiltered by proxies
func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if r.Header.Get("X-Forwarded-Proto") == "https" {
		return true
	}
	if r.Header.Get("X-Forwarded-Scheme") == "https" {
		return true
	}
	if r.Header.Get("X-Forwarded-Ssl") == "on" {
		return true
	}
	if r.Header.Get("Front-End-Https") == "on" {
		return true
	}
	// Last resort: the browser's Referer header contains the full scheme.
	// When navigating from an HTTPS page, Referer starts with "https://".
	if strings.HasPrefix(r.Referer(), "https://") {
		return true
	}
	return false
}

// Addr returns the listen address string "host:port".
func (c *ServerConfig) Addr() string {
	return c.Host + ":" + strconv.Itoa(c.Port)
}
