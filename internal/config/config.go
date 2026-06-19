package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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
	Transport string // "http" or "sse"
	Host      string
	Port      int
	Debug     bool
	DBPath    string
}

// ParseFlags parses CLI arguments and returns a ServerConfig.
func ParseFlags() *ServerConfig {
	cfg := &ServerConfig{}

	flag.StringVar(&cfg.Transport, "transport", "http", "MCP transport: http or sse")
	flag.StringVar(&cfg.Host, "host", "127.0.0.1", "Listen address")
	flag.IntVar(&cfg.Port, "port", 24587, "Listen port")
	flag.BoolVar(&cfg.Debug, "debug", false, "Enable debug logging")
	flag.StringVar(&cfg.DBPath, "db", DefaultDB(), "SQLite database path")
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
	return nil
}

// DashboardURL returns the browser-reachable dashboard URL, normalising
// wildcard bind addresses to localhost.
func (c *ServerConfig) DashboardURL() string {
	display := c.Host
	if display == "" || display == "0.0.0.0" || display == "::" {
		display = "localhost"
	}
	return "http://" + display + ":" + strconv.Itoa(c.Port)
}

// Addr returns the listen address string "host:port".
func (c *ServerConfig) Addr() string {
	return c.Host + ":" + strconv.Itoa(c.Port)
}
