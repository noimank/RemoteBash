package ssh

import (
	"context"
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
//
// Lock model (acquire outer → inner; never violate this order):
//
//	execLock    → connectLock → shellLock → connMu
//
//	execLock     serialises MCP Exec callers.
//	connectLock  serialises the connection lifecycle: a Connect dial/publish
//	             and a Disconnect claim are mutually exclusive, so after
//	             Disconnect returns the session stays down until the next
//	             explicit Connect.
//	shellLock    guards shell, shellType, homeCache, cwd.
//	connMu       (RWMutex) guards conn and keepaliveDone. All conn reads go
//	             through snapshotConn(); writers are Connect (publish) and
//	             Disconnect (claim), both under connectLock.
type RemoteSession struct {
	Name     string
	Host     string
	Port     int
	User     string
	Password string
	Enabled  bool
	SafeRm   bool
	Via      string // jump host client name, empty = direct

	conn        *gossh.Client
	connMu      sync.RWMutex // guards conn and keepaliveDone
	connectLock sync.Mutex   // serialises Connect vs Disconnect

	shell     *PersistentShell // MCP exec shell
	shellType string           // detected remote shell (ash, bash, ...)
	shellLock sync.Mutex       // guards shell, shellType, homeCache, cwd
	execLock  sync.Mutex       // serialises concurrent Exec callers

	cwd       string
	homeCache string // cached remote $HOME, cleared on disconnect

	keepaliveDone  chan struct{} // under connMu; closed to stop keepaliveLoop
	auditCb        AuditCallback
	tunnelResolver TunnelResolver // name → jump-host *gossh.Client
	relay          *SocatTunnelRelay
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
	return s.snapshotConn() != nil
}

// snapshotConn returns the current SSH client under connMu, or nil if
// disconnected. Every read of s.conn must go through here so it never races
// with Connect/Disconnect.
func (s *RemoteSession) snapshotConn() *gossh.Client {
	s.connMu.RLock()
	defer s.connMu.RUnlock()
	return s.conn
}

// getCwd returns the last known remote working directory (for display).
func (s *RemoteSession) getCwd() string {
	s.shellLock.Lock()
	defer s.shellLock.Unlock()
	return s.cwd
}

// setCwd sets the remote working directory. Callers already holding shellLock
// must write s.cwd directly to avoid self-deadlock.
func (s *RemoteSession) setCwd(v string) {
	s.shellLock.Lock()
	s.cwd = v
	s.shellLock.Unlock()
}

// ── SSH keepalive (two-layer) ─────────────────────────────────────────
//
// Layer 1 — TCP keepalive (SO_KEEPALIVE, 15 s): OS-level dead-peer detection.
//   Catches remote host crash, physical disconnection, or network partition.
//   Set on the net.Conn at dial time for the direct connection path.
//
// Layer 2 — SSH keepalive (keepalive@openssh.com, 30 s, wantReply=false):
//   Fire-and-forget global request that prevents NAT gateways, firewalls,
//   and sshd ServerAliveCountMax from dropping truly idle encrypted tunnels.
//   Non-OpenSSH servers silently ignore unknown requests (RFC 4254 §4), so
//   this is safe for Dropbear, Tectia, libssh, and network-device SSHds.
//
// Between the two layers, the connection stays alive through nearly every
// idle-timeout scenario: physical, network, and application.

// startKeepalive launches the SSH-layer keepalive goroutine.
// Must only be called when a connection has just been published.
// keepaliveDone is created under connMu so it cannot race with Disconnect's
// close of the same channel.
func (s *RemoteSession) startKeepalive() {
	s.connMu.Lock()
	s.keepaliveDone = make(chan struct{})
	s.connMu.Unlock()
	go s.keepaliveLoop()
}

// stopKeepalive signals the keepalive goroutine to exit.
// Safe to call when the goroutine is not running (nil or already-closed
// keepaliveDone).
func (s *RemoteSession) stopKeepalive() {
	s.connMu.Lock()
	done := s.keepaliveDone
	s.connMu.Unlock()
	if done == nil {
		return
	}
	select {
	case <-done:
	default:
		close(done)
	}
}

func (s *RemoteSession) keepaliveLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	// Capture the stop channel once at goroutine start. Without this, a later
	// startKeepalive replacing s.keepaliveDone would leave this goroutine
	// selecting on a fresh channel that is never closed — a leak.
	s.connMu.Lock()
	done := s.keepaliveDone
	s.connMu.Unlock()

	for {
		select {
		case <-ticker.C:
			conn := s.snapshotConn()
			if conn == nil {
				return
			}

			// Time-bounded SendRequest: even with wantReply=false the
			// underlying TCP write can block on a dead connection whose
			// socket buffer is full. A goroutine + select keeps this loop
			// responsive regardless.
			errCh := make(chan error, 1)
			go func() {
				_, _, err := conn.SendRequest("keepalive@openssh.com", false, nil)
				errCh <- err
			}()

			var sendErr error
			select {
			case sendErr = <-errCh:
			case <-time.After(10 * time.Second):
				sendErr = fmt.Errorf(
					"keepalive SendRequest timed out after 10s "+
						"(remote %s:%d may be unreachable)",
					s.Host, s.Port)
			case <-done:
				return
			}

			if sendErr != nil {
				// Re-check the stop signal before tearing down: a concurrent
				// reconnect may have replaced this connection, in which case
				// the error is just the old one dying.
				select {
				case <-done:
					return
				default:
				}
				slog.Warn("SSH keepalive 丢失，断开连接", "client", s.Name, "err", sendErr)
				s.Disconnect()
				return
			}
		case <-done:
			return
		}
	}
}

// RawConn returns the underlying *gossh.Client, used for tunnel resolution.
func (s *RemoteSession) RawConn() *gossh.Client {
	return s.snapshotConn()
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
// Enabled flag does NOT gate connectivity — it only controls MCP visibility
// (ListEnabled). A disabled jump host still serves its dependents.
func (s *RemoteSession) Connect() error {
	s.connectLock.Lock()
	defer s.connectLock.Unlock()

	// Atomic w.r.t. Disconnect (which also takes connectLock): no TOCTOU.
	if s.Connected() {
		return nil
	}
	s.stopKeepalive()

	config := &gossh.ClientConfig{
		User:            s.User,
		Auth:            []gossh.AuthMethod{gossh.Password(s.Password)},
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

				ncc, chans, reqs, relaySSHErr := gossh.NewClientConn(sock, addr, config)
				if relaySSHErr != nil {
					relay.Close()
					s.relay = nil
					return fmt.Errorf("ssh over relay: %w", relaySSHErr)
				}
				s.publishConn(gossh.NewClient(ncc, chans, reqs))
				slog.Info("跳板机中继连接成功", "via", s.Via, "target", fmt.Sprintf("%s:%d", s.Host, s.Port))
				return nil
			}
			return fmt.Errorf("tunnel dial: %w", err)
		}

		ncc, chans, reqs, tunnelSSHErr := gossh.NewClientConn(conn, addr, config)
		if tunnelSSHErr != nil {
			return fmt.Errorf("ssh over tunnel: %w", tunnelSSHErr)
		}
		s.publishConn(gossh.NewClient(ncc, chans, reqs))
		return nil
	}

	// Direct connection with TCP keepalive enabled on the underlying socket.
	// The OS periodically probes an idle TCP connection so a dead remote host
	// or network partition is detected quickly.
	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	d := net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 15 * time.Second,
	}
	tcpConn, err := d.Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("connect %s: %w", addr, err)
	}
	ncc, chans, reqs, err := gossh.NewClientConn(tcpConn, addr, config)
	if err != nil {
		tcpConn.Close()
		return fmt.Errorf("ssh handshake %s: %w", addr, err)
	}
	s.publishConn(gossh.NewClient(ncc, chans, reqs))
	return nil
}

// publishConn installs a freshly established SSH client as the active
// connection (under connMu), resets session-scoped state, and starts
// keepalive. Must be called with connectLock held.
func (s *RemoteSession) publishConn(c *gossh.Client) {
	s.connMu.Lock()
	s.conn = c
	s.connMu.Unlock()
	s.setCwd("~")
	s.startKeepalive()
}

// Disconnect closes the SSH connection and all associated shells.
// Safe for concurrent calls.
//
// connectLock is held for the whole claim so this cannot interleave with a
// Connect dial/publish: after Disconnect returns, the session stays down
// until the next explicit Connect. connMu guards only the conn pointer and
// keepaliveDone; the slow steps (conn.Close, shell teardown) run outside it.
func (s *RemoteSession) Disconnect() {
	s.connectLock.Lock()
	defer s.connectLock.Unlock()

	// Step 1: claim the connection and stop keepalive atomically.
	s.connMu.Lock()
	conn := s.conn
	s.conn = nil
	if s.keepaliveDone != nil {
		select {
		case <-s.keepaliveDone:
		default:
			close(s.keepaliveDone)
		}
	}
	s.connMu.Unlock()

	// Step 2: close the SSH client outside any lock. On a truly dead
	// connection the TCP write inside Close() (SSH_MSG_DISCONNECT) can block
	// indefinitely, so bound it.
	if conn != nil {
		done := make(chan struct{})
		go func() { conn.Close(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			slog.Warn("连接关闭超时，强制断开", "client", s.Name)
		}
	}

	// Step 3: with the mux gone, tear down the shell under shellLock.
	s.shellLock.Lock()
	if s.shell != nil {
		s.shell.Close()
		s.shell = nil
	}
	s.shellType = ""
	s.homeCache = ""
	s.shellLock.Unlock()

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

	conn := s.snapshotConn()
	if conn == nil {
		return nil, fmt.Errorf("not connected")
	}

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

	shell := NewPersistentShell(conn, defaultCols, defaultRows, s.SafeRm, initScript, "")
	if err := shell.Start(); err != nil {
		return nil, fmt.Errorf("start mcp shell: %w", err)
	}

	s.shell = shell
	s.shellType = shell.ShellType()
	s.cwd = "~"
	return s.shell, nil
}

// Exec runs a command on the persistent interactive shell.
// Uses lazy connect: establishes the SSH connection on first call.
// Concurrent callers are serialised via execLock.
func (s *RemoteSession) Exec(command string, timeout time.Duration) (*CommandOutput, error) {
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
				Cwd:        s.getCwd(),
				DurationMs: elapsed,
			})
		}
		s.Disconnect()
		return nil, fmt.Errorf("ssh command failed: %w", err)
	}

	s.setCwd(result.Cwd)
	if s.auditCb != nil {
		s.auditCb(s.Name, command, result)
	}
	return result, nil
}

// Transfer performs an SFTP file transfer.
//
// ctx controls cancellation/timeout: on ctx.Done the open file handles are
// closed to unblock stalled SFTP I/O, and the copy returns a cancellation
// error. Uploads use pipelined (concurrent) SFTP writes for throughput on
// high-latency links; downloads already use concurrent reads by default.
func (s *RemoteSession) Transfer(ctx context.Context, src, dst, direction string) (*TransferOutput, error) {
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

	// Snapshot conn so a concurrent Disconnect can't nil it between Connect()
	// above and sftp.NewClient() below.
	conn := s.snapshotConn()
	if conn == nil {
		return nil, fmt.Errorf("disconnected before transfer")
	}

	// Open SFTP session over the SSH client. Concurrent writes pipeline write
	// packets — the main throughput win for uploads over WAN links.
	sftpClient, err := sftp.NewClient(conn, sftp.UseConcurrentWrites(true))
	if err != nil {
		return nil, fmt.Errorf("sftp session: %w", err)
	}
	defer sftpClient.Close()

	// Total size drives progress percentage (best-effort).
	var total int64
	switch direction {
	case "remote2local":
		if st, statErr := sftpClient.Stat(src); statErr == nil {
			total = st.Size()
		}
	case "local2remote":
		if st, statErr := os.Stat(src); statErr == nil {
			total = st.Size()
		}
	default:
		return nil, fmt.Errorf("invalid direction '%s', expected 'remote2local' or 'local2remote'", direction)
	}

	// Open source/destination handles per direction.
	var (
		srcReader io.Reader
		dstWriter io.Writer
		srcCloser io.Closer
		dstCloser io.Closer
	)
	switch direction {
	case "remote2local":
		remoteFile, e := sftpClient.Open(src)
		if e != nil {
			return nil, fmt.Errorf("open remote file: %w", e)
		}
		localFile, e := os.Create(dst)
		if e != nil {
			remoteFile.Close()
			return nil, fmt.Errorf("create local file: %w", e)
		}
		srcReader, srcCloser = remoteFile, remoteFile
		dstWriter, dstCloser = localFile, localFile
	case "local2remote":
		localFile, e := os.Open(src)
		if e != nil {
			return nil, fmt.Errorf("open local file: %w", e)
		}
		remoteFile, e := sftpClient.Create(dst)
		if e != nil {
			localFile.Close()
			return nil, fmt.Errorf("create remote file: %w", e)
		}
		srcReader, srcCloser = localFile, localFile
		dstWriter, dstCloser = remoteFile, remoteFile
	}
	defer srcCloser.Close()
	defer dstCloser.Close()

	// progressWriter wraps the destination so io.Copy streams through it in
	// both directions (upload via the generic copy loop, download via the
	// remote file's WriteTo, which calls our Write).
	pw := &progressWriter{
		w:         dstWriter,
		total:     total,
		start:     t0,
		lastLogAt: t0,
		client:    s.Name,
		dir:       direction,
	}

	// On ctx cancellation close both handles to unblock stalled SFTP I/O.
	// This tears down only the SFTP files (a channel-level op), not the SSH
	// connection — a parallel Exec on the same session keeps running.
	cancelStop := context.AfterFunc(ctx, func() {
		srcCloser.Close()
		dstCloser.Close()
	})
	defer cancelStop()

	// 1 MiB copy buffer feeds the SFTP writer more data per round-trip than
	// io.Copy's default 32 KiB.
	buf := make([]byte, 1024*1024)
	if _, err := io.CopyBuffer(pw, srcReader, buf); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("sftp transfer cancelled: %w", ctxErr)
		}
		return nil, fmt.Errorf("sftp %s: %w", direction, err)
	}

	// Best-effort stat the remote file for size reporting.
	var size int64
	remoteStatPath := src
	if direction == "local2remote" {
		remoteStatPath = dst
	}
	if st, statErr := sftpClient.Stat(remoteStatPath); statErr == nil {
		size = st.Size()
	}

	elapsed := time.Since(t0)
	elapsedMs := int(elapsed.Milliseconds())
	var bps int64
	if elapsed > 0 {
		bps = int64(float64(size) / elapsed.Seconds())
	}
	slog.Info("传输完成",
		"src", src, "dst", dst, "direction", direction,
		"bytes", size, "duration_ms", elapsedMs,
		"rate_mibs", fmt.Sprintf("%.2f", float64(bps)/(1024*1024)),
	)
	return &TransferOutput{
		Success:     true,
		Direction:   direction,
		Src:         src,
		Dst:         dst,
		SizeBytes:   size,
		DurationMs:  elapsedMs,
		BytesPerSec: bps,
	}, nil
}

// TransferOutput is the result of an SFTP transfer.
type TransferOutput struct {
	Success     bool
	Direction   string
	Src         string
	Dst         string
	SizeBytes   int64
	DurationMs  int
	BytesPerSec int64
}

// ExecLock acquires the exec lock (used by Manager during shutdown).
func (s *RemoteSession) ExecLock() { s.execLock.Lock() }

// ExecUnlock releases the exec lock.
func (s *RemoteSession) ExecUnlock() { s.execLock.Unlock() }

// home returns the remote $HOME, using a cached value after first fetch.
// $HOME is invariant for the life of an SSH connection, so it is safe to cache.
func (s *RemoteSession) home() string {
	s.shellLock.Lock()
	if s.homeCache != "" {
		h := s.homeCache
		s.shellLock.Unlock()
		return h
	}
	s.shellLock.Unlock()

	conn := s.snapshotConn()
	if conn == nil {
		return ""
	}

	// Use a short-lived SSH session (reuses the existing TCP/encrypted
	// connection — no re-auth) to avoid competing on execLock with concurrent
	// command executions.  Typical cost: 2 RTT.
	sess, err := conn.NewSession()
	if err != nil {
		return ""
	}
	defer sess.Close()

	out, err := sess.Output("echo $HOME")
	if err != nil {
		return ""
	}

	h := strings.TrimSpace(string(out))
	s.shellLock.Lock()
	s.homeCache = h
	s.shellLock.Unlock()
	return h
}

// expandRemotePath expands ~ to the remote home directory.
func (s *RemoteSession) expandRemotePath(path string) string {
	if !strings.Contains(path, "~") {
		return path
	}

	home := s.home()
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
	if err := s.Connect(); err != nil {
		return nil, err
	}

	conn := s.snapshotConn()
	if conn == nil {
		return nil, fmt.Errorf("disconnected before opening terminal shell")
	}

	var initScript string
	if s.SafeRm {
		initScript = SafeRmShim
	}

	shell := NewPersistentShell(conn, defaultCols, defaultRows, s.SafeRm, initScript, TerminalPS1)
	if err := shell.Start(); err != nil {
		return nil, fmt.Errorf("start terminal shell: %w", err)
	}
	return shell, nil
}

// ToInfo returns a serializable ClientInfo representation of the session.
func (s *RemoteSession) ToInfo() models.ClientInfo {
	connected := s.Connected()
	s.shellLock.Lock()
	cwd, shellType := s.cwd, s.shellType
	s.shellLock.Unlock()
	return models.ClientInfo{
		Name:      s.Name,
		Host:      s.Host,
		Port:      s.Port,
		User:      s.User,
		Connected: connected,
		Cwd:       cwd,
		Enabled:   s.Enabled,
		SafeRm:    s.SafeRm,
		Via:       s.Via,
		ShellType: shellType,
	}
}

// ── SFTP helpers ─────────────────────────────────────────────────────

const (
	// progressBytesInterval logs progress at least every 64 MiB transferred.
	progressBytesInterval = 64 * 1024 * 1024
	// progressTimeInterval also logs progress every 10s, so slow transfers
	// of files smaller than 64 MiB stay visible.
	progressTimeInterval = 10 * time.Second
)

// progressWriter wraps an io.Writer and logs transfer progress at regular
// byte/time intervals. Only the bytes that flow through the destination are
// counted, which for SFTP is the full payload in both directions.
type progressWriter struct {
	w            io.Writer
	written      int64
	total        int64
	start        time.Time
	client       string
	dir          string
	lastLogBytes int64
	lastLogAt    time.Time
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n, err := pw.w.Write(p)
	pw.written += int64(n)
	if pw.total <= 0 {
		return n, err
	}
	if pw.written-pw.lastLogBytes >= progressBytesInterval ||
		time.Since(pw.lastLogAt) >= progressTimeInterval {
		pw.log()
		pw.lastLogBytes = pw.written
		pw.lastLogAt = time.Now()
	}
	return n, err
}

func (pw *progressWriter) log() {
	elapsed := time.Since(pw.start).Seconds()
	pct := float64(pw.written) / float64(pw.total) * 100
	var rateMiB float64
	if elapsed > 0 {
		rateMiB = float64(pw.written) / elapsed / (1024 * 1024)
	}
	slog.Info("传输进度",
		"client", pw.client,
		"direction", pw.dir,
		"read_bytes", pw.written,
		"total_bytes", pw.total,
		"pct", fmt.Sprintf("%.1f", pct),
		"rate_mibs", fmt.Sprintf("%.2f", rateMiB),
	)
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
