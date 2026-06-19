package ssh

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	gossh "golang.org/x/crypto/ssh"

	"remotebash/internal/models"
)

// RemoteSession manages an SSH connection to a remote host and provides
// command execution (MCP path) and terminal shell access (browser path).
type RemoteSession struct {
	Name     string
	Host     string
	Port     int
	User     string
	Password string
	Enabled  bool
	SafeRm   bool
	Via      string // jump host client name, empty = direct

	conn            *gossh.Client
	shell           *PersistentShell // MCP exec shell
	shellLock       sync.Mutex       // guards EnsureShell
	execLock        sync.Mutex       // serialises concurrent Exec callers
	connectLock     sync.Mutex       // guards Connect against TOCTOU races
	disconnectLock  sync.Mutex       // serialises Disconnect calls
	cwd             string
	auditCb         AuditCallback
	tunnelResolver  TunnelResolver // callable: name → *gossh.Client
	relay           *SocatTunnelRelay
}

// AuditCallback is called after every command execution.
type AuditCallback func(clientName, command string, result *CommandOutput)

// TunnelResolver resolves a jump host name to an SSH client connection.
type TunnelResolver func(name string) (*gossh.Client, error)

// NewRemoteSession creates a new RemoteSession.
func NewRemoteSession(name, host, user, password string, port int, enabled, safeRm bool, via string) *RemoteSession {
	return &RemoteSession{
		Name:     name,
		Host:     host,
		Port:     port,
		User:     user,
		Password: password,
		Enabled:  enabled,
		SafeRm:   safeRm,
		Via:      via,
		cwd:      "~",
	}
}

// Connected reports whether the SSH connection is alive.
func (s *RemoteSession) Connected() bool {
	return s.conn != nil
}

// RawConn returns the underlying *gossh.Client, used for tunnel resolution.
func (s *RemoteSession) RawConn() *gossh.Client {
	return s.conn
}

// SetAuditCallback registers the audit logging callback.
func (s *RemoteSession) SetAuditCallback(cb AuditCallback) {
	s.auditCb = cb
}

// SetTunnelResolver registers the jump-host connection resolver.
func (s *RemoteSession) SetTunnelResolver(r TunnelResolver) {
	s.tunnelResolver = r
}

// Connect establishes the SSH connection (direct or via jump host).
func (s *RemoteSession) Connect() error {
	if !s.Enabled {
		return fmt.Errorf("client '%s' is disabled", s.Name)
	}

	s.connectLock.Lock()
	defer s.connectLock.Unlock()

	if s.Connected() {
		return nil
	}

	config := &gossh.ClientConfig{
		User: s.User,
		Auth: []gossh.AuthMethod{gossh.Password(s.Password)},
		HostKeyCallback: hostKeyLogger(s.Name),
		Timeout:         10 * time.Second,
	}

	if s.Via != "" {
		if s.tunnelResolver == nil {
			return fmt.Errorf("client '%s' requires jump host '%s' but no tunnel resolver is configured", s.Name, s.Via)
		}

		viaConn, err := s.tunnelResolver(s.Via)
		if err != nil {
			return fmt.Errorf("resolve jump host '%s': %w", s.Via, err)
		}

		slog.Info("通过跳板机连接", "via", s.Via, "target", fmt.Sprintf("%s:%d", s.Host, s.Port))

		// Try direct tunnel first.
		addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
		conn, err := viaConn.Dial("tcp", addr)
		if err != nil {
			// If the jump host prohibits TCP forwarding, fall back to relay.
			if strings.Contains(err.Error(), "administratively prohibited") ||
				strings.Contains(err.Error(), "not permitted") {
				slog.Warn("跳板机禁止 TCP 转发，降级为中继模式", "via", s.Via)
				relay := NewSocatTunnelRelay(viaConn, s.Host, s.Port)
				sock, relayErr := relay.Connect()
				if relayErr != nil {
					return fmt.Errorf("jump host '%s' prohibits TCP forwarding and no relay tool is available: %w", s.Via, relayErr)
				}
				s.relay = relay

				// Use the relayed socket instead.
				ncc, chans, reqs, relaySSHErr := gossh.NewClientConn(sock, addr, config)
				if relaySSHErr != nil {
					relay.Close()
					s.relay = nil
					return fmt.Errorf("ssh over relay: %w", relaySSHErr)
				}
				s.conn = gossh.NewClient(ncc, chans, reqs)
				s.cwd = "~"
				slog.Info("跳板机中继连接成功", "via", s.Via, "target", fmt.Sprintf("%s:%d", s.Host, s.Port))
				return nil
			}
			return fmt.Errorf("tunnel dial: %w", err)
		}

		ncc, chans, reqs, tunnelSSHErr := gossh.NewClientConn(conn, addr, config)
		if tunnelSSHErr != nil {
			return fmt.Errorf("ssh over tunnel: %w", tunnelSSHErr)
		}
		s.conn = gossh.NewClient(ncc, chans, reqs)
		s.cwd = "~"
		return nil
	}

	// Direct connection.
	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	conn, err := gossh.Dial("tcp", addr, config)
	if err != nil {
		return fmt.Errorf("connect %s: %w", addr, err)
	}
	s.conn = conn
	s.cwd = "~"
	return nil
}

// Disconnect closes the SSH connection and all associated shells.
// Safe for concurrent calls.
func (s *RemoteSession) Disconnect() {
	s.disconnectLock.Lock()
	defer s.disconnectLock.Unlock()

	// Close shell under shellLock to avoid racing EnsureShell.
	s.shellLock.Lock()
	if s.shell != nil {
		s.shell.Close()
		s.shell = nil
	}
	s.shellLock.Unlock()

	if s.conn != nil {
		s.conn.Close()
		s.conn = nil
	}
	if s.relay != nil {
		s.relay.Close()
		s.relay = nil
	}
}

// EnsureShell returns a live PersistentShell for MCP command execution,
// (re)starting it if needed.
func (s *RemoteSession) EnsureShell() (*PersistentShell, error) {
	s.shellLock.Lock()
	defer s.shellLock.Unlock()

	// Rebuild if shell died or safe_rm was toggled.
	if s.shell != nil && s.shell.Alive() && s.shell.SafeRmFlag() == s.SafeRm {
		return s.shell, nil
	}

	// Tear down stale shell.
	if s.shell != nil {
		s.shell.Close()
		s.shell = nil
	}

	var initScript string
	if s.SafeRm {
		initScript = SafeRmShim
	}

	shell := NewPersistentShell(s.conn, defaultCols, defaultRows, s.SafeRm, initScript, "")
	if err := shell.Start(); err != nil {
		return nil, fmt.Errorf("start mcp shell: %w", err)
	}

	s.shell = shell
	s.cwd = "~"
	return s.shell, nil
}

// Exec runs a command on the persistent interactive shell.
// Uses lazy connect: establishes the SSH connection on first call.
// Concurrent callers are serialised via execLock.
func (s *RemoteSession) Exec(command string, timeout time.Duration) (*CommandOutput, error) {
	if !s.Enabled {
		return nil, fmt.Errorf("client '%s' is disabled", s.Name)
	}

	s.execLock.Lock()
	defer s.execLock.Unlock()

	if err := s.Connect(); err != nil {
		return nil, fmt.Errorf("ssh connect failed: %w", err)
	}

	// EnsureShell stays INSIDE the lock: a queued caller must re-check
	// the shell after waiting.
	shell, err := s.EnsureShell()
	if err != nil {
		s.Disconnect()
		return nil, fmt.Errorf("ssh shell setup failed: %w", err)
	}

	t0 := time.Now()
	result, err := shell.Run(command, timeout)

	if err != nil {
		elapsed := int(time.Since(t0).Milliseconds())
		if s.auditCb != nil {
			s.auditCb(s.Name, command, &CommandOutput{
				Output:     fmt.Sprintf("SSH command failed: %v", err),
				ExitCode:   -1,
				Cwd:        s.cwd,
				DurationMs: elapsed,
			})
		}
		s.Disconnect()
		return nil, fmt.Errorf("ssh command failed: %w", err)
	}

	s.cwd = result.Cwd
	if s.auditCb != nil {
		s.auditCb(s.Name, command, result)
	}
	return result, nil
}

// Transfer performs an SFTP file transfer.
func (s *RemoteSession) Transfer(src, dst, direction string) (*TransferOutput, error) {
	if !s.Enabled {
		return nil, fmt.Errorf("client '%s' is disabled", s.Name)
	}

	if err := s.Connect(); err != nil {
		return nil, err
	}

	// Expand ~ in remote paths (SFTP doesn't expand ~).
	if direction == "remote2local" {
		src = s.expandRemotePath(src)
	} else {
		dst = s.expandRemotePath(dst)
	}

	t0 := time.Now()

	// Open SFTP session over the SSH client.
	sftpClient, err := sftp.NewClient(s.conn)
	if err != nil {
		return nil, fmt.Errorf("sftp session: %w", err)
	}
	defer sftpClient.Close()

	var size int64
	switch direction {
	case "remote2local":
		// Download: remote src → local dst.
		if err := sftpDownload(sftpClient, src, dst); err != nil {
			s.Disconnect()
			return nil, fmt.Errorf("sftp download: %w", err)
		}
		// Best-effort stat the remote file for size reporting.
		st, statErr := sftpClient.Stat(src)
		if statErr == nil {
			size = st.Size()
		}
	case "local2remote":
		// Upload: local src → remote dst.
		if err := sftpUpload(sftpClient, src, dst); err != nil {
			s.Disconnect()
			return nil, fmt.Errorf("sftp upload: %w", err)
		}
		st, statErr := sftpClient.Stat(dst)
		if statErr == nil {
			size = st.Size()
		}
	default:
		return nil, fmt.Errorf("invalid direction '%s', expected 'remote2local' or 'local2remote'", direction)
	}

	elapsed := int(time.Since(t0).Milliseconds())
	slog.Info("传输完成", "src", src, "dst", dst, "direction", direction, "duration_ms", elapsed, "bytes", size)
	return &TransferOutput{
		Success:    true,
		Direction:  direction,
		Src:        src,
		Dst:        dst,
		SizeBytes:  size,
		DurationMs: elapsed,
	}, nil
}

// TransferOutput is the result of an SFTP transfer.
type TransferOutput struct {
	Success    bool
	Direction  string
	Src        string
	Dst        string
	SizeBytes  int64
	DurationMs int
}

// ExecLock acquires the exec lock (used by Manager during shutdown).
func (s *RemoteSession) ExecLock()  { s.execLock.Lock() }

// ExecUnlock releases the exec lock.
func (s *RemoteSession) ExecUnlock() { s.execLock.Unlock() }

// expandRemotePath expands ~ to the remote home directory.
func (s *RemoteSession) expandRemotePath(path string) string {
	if !strings.Contains(path, "~") {
		return path
	}

	result, err := s.Exec("echo $HOME", 5*time.Second)
	if err != nil {
		return path
	}
	home := strings.TrimSpace(result.Output)
	if home == "" {
		return path
	}
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return home + path[1:]
	}
	return path
}

// TestConnection verifies connectivity using the same path as Connect().
func (s *RemoteSession) TestConnection() error {
	return s.Connect()
}

// OpenTerminalShell creates a separate PersistentShell for the browser terminal.
// This is independent of the MCP exec shell.
func (s *RemoteSession) OpenTerminalShell() (*PersistentShell, error) {
	if !s.Enabled {
		return nil, fmt.Errorf("client '%s' is disabled", s.Name)
	}
	if err := s.Connect(); err != nil {
		return nil, err
	}

	var initScript string
	if s.SafeRm {
		initScript = SafeRmShim
	}

	shell := NewPersistentShell(s.conn, defaultCols, defaultRows, s.SafeRm, initScript, TerminalPS1)
	if err := shell.Start(); err != nil {
		return nil, fmt.Errorf("start terminal shell: %w", err)
	}
	return shell, nil
}

// ToInfo returns a serializable ClientInfo representation of the session.
func (s *RemoteSession) ToInfo() models.ClientInfo {
	return models.ClientInfo{
		Name:      s.Name,
		Host:      s.Host,
		Port:      s.Port,
		User:      s.User,
		Connected: s.Connected(),
		Cwd:       s.cwd,
		Enabled:   s.Enabled,
		SafeRm:    s.SafeRm,
		Via:       s.Via,
	}
}

// ── SFTP helpers ─────────────────────────────────────────────────────

func sftpDownload(client *sftp.Client, remotePath, localPath string) error {
	srcFile, err := client.Open(remotePath)
	if err != nil {
		return fmt.Errorf("open remote file: %w", err)
	}
	defer srcFile.Close()

	dstFile, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("create local file: %w", err)
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	return err
}

func sftpUpload(client *sftp.Client, localPath, remotePath string) error {
	srcFile, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open local file: %w", err)
	}
	defer srcFile.Close()

	dstFile, err := client.Create(remotePath)
	if err != nil {
		return fmt.Errorf("create remote file: %w", err)
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	return err
}

// hostKeyLogger returns a host key callback that logs unknown keys
// and accepts them. In production, this should verify against known_hosts.
func hostKeyLogger(clientName string) gossh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key gossh.PublicKey) error {
		slog.Info("SSH 主机密钥",
			"client", clientName,
			"hostname", hostname,
			"remote", remote.String(),
			"fingerprint", gossh.FingerprintSHA256(key),
		)
		return nil // accept all keys
	}
}
