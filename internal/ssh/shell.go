package ssh

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

// Default PTY dimensions.
const (
	defaultCols = 200
	defaultRows = 50
	readChunk   = 4096
)

// TTY opcode for ECHO (RFC 4254 §8).
const ttyOpEcho = 53

// Framing sentinels.
const (
	startPrefix = "__RBSH_START__"
	donePrefix  = "__RBSH_DONE__"
	readyMarker = "__RBSH_READY__"
)

// Max bytes to buffer when the terminal path is active (512 KiB).
const terminalBufMax = 512 * 1024

// ── ANSI stripping ────────────────────────────────────────────────────

var ansiRe = regexp.MustCompile(
	"\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)" + // OSC ... BEL / ST
		"|\x1b[@-Z\\\\-_]" + // single-char ESC X
		"|\x1b\\[[0-9;?]*[ -/]*[@-~]", // CSI ...
)

// StripANSI removes ANSI escape sequences from binary data.
func StripANSI(data []byte) []byte {
	return ansiRe.ReplaceAll(data, nil)
}

// ── PersistentShell ───────────────────────────────────────────────────

// TapCallback receives raw PTY output bytes for the browser terminal path.
type TapCallback func(chunk []byte)

// TapHandle is a unique identifier for a registered tap, returned by AttachTap.
type TapHandle int

// PersistentShell manages a single long-lived interactive PTY bash shell
// on an SSH connection.
//
// Concurrency model:
//   - s.mu protects all mutable state: buf, pending, ready, closed.
//   - s.tapsMu protects the taps slice.
//   - readerLoop is a single goroutine; it acquires mu, reads+scans,
//     then releases mu before firing tap callbacks.
//   - Run() serialises callers via mu; it sets pending under mu, then
//     the readerLoop's scan() resolves it under mu.
//   - Close() takes mu and tears everything down.
type PersistentShell struct {
	conn           *gossh.Client
	session        *gossh.Session
	stdin          io.WriteCloser
	stdout         io.Reader
	cols, rows     int
	safeRm         bool
	initScript     string
	promptTemplate string
	shellType      string // detected at startup: ash, bash, dash, zsh, etc.

	mu      sync.Mutex
	buf     []byte
	pending *pendingCmd
	ready   *readyState
	closed  bool

	taps    []tapEntry
	tapsMu  sync.RWMutex
	nextTap TapHandle

	ctx    context.Context
	cancel context.CancelFunc
}

type readyState struct {
	done chan struct{} // closed when init is complete
}

type tapEntry struct {
	cb     TapCallback
	handle TapHandle
}

type pendingCmd struct {
	pattern *regexp.Regexp
	result  chan cmdResult
}

type cmdResult struct {
	rawOutput []byte
	exitCode  int
	cwd       string
	err       error
}

// NewPersistentShell creates an unstarted PersistentShell.
func NewPersistentShell(conn *gossh.Client, cols, rows int, safeRm bool,
	initScript, promptTemplate string) *PersistentShell {

	ctx, cancel := context.WithCancel(context.Background())
	return &PersistentShell{
		conn:           conn,
		cols:           cols,
		rows:           rows,
		safeRm:         safeRm,
		initScript:     initScript,
		promptTemplate: promptTemplate,
		ready:          &readyState{done: make(chan struct{})},
		ctx:            ctx,
		cancel:         cancel,
	}
}

// Start opens the PTY bash shell and initialises the selected mode.
func (s *PersistentShell) Start() error {
	session, err := s.conn.NewSession()
	if err != nil {
		return fmt.Errorf("new session: %w", err)
	}
	s.session = session

	// MCP path disables echo to prevent PTY echo of sentinel wrapper lines.
	modes := gossh.TerminalModes{}
	if s.promptTemplate == "" {
		modes[ttyOpEcho] = 0
	}

	if err := session.RequestPty("xterm-256color", s.rows, s.cols, modes); err != nil {
		session.Close()
		return fmt.Errorf("request pty: %w", err)
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		session.Close()
		return fmt.Errorf("stdin pipe: %w", err)
	}
	s.stdin = stdin

	stdout, err := session.StdoutPipe()
	if err != nil {
		session.Close()
		return fmt.Errorf("stdout pipe: %w", err)
	}
	s.stdout = stdout

	if err := session.Shell(); err != nil {
		session.Close()
		return fmt.Errorf("start shell: %w", err)
	}

	go s.readerLoop()

	// Step 1 — Kill echo BEFORE the init payload.
	if err := s.sendInit("stty -echo 2>/dev/null"); err != nil {
		s.Close()
		return fmt.Errorf("disable echo: %w", err)
	}

	// Step 2 — Mode-specific init.
	if s.promptTemplate != "" {
		ps1Cmd := fmt.Sprintf("export PS1='%s'", s.promptTemplate)
		init := fmt.Sprintf(
			"printf '%%s' '%s'; %s; export PS2=''; export PROMPT_COMMAND=; printf '\\r\\033[2K'; true",
			readyMarker, ps1Cmd,
		)
		if err := s.sendInit(init); err != nil {
			s.Close()
			return fmt.Errorf("terminal init: %w", err)
		}
	} else {
		if err := s.sendInit("true"); err != nil {
			s.Close()
			return fmt.Errorf("mcp init: %w", err)
		}
	}

	// Step 3 — Optional init script while echo is OFF.
	if s.initScript != "" {
		if err := s.sendInit("unalias rm 2>/dev/null"); err != nil {
			s.Close()
			return fmt.Errorf("unalias rm: %w", err)
		}
		escaped := strings.ReplaceAll(s.initScript, "'", "'\\''")
		if err := s.sendInit("eval '"+escaped+"' 2>/dev/null; true"); err != nil {
			s.Close()
			return fmt.Errorf("init script: %w", err)
		}
	}

	// Step 4 — Re-enable echo for browser terminal.
	if s.promptTemplate != "" {
		if err := s.sendInit("stty echo 2>/dev/null"); err != nil {
			s.Close()
			return fmt.Errorf("enable echo: %w", err)
		}
	}

	// MCP path: verify framing works, then detect remote shell type.
	if s.promptTemplate == "" {
		if _, err := s.Run("true", 15*time.Second); err != nil {
			s.Close()
			return fmt.Errorf("shell framing verification failed: %w", err)
		}
		s.detectShellType()
		s.mu.Lock()
		close(s.ready.done)
		s.mu.Unlock()
		return nil
	}

	// Browser terminal: wait for readiness marker or process exit.
	deadline := time.Now().Add(15 * time.Second)
	for {
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if closed {
			return fmt.Errorf("shell closed during init")
		}

		select {
		case <-s.ready.done:
			return nil
		case <-time.After(300 * time.Millisecond):
			if time.Now().After(deadline) {
				s.Close()
				return fmt.Errorf("remote shell did not produce a prompt within 15s — the host may not have /bin/bash")
			}
		case <-s.ctx.Done():
			return fmt.Errorf("shell closed during init")
		}
	}
}

func (s *PersistentShell) sendInit(line string) error {
	_, err := s.stdin.Write([]byte(line + "\n"))
	return err
}

// ── Reader loop ──────────────────────────────────────────────────────

func (s *PersistentShell) readerLoop() {
	buf := make([]byte, readChunk)
	for {
		n, err := s.stdout.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])

			// Fire tap callbacks outside the lock.
			s.tapsMu.RLock()
			taps := make([]tapEntry, len(s.taps))
			copy(taps, s.taps)
			s.tapsMu.RUnlock()
			for _, t := range taps {
				func() {
					defer func() { recover() }()
					t.cb(chunk)
				}()
			}

			// Append and scan under the main lock.
			s.mu.Lock()
			s.buf = append(s.buf, chunk...)
			s.scanLocked()
			s.mu.Unlock()
		}
		if err != nil {
			if err != io.EOF || n == 0 {
				slog.Warn("读取循环已结束", "err", err)
			}
			break
		}
	}

	// If a command is still outstanding, fail it.
	s.mu.Lock()
	if s.pending != nil {
		select {
		case s.pending.result <- cmdResult{err: fmt.Errorf("remote shell closed before command finished")}:
		default:
		}
		s.pending = nil
	}
	s.mu.Unlock()
}

// scanLocked scans buf for a readiness marker or a pending command's done
// token. Must be called with s.mu held.
func (s *PersistentShell) scanLocked() {
	// Terminal-mode readiness detection.
	if s.promptTemplate != "" {
		select {
		case <-s.ready.done:
			// already ready
		default:
			idx := bytes.Index(s.buf, []byte(readyMarker))
			if idx >= 0 {
				nl := bytes.IndexByte(s.buf[idx:], '\n')
				end := idx + len(readyMarker)
				if nl >= 0 {
					end = idx + nl + 1
				}
				s.buf = s.buf[end:]
				close(s.ready.done)
				return
			}
		}
	}

	if s.pending == nil {
		// No active command — keep memory bounded for terminal path.
		select {
		case <-s.ready.done:
			if len(s.buf) > terminalBufMax {
				s.buf = s.buf[len(s.buf)-terminalBufMax/2:]
			}
		default:
		}
		return
	}

	// Scan for the done token.
	m := s.pending.pattern.FindSubmatch(s.buf)
	if m == nil {
		return
	}

	rawOutput := make([]byte, len(m[1]))
	copy(rawOutput, m[1])
	exitCode := 0
	if _, err := fmt.Sscanf(string(m[2]), "%d", &exitCode); err != nil {
		slog.Warn("scanLocked: 无法解析退出码", "captured", string(m[2]))
	}
	cwd := strings.TrimRight(string(m[3]), " ")
	s.buf = s.buf[len(m[0]):]

	p := s.pending
	s.pending = nil
	select {
	case p.result <- cmdResult{rawOutput: rawOutput, exitCode: exitCode, cwd: cwd}:
	default:
	}
}

// ── Command execution (MCP path) ──────────────────────────────────────

// Run executes a command on the persistent shell and returns its output.
// Callers are serialised by the internal mutex.
func (s *PersistentShell) Run(command string, timeout time.Duration) (*CommandOutput, error) {
	t0 := time.Now()

	// Phase 1: dispatch under mu — set up the pending command and write to stdin.
	resultCh, token, err := s.dispatch(command)
	if err != nil {
		return nil, err
	}

	// Phase 2: wait without mu — readerLoop can call scanLocked().
	return s.waitForResult(command, token, timeout, resultCh, t0)
}

// dispatch writes the command payload and returns a result channel + token.
// It holds mu for its entire duration, releasing it before returning.
// Callers MUST NOT hold mu.
func (s *PersistentShell) dispatch(command string) (chan cmdResult, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil, "", fmt.Errorf("persistent shell is closed")
	}
	if s.pending != nil {
		return nil, "", fmt.Errorf("another command is already running on this shell")
	}

	// Discard stale idle bytes.
	s.buf = s.buf[:0]

	token := randomToken()
	resultCh := make(chan cmdResult, 1)
	s.pending = &pendingCmd{pattern: s.donePattern(token), result: resultCh}

	payload := s.buildPayload(command, token)
	if _, err := s.stdin.Write(payload); err != nil {
		s.pending = nil
		return nil, "", fmt.Errorf("write command: %w", err)
	}

	return resultCh, token, nil
}

// waitForResult blocks on the result channel or timeout.
// Must be called without mu held.
func (s *PersistentShell) waitForResult(command, token string, timeout time.Duration, resultCh chan cmdResult, t0 time.Time) (*CommandOutput, error) {
	ctx, cancel := context.WithTimeout(s.ctx, timeout)
	defer cancel()

	select {
	case r := <-resultCh:
		s.mu.Lock()
		defer s.mu.Unlock()

		if r.err != nil {
			return nil, r.err
		}

		elapsed := time.Since(t0)
		clean := s.cleanOutput(r.rawOutput, command, token)

		return &CommandOutput{
			Output:     clean,
			ExitCode:   r.exitCode,
			Cwd:        r.cwd,
			DurationMs: int(elapsed.Milliseconds()),
		}, nil

	case <-ctx.Done():
		// Capture partial output under mu, then release for Close().
		s.mu.Lock()
		partialBuf := make([]byte, len(s.buf))
		copy(partialBuf, s.buf)
		s.pending = nil
		s.mu.Unlock()

		partial := s.cleanPartialOutput(partialBuf, command, token)
		s.Close()

		detail := fmt.Sprintf(
			"Command timed out after %v.\nremote_shell cannot answer interactive prompts. "+
				"The command may be waiting for input; retry with non-interactive flags or "+
				"include the required input in the command.\n"+
				"The remote shell session was reset.", timeout.Round(time.Second))
		if partial != "" {
			detail += "\nOutput captured before timeout:\n" + partial
		}
		return nil, fmt.Errorf("%s", detail)
	}
}

// CommandOutput is the result returned by Run().
type CommandOutput struct {
	Output     string
	ExitCode   int
	Cwd        string
	DurationMs int
}

// ── Raw passthrough (browser terminal path) ───────────────────────────

// AttachTap registers a callback that receives raw PTY output.
// Returns a handle that can be used to detach the tap.
func (s *PersistentShell) AttachTap(cb TapCallback) func() {
	s.tapsMu.Lock()
	s.nextTap++
	h := s.nextTap
	s.taps = append(s.taps, tapEntry{cb: cb, handle: h})
	s.tapsMu.Unlock()

	return func() {
		s.tapsMu.Lock()
		defer s.tapsMu.Unlock()
		s.taps = slices.DeleteFunc(s.taps, func(e tapEntry) bool {
			return e.handle == h
		})
	}
}

// ClearBuffer discards buffered PTY output (called after attaching a new tap).
func (s *PersistentShell) ClearBuffer() {
	s.mu.Lock()
	s.buf = s.buf[:0]
	s.mu.Unlock()
}

// FeedRaw sends raw bytes to the PTY stdin (browser terminal keystrokes).
func (s *PersistentShell) FeedRaw(data []byte) error {
	if s.stdin == nil {
		return fmt.Errorf("persistent shell is not running")
	}
	_, err := s.stdin.Write(data)
	return err
}

// Resize changes the PTY dimensions (called when the browser terminal resizes).
func (s *PersistentShell) Resize(cols, rows int) error {
	s.cols, s.rows = cols, rows
	if s.session != nil {
		return s.session.WindowChange(rows, cols)
	}
	return nil
}

// Alive reports whether the shell is running.
func (s *PersistentShell) Alive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed && s.session != nil
}

// SafeRmFlag returns the safe_rm flag that was baked-in at shell start.
func (s *PersistentShell) SafeRmFlag() bool {
	return s.safeRm
}

// ShellType returns the detected remote shell name (e.g. "ash", "bash", "dash", "zsh").
// Empty string means not yet detected. Only populated for the MCP path.
func (s *PersistentShell) ShellType() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shellType
}

// detectShellType runs a lightweight POSIX command to identify the remote shell.
// Must only be called during Start(), on the MCP path (promptTemplate == "").
func (s *PersistentShell) detectShellType() {
	// Pure shell parameter expansion — no external binaries needed.
	// Strips leading '-' (login shell marker) and leading path (e.g. /bin/ →).
	detectCmd := `_o=$0; _o=${_o#-}; echo "${_o##*/}"`
	result, err := s.Run(detectCmd, 5*time.Second)
	if err != nil {
		slog.Warn("shell类型检测失败", "err", err)
		return
	}
	s.shellType = strings.TrimSpace(result.Output)
	slog.Info("检测到远程shell类型", "type", s.shellType)
}

// Close tears down the shell.
func (s *PersistentShell) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.cancel()

	if s.pending != nil {
		select {
		case s.pending.result <- cmdResult{err: fmt.Errorf("shell closed")}:
		default:
		}
		s.pending = nil
	}

	if s.session != nil {
		s.session.Close()
		s.session = nil
	}
	s.stdin = nil
	s.stdout = nil
}

// ── Command payload construction ──────────────────────────────────────

func (s *PersistentShell) buildPayload(command, token string) []byte {
	quoted := bashQuote(command)
	payload := fmt.Sprintf(
		"__rbsh_opts=$-; "+
			"case $__rbsh_opts in *e*) set +e; __rbsh_restore_e=1;; *) __rbsh_restore_e=0;; esac; "+
			"__rbsh_cmd=%s; "+
			"printf '%s:%s__\\n'; "+
			"eval \"$__rbsh_cmd\"; "+
			"__rbsh_ec=$?; "+
			"printf '%s:%s:%%s:CWD:%%s__\\n' \"$__rbsh_ec\" \"$PWD\"; "+
			"[ \"$__rbsh_restore_e\" = 1 ] && set -e; "+
			"unset __rbsh_cmd __rbsh_ec __rbsh_opts __rbsh_restore_e\n",
		quoted, startPrefix, token, donePrefix, token,
	)
	return []byte(payload)
}

func (s *PersistentShell) donePattern(token string) *regexp.Regexp {
	return regexp.MustCompile(
		`(?s)` + // dot matches newline — command output spans multiple lines
			regexp.QuoteMeta(startPrefix+":"+token+"__") +
			` ?\r?\n` +
			`(.*?)` +
			regexp.QuoteMeta(donePrefix+":"+token+":") +
			`(-?\d+):CWD:(.*?)__ ?\r?\n`,
	)
}

// ── Output cleaning ───────────────────────────────────────────────────
//
// Pipeline: raw PTY bytes → collapseCSIErase → StripANSI →
//   removeBackspaceOverstrikes → normalise line endings →
//   strip control characters.
//
// Designed for MCP tool output: text delivered to an AI must be
// readable, UTF-8-clean, and free of terminal control artefacts.

// normalizeOutput cleans raw PTY output for AI consumption.
// Preserves UTF-8. Returns "" for nil/empty input.
func normalizeOutput(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}

	// Step 1: Collapse CSI erase-in-line sequences before stripping ANSI.
	// \x1b[K / \x1b[0K — erase to end of line (remove the escape).
	// \x1b[1K            — erase to start of line (keep text after).
	// \x1b[2K            — erase entire line (keep text after).
	text := collapseCSIErase(string(raw))

	// Step 2: Strip remaining ANSI escape sequences.
	clean := string(StripANSI([]byte(text)))

	// Step 3: Remove backspace overstrikes (rune-aware).
	clean = removeBackspaceOverstrikes(clean)

	// Step 4: Normalise line endings.
	// CRLF → LF; standalone CR (progress bars) → LF.
	clean = strings.ReplaceAll(clean, "\r\n", "\n")
	clean = strings.ReplaceAll(clean, "\r", "\n")

	// Step 5: Strip control characters.
	// Keep: \n (line feed), \t (tab), all printable chars (≥0x20, excludes DEL 0x7F).
	clean = stripControlChars(clean)

	return clean
}

// collapseCSIErase handles CSI K (erase-in-line) sequences.
// Operates line-by-line; only the escape bytes need ASCII parsing.
func collapseCSIErase(text string) string {
	// Fast path: no escape character present.
	if !strings.Contains(text, "\x1b[") {
		return text
	}

	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = collapseOneLineCSIErase(line)
	}
	return strings.Join(lines, "\n")
}

func collapseOneLineCSIErase(line string) string {
	if !strings.Contains(line, "\x1b[") {
		return line
	}

	// CSI 2K — erase entire line. Keep only what follows the last one.
	// CSI 1K — erase from start to cursor. Keep only what follows the last one.
	for _, seq := range []string{"\x1b[2K", "\x1b[1K"} {
		if idx := strings.LastIndex(line, seq); idx >= 0 {
			line = line[idx+len(seq):]
		}
	}

	// CSI 0K / CSI K — erase from cursor to end. Remove the sequence itself;
	// text before and after both survive (we lack cursor position).
	line = strings.ReplaceAll(line, "\x1b[0K", "")
	line = strings.ReplaceAll(line, "\x1b[K", "")

	return line
}

// stripControlChars removes C0/C1 control characters.
// Keeps: \n (0x0A), \t (0x09), and all printable characters (≥0x20, ≠0x7F).
func stripControlChars(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\n' || r == '\t' || (r >= 0x20 && r != 0x7F) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// removeBackspaceOverstrikes removes each \b and the full rune preceding it.
// Uses a two-pointer approach on []rune for O(n) complexity.
func removeBackspaceOverstrikes(s string) string {
	if !strings.Contains(s, "\b") {
		return s
	}

	runes := []rune(s)
	dst := 0
	for _, r := range runes {
		if r == '\b' {
			if dst > 0 {
				dst-- // erase the previous rune
			}
		} else {
			runes[dst] = r
			dst++
		}
	}
	return string(runes[:dst])
}

func (s *PersistentShell) cleanOutput(raw []byte, command, token string) string {
	if len(raw) == 0 {
		return ""
	}

	text := normalizeOutput(raw)
	lines := strings.Split(text, "\n")

	// Strip leading blank lines (typically just the newline after the
	// start sentinel that the regex consumed).
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}

	// Defensive: strip wrapper lines that may have leaked through PTY echo.
	// These are only stripped when they contain the rbsh token, preventing
	// false positives from legitimate output that happens to match the
	// command text.
	for len(lines) > 0 {
		first := strings.TrimSpace(lines[0])
		if strings.Contains(first, token) {
			if strings.HasPrefix(first, "__rbsh_cmd=") ||
				strings.HasPrefix(first, "__rbsh_opts=") ||
				first == strings.TrimSpace(command) {
				lines = lines[1:]
				continue
			}
		}
		break
	}

	// Strip trailing blank lines.
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}

	return strings.Join(lines, "\n")
}

func (s *PersistentShell) cleanPartialOutput(raw []byte, command, token string) string {
	if len(raw) == 0 {
		return ""
	}

	startSentinel := fmt.Sprintf("%s:%s__", startPrefix, token)
	startBytes := []byte(startSentinel)

	// Strip the start sentinel line.
	if idx := bytes.Index(raw, startBytes); idx >= 0 {
		if lineEnd := bytes.IndexByte(raw[idx:], '\n'); lineEnd >= 0 {
			raw = raw[idx+lineEnd+1:]
		} else {
			raw = raw[idx+len(startSentinel):]
		}
	}

	// Strip the done sentinel line and everything after it (prompt, etc.).
	// This handles the race where the done line arrived in buf but the
	// context deadline fired before the select could pick up the result.
	doneSentinel := fmt.Sprintf("%s:%s:", donePrefix, token)
	if idx := bytes.Index(raw, []byte(doneSentinel)); idx >= 0 {
		raw = raw[:idx]
		// Trim trailing \r\n or \n.
		raw = bytes.TrimRight(raw, "\r\n")
	}

	return s.cleanOutput(raw, command, token)
}

// ── Helpers ───────────────────────────────────────────────────────────

func bashQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func randomToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
