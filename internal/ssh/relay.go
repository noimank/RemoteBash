package ssh

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"

	gossh "golang.org/x/crypto/ssh"
)

// Relay commands tried in order. The first one whose SSH process starts
// successfully wins.
var relayCandidates = []string{
	"socat STDIO TCP:{host}:{port},connect-timeout=10 2>/dev/null",
	"nc {host} {port} 2>/dev/null",
	"ncat {host} {port} 2>/dev/null",
	"python3 -c 'import socket,select,sys,os;s=socket.socket();s.connect((\"{host}\",{port}));si=sys.stdin.buffer;so=sys.stdout.buffer;sif=sys.stdin.fileno();sf=s.fileno();exec(\"while 1:\\n r,_,_=select.select([sif,sf],[],[])\\n if sif in r:\\n  d=os.read(sif,4096)\\n  if not d:break\\n  s.sendall(d)\\n if sf in r:\\n  d=s.recv(4096)\\n  if not d:break\\n  so.write(d);so.flush()\");s.close()' 2>/dev/null",
}

const relayReadChunk = 65536

// RelayError is returned when no relay tool is available on the jump host.
type RelayError struct {
	Msg string
}

func (e *RelayError) Error() string {
	return e.Msg
}

// SocatTunnelRelay bridges a local net.Pipe through a jump-host TCP relay
// process when the jump host's SSH server prohibits TCP forwarding.
type SocatTunnelRelay struct {
	jumpConn   *gossh.Client
	targetHost string
	targetPort int

	mu      sync.Mutex
	session *gossh.Session
	stdin   io.WriteCloser
	stdout  io.Reader

	connA  net.Conn // caller side
	connB  net.Conn // bridge side
	closed bool
	done   chan struct{}
}

// NewSocatTunnelRelay creates a relay for the given jump host and target.
func NewSocatTunnelRelay(jumpConn *gossh.Client, targetHost string, targetPort int) *SocatTunnelRelay {
	return &SocatTunnelRelay{
		jumpConn:   jumpConn,
		targetHost: targetHost,
		targetPort: targetPort,
		done:       make(chan struct{}),
	}
}

// Connect starts the relay and returns a net.Conn for use with
// gossh.NewClientConn(conn, addr, config).
func (r *SocatTunnelRelay) Connect() (net.Conn, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return nil, fmt.Errorf("relay is already closed")
	}

	// Create a net.Pipe for the bridge.
	r.connA, r.connB = net.Pipe()

	// Try each relay candidate.
	var lastErr error
	for _, cmdTemplate := range relayCandidates {
		cmd := relayCommand(cmdTemplate, r.targetHost, r.targetPort)
		session, err := r.jumpConn.NewSession()
		if err != nil {
			lastErr = err
			continue
		}

		stdin, err := session.StdinPipe()
		if err != nil {
			session.Close()
			lastErr = err
			continue
		}
		stdout, err := session.StdoutPipe()
		if err != nil {
			session.Close()
			lastErr = err
			continue
		}
		// Consume stderr to prevent pipe buffer deadlock.
		stderr, err := session.StderrPipe()
		if err != nil {
			session.Close()
			lastErr = err
			continue
		}
		go io.Copy(io.Discard, stderr)

		if err := session.Start(cmd); err != nil {
			session.Close()
			lastErr = err
			slog.Debug("中继工具不可用", "tool", cmdTemplate[:min(10, len(cmdTemplate))], "err", err)
			continue
		}

		r.session = session
		r.stdin = stdin
		r.stdout = stdout
		slog.Info("跳板机中继进程已启动", "cmd", cmd)
		break
	}

	if r.session == nil {
		r.cleanupConnLocked()
		return nil, &RelayError{
			Msg: fmt.Sprintf("no relay tool available on jump host. Tried: socat, nc, ncat, python3. Last error: %v", lastErr),
		}
	}

	// Start bidirectional bridge goroutines.
	go r.bridgeStdout()
	go r.bridgeStdin()

	return r.connA, nil
}

// Close tears down the relay. Safe to call multiple times.
func (r *SocatTunnelRelay) Close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	r.mu.Unlock()

	r.cleanupConn()

	r.mu.Lock()
	session := r.session
	r.session = nil
	r.mu.Unlock()

	if session != nil {
		session.Close()
		// Wait for bridge goroutines to finish now that session is closed.
		<-r.done
	}
}

func (r *SocatTunnelRelay) bridgeStdout() {
	defer close(r.done)
	buf := make([]byte, relayReadChunk)
	for {
		n, err := r.stdout.Read(buf)
		if n > 0 {
			if _, werr := r.connB.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (r *SocatTunnelRelay) bridgeStdin() {
	buf := make([]byte, relayReadChunk)
	for {
		n, err := r.connB.Read(buf)
		if n > 0 {
			r.mu.Lock()
			stdin := r.stdin
			r.mu.Unlock()
			if stdin == nil {
				return
			}
			if _, werr := stdin.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (r *SocatTunnelRelay) cleanupConnLocked() {
	if r.connB != nil {
		r.connB.Close()
		r.connB = nil
	}
	if r.connA != nil {
		r.connA.Close()
		r.connA = nil
	}
}

func (r *SocatTunnelRelay) cleanupConn() {
	r.mu.Lock()
	r.cleanupConnLocked()
	r.mu.Unlock()
}

func relayCommand(template, host string, port int) string {
	replacer := strings.NewReplacer(
		"{host}", host,
		"{port}", fmt.Sprintf("%d", port),
	)
	return replacer.Replace(template)
}
