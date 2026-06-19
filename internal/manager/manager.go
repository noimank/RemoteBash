package manager

import (
	"database/sql"
	"fmt"
	"log/slog"
	"sync"

	"remotebash/internal/database"
	"remotebash/internal/models"
	"remotebash/internal/ssh"

	gossh "golang.org/x/crypto/ssh"
)

// ConnectionManager is a SQLite-backed registry of SSH sessions.
type ConnectionManager struct {
	db        *sql.DB
	sessions  map[string]*ssh.RemoteSession
	terminals map[string]*ssh.PersistentShell
	mu        sync.RWMutex
}

// New creates a new ConnectionManager.
func New(db *sql.DB) *ConnectionManager {
	return &ConnectionManager{
		db:        db,
		sessions:  make(map[string]*ssh.RemoteSession),
		terminals: make(map[string]*ssh.PersistentShell),
	}
}

// ── Lifecycle ─────────────────────────────────────────────────────────

// Load restores persisted clients from the database.
func (m *ConnectionManager) Load() error {
	clients, err := database.LoadClients(m.db)
	if err != nil {
		return fmt.Errorf("load clients: %w", err)
	}

	for _, c := range clients {
		s := ssh.NewRemoteSession(c.Name, c.Host, c.User, c.Password,
			c.Port, c.Enabled, c.SafeRm, c.Via)
		s.SetAuditCallback(m.onAudit)
		s.SetTunnelResolver(m.resolveTunnel)
		m.sessions[c.Name] = s
	}
	slog.Info("已加载客户端", "count", len(clients))
	return nil
}

// WarmUp asynchronously connects all enabled clients and starts their shells.
// This populates shellType (via detectShellType) and cwd on each session so
// list_remote_clients returns complete host metadata without waiting for a
// first command. Failed hosts are logged and skipped — the lazy-connect path
// in Exec() still retries on next use.
func (m *ConnectionManager) WarmUp() {
	m.mu.RLock()
	sessions := make([]*ssh.RemoteSession, 0)
	for _, s := range m.sessions {
		if s.Enabled {
			sessions = append(sessions, s)
		}
	}
	m.mu.RUnlock()

	if len(sessions) == 0 {
		return
	}

	slog.Info("开始异步预热客户端", "count", len(sessions))
	for _, s := range sessions {
		go func(sess *ssh.RemoteSession) {
			if err := sess.Connect(); err != nil {
				slog.Warn("预热连接失败", "client", sess.Name, "err", err)
				return
			}
			if _, err := sess.EnsureShell(); err != nil {
				slog.Warn("预热shell失败", "client", sess.Name, "err", err)
				return
			}
			slog.Info("客户端预热完成", "client", sess.Name, "shell_type", sess.ToInfo().ShellType)
		}(s)
	}
}

// Close disconnects all sessions and terminal shells.
// Acquires execLock on each session to avoid racing in-flight Exec calls.
func (m *ConnectionManager) Close() {
	m.mu.Lock()
	// Snapshot sessions under mu, then release to avoid holding mu
	// while blocking on execLock (Exec holds execLock then calls
	// m.mu.RLock via Get → deadlock risk).
	sessions := make([]*ssh.RemoteSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	for name, shell := range m.terminals {
		shell.Close()
		delete(m.terminals, name)
	}
	m.mu.Unlock()

	for _, s := range sessions {
		s.ExecLock() // wait for in-flight Exec to finish
		s.Disconnect()
		s.ExecUnlock()
	}
	m.sessions = make(map[string]*ssh.RemoteSession)
}

// ── Audit ─────────────────────────────────────────────────────────────

func (m *ConnectionManager) onAudit(clientName, command string, result *ssh.CommandOutput) {
	if err := database.InsertAudit(m.db, clientName, command, result.Output,
		result.ExitCode, result.Cwd, result.DurationMs,
		result.ExitCode == 0); err != nil {
		slog.Warn("审计记录写入失败", "err", err)
	}
}

// LogAudit writes a generic audit record (used for SFTP transfers).
func (m *ConnectionManager) LogAudit(clientName, command, output string,
	exitCode int, cwd string, durationMs int, success bool) {
	if err := database.InsertAudit(m.db, clientName, command, output,
		exitCode, cwd, durationMs, success); err != nil {
		slog.Warn("审计记录写入失败", "err", err)
	}
}

// ── Clients ───────────────────────────────────────────────────────────

// Add creates and persists a new client. Returns the new client info.
func (m *ConnectionManager) Add(name, host, user, password, via string, port int, enabled, safeRm bool) (*models.ClientInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.sessions[name]; exists {
		return nil, fmt.Errorf("客户端 '%s' 已存在。", name)
	}

	if wouldCreateCycle(m.sessions, name, via) {
		return nil, fmt.Errorf("无法添加 '%s'：不能将 '%s' 设为跳板，这会产生循环引用。", name, via)
	}

	client := &models.Client{
		Name:     name,
		Host:     host,
		Port:     port,
		User:     user,
		Password: password,
		Enabled:  enabled,
		SafeRm:   safeRm,
		Via:      via,
	}
	if err := database.InsertClient(m.db, client); err != nil {
		return nil, fmt.Errorf("persist client: %w", err)
	}

	s := ssh.NewRemoteSession(name, host, user, password, port, enabled, safeRm, via)
	s.SetAuditCallback(m.onAudit)
	s.SetTunnelResolver(m.resolveTunnel)
	m.sessions[name] = s

	info := s.ToInfo()
	return &info, nil
}

// Remove deletes a client and its session.
func (m *ConnectionManager) Remove(name string) error {
	m.mu.Lock()

	_, exists := m.sessions[name]
	if !exists {
		m.mu.Unlock()
		return fmt.Errorf("客户端 '%s' 不存在。", name)
	}

	deps := m.dependents(name)
	if len(deps) > 0 {
		m.mu.Unlock()
		return fmt.Errorf(
			"无法删除 '%s'，以下客户端依赖它作为跳板: %s。请先删除或修改这些客户端。",
			name, joinNames(deps))
	}

	// Snapshot the session and release manager lock before acquiring execLock.
	// This avoids a deadlock: Remove holds m.mu, Exec holds execLock then calls
	// m.mu.RLock via Get() during audit callback (the resolver path is safe,
	// but defensive ordering is still correct).
	s := m.sessions[name]
	m.closeTerminalLocked(name)
	delete(m.sessions, name)
	m.mu.Unlock()

	// Wait for in-flight Exec() to finish, then tear down the connection.
	s.ExecLock()
	s.Disconnect()
	s.ExecUnlock()

	// Clean up audit log entries before deleting the client to avoid FK
	// constraint violations (audit_log.client_name REFERENCES clients(name)).
	if _, err := database.DeleteAuditByClient(m.db, name); err != nil {
		slog.Warn("删除客户端审计日志失败", "client", name, "err", err)
	}

	if err := database.DeleteClient(m.db, name); err != nil {
		return fmt.Errorf("delete client from db: %w", err)
	}
	return nil
}

// Update modifies an existing client's fields. Returns the updated info.
func (m *ConnectionManager) Update(name string, update models.ClientUpdate) (*models.ClientInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, exists := m.sessions[name]
	if !exists {
		return nil, fmt.Errorf("客户端 '%s' 不存在。", name)
	}

	fields := make(map[string]any)

	if update.Host != nil {
		fields["host"] = *update.Host
		s.Host = *update.Host
	}
	if update.Port != nil {
		fields["port"] = *update.Port
		s.Port = *update.Port
	}
	if update.User != nil {
		fields["user"] = *update.User
		s.User = *update.User
	}
	if update.Password != nil {
		fields["password"] = *update.Password
		s.Password = *update.Password
	}
	if update.Enabled != nil {
		fields["enabled"] = *update.Enabled
		s.Enabled = *update.Enabled
	}
	if update.SafeRm != nil {
		fields["safe_rm"] = *update.SafeRm
		s.SafeRm = *update.SafeRm
	}
	if update.Via != nil {
		if wouldCreateCycle(m.sessions, name, *update.Via) {
			return nil, fmt.Errorf("无法更新 '%s'：不能将 '%s' 设为跳板，这会产生循环引用。", name, *update.Via)
		}
		fields["via"] = *update.Via
		s.Via = *update.Via
	}

	if len(fields) > 0 {
		if err := database.UpdateClient(m.db, name, fields); err != nil {
			return nil, fmt.Errorf("update client in db: %w", err)
		}
	}

	info := s.ToInfo()
	return &info, nil
}

// Get returns a session by name.
func (m *ConnectionManager) Get(name string) (*ssh.RemoteSession, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	s, exists := m.sessions[name]
	if !exists {
		enabled := m.listEnabledLocked()
		hint := ""
		if len(enabled) > 0 {
			names := make([]string, len(enabled))
			for i, c := range enabled {
				names[i] = c.Name
			}
			hint = fmt.Sprintf(" 已启用的客户端: %s。", joinNames(names))
		}
		return nil, fmt.Errorf("客户端 '%s' 不存在。%s", name, hint)
	}
	return s, nil
}

// ── Browser terminals ─────────────────────────────────────────────────

// GetOrCreateTerminal returns a live PersistentShell for the browser terminal.
func (m *ConnectionManager) GetOrCreateTerminal(name string) (*ssh.PersistentShell, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, exists := m.sessions[name]
	if !exists {
		return nil, fmt.Errorf("客户端 '%s' 不存在。", name)
	}

	shell := m.terminals[name]
	if shell != nil {
		if !shell.Alive() || shell.SafeRmFlag() != s.SafeRm {
			shell.Close()
			shell = nil
		}
	}

	if shell == nil {
		var err error
		shell, err = s.OpenTerminalShell()
		if err != nil {
			return nil, err
		}
		m.terminals[name] = shell
	}
	return shell, nil
}

// CloseTerminal tears down a browser terminal shell.
func (m *ConnectionManager) CloseTerminal(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closeTerminalLocked(name)
}

func (m *ConnectionManager) closeTerminalLocked(name string) {
	if shell, ok := m.terminals[name]; ok {
		shell.Close()
		delete(m.terminals, name)
	}
}

// ListAll returns all clients.
func (m *ConnectionManager) ListAll() []models.ClientInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]models.ClientInfo, 0, len(m.sessions))
	for _, s := range m.sessions {
		result = append(result, s.ToInfo())
	}
	return result
}

// ListEnabled returns only enabled clients.
func (m *ConnectionManager) ListEnabled() []models.ClientInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.listEnabledLocked()
}

func (m *ConnectionManager) listEnabledLocked() []models.ClientInfo {
	result := make([]models.ClientInfo, 0)
	for _, s := range m.sessions {
		if s.Enabled {
			result = append(result, s.ToInfo())
		}
	}
	return result
}

// ── Internal ──────────────────────────────────────────────────────────

func (m *ConnectionManager) resolveTunnel(name string) (*gossh.Client, error) {
	m.mu.RLock()
	s, exists := m.sessions[name]
	m.mu.RUnlock()
	if !exists {
		return nil, fmt.Errorf("jump host '%s' not found", name)
	}

	if !s.Connected() {
		if err := s.Connect(); err != nil {
			return nil, fmt.Errorf("connect jump host '%s': %w", name, err)
		}
	}
	return s.RawConn(), nil
}

func (m *ConnectionManager) dependents(name string) []string {
	var deps []string
	for n, s := range m.sessions {
		if s.Via == name {
			deps = append(deps, n)
		}
	}
	return deps
}

func wouldCreateCycle(sessions map[string]*ssh.RemoteSession, name, via string) bool {
	if via == "" {
		return false
	}
	visited := map[string]bool{name: true}
	cur := via
	for cur != "" {
		if visited[cur] {
			return true
		}
		visited[cur] = true
		s, ok := sessions[cur]
		if !ok || s.Via == "" {
			return false
		}
		cur = s.Via
	}
	return false
}

func joinNames(names []string) string {
	result := ""
	for i, n := range names {
		if i > 0 {
			result += ", "
		}
		result += n
	}
	return result
}

// ── Audit queries ─────────────────────────────────────────────────────

// AuditList returns paginated audit entries.
func (m *ConnectionManager) AuditList(clientName *string, after, before *string,
	limit, offset int) (*models.AuditListResponse, error) {

	entries, err := database.QueryAudit(m.db, clientName, after, before, limit, offset)
	if err != nil {
		return nil, err
	}
	total, err := database.CountAudit(m.db, clientName, after, before)
	if err != nil {
		return nil, err
	}
	return &models.AuditListResponse{
		Entries: entries,
		Total:   total,
		Limit:   limit,
		Offset:  offset,
	}, nil
}

// AuditDelete deletes audit entries by the various criteria.
func (m *ConnectionManager) AuditDelete(entryID *int, entryIDs []int,
	clientName *string, beforeID *int) (int64, error) {

	if entryID != nil {
		return database.DeleteAuditByID(m.db, *entryID)
	}
	if len(entryIDs) > 0 {
		return database.DeleteAuditByIDs(m.db, entryIDs)
	}
	if clientName != nil && *clientName != "" {
		return database.DeleteAuditByClient(m.db, *clientName)
	}
	if beforeID != nil {
		return database.DeleteAuditBeforeID(m.db, *beforeID)
	}
	return 0, nil
}
