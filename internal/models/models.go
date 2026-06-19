package models

// Client represents a persisted SSH connection configuration.
type Client struct {
	Name      string `json:"name"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	User      string `json:"user"`
	Password  string `json:"password,omitempty"`
	Label     string `json:"label"`
	Enabled   bool   `json:"enabled"`
	SafeRm    bool   `json:"safe_rm"`
	Via       string `json:"via,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// ClientInfo is the public-facing representation returned by the API
// and the list_remote_clients MCP tool (password is never exposed).
type ClientInfo struct {
	Name      string `json:"name"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	User      string `json:"user"`
	Connected bool   `json:"connected"`
	Cwd       string `json:"cwd"`
	Enabled   bool   `json:"enabled"`
	SafeRm    bool   `json:"safe_rm"`
	Via       string `json:"via,omitempty"`
	ShellType string `json:"shell_type"`
}

// RemoteClientInfo is the MCP list_remote_clients response shape.
// Uses "client_name" as the key field so MCP clients see a descriptive name.
type RemoteClientInfo struct {
	ClientName string `json:"client_name"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	User       string `json:"user"`
	Cwd        string `json:"cwd"`
	SafeRm     bool   `json:"safe_rm"`
	ShellType  string `json:"shell_type"`
}

// ClientRequest is the JSON body for adding/updating a client.
type ClientRequest struct {
	Name    string `json:"name"`
	Host    string `json:"host"`
	User    string `json:"user"`
	Password string `json:"password"`
	Port    int    `json:"port"`
	Enabled *bool  `json:"enabled,omitempty"`
	SafeRm  *bool  `json:"safe_rm,omitempty"`
	Via     string `json:"via,omitempty"`
	// AutoConnect controls whether add also performs an immediate connect test.
	AutoConnect *bool `json:"auto_connect,omitempty"`
}

// ClientUpdate is the JSON body for PATCH /api/clients/{name}.
type ClientUpdate struct {
	Host     *string `json:"host,omitempty"`
	Port     *int    `json:"port,omitempty"`
	User     *string `json:"user,omitempty"`
	Password *string `json:"password,omitempty"`
	Enabled  *bool   `json:"enabled,omitempty"`
	SafeRm   *bool   `json:"safe_rm,omitempty"`
	Via      *string `json:"via,omitempty"`
}

// AuditEntry is a single command audit record.
type AuditEntry struct {
	ID         int    `json:"id"`
	ClientName string `json:"client_name"`
	Command    string `json:"command"`
	Output     string `json:"output"`
	ExitCode   int    `json:"exit_code"`
	Cwd        string `json:"cwd"`
	DurationMs int    `json:"duration_ms"`
	Success    bool   `json:"success"`
	CreatedAt  string `json:"created_at"`
}

// AuditListResponse is the paginated audit query result.
type AuditListResponse struct {
	Entries []AuditEntry `json:"entries"`
	Total   int          `json:"total"`
	Limit   int          `json:"limit"`
	Offset  int          `json:"offset"`
}

// AuditDeleteResponse is the response from audit deletion.
type AuditDeleteResponse struct {
	OK      bool `json:"ok"`
	Deleted int  `json:"deleted"`
}

// CommandResult is the output of a single command execution.
type CommandResult struct {
	Output     string `json:"output"`
	ExitCode   int    `json:"exit_code"`
	Cwd        string `json:"cwd"`
	DurationMs int    `json:"duration_ms"`
}

// TransferResult is the output of an SFTP file transfer.
type TransferResult struct {
	Success    bool   `json:"success"`
	Direction  string `json:"direction"`
	Src        string `json:"src"`
	Dst        string `json:"dst"`
	SizeBytes  int64  `json:"size_bytes"`
	DurationMs int    `json:"duration_ms"`
}

// MCPRemoteBashOutput is the MCP remote_shell tool return shape.
type MCPRemoteBashOutput struct {
	Output   string `json:"output"`
	ExitCode int    `json:"exit_code"`
	Cwd      string `json:"cwd"`
}

// MCPDataTransferOutput is the MCP data_transfer tool return shape.
type MCPDataTransferOutput struct {
	Success    bool   `json:"success"`
	Direction  string `json:"direction"`
	Src        string `json:"src"`
	Dst        string `json:"dst"`
	SizeBytes  int64  `json:"size_bytes"`
	DurationMs int    `json:"duration_ms"`
}

// WebSocketStatus is a JSON status message sent over the terminal websocket.
type WebSocketStatus struct {
	Type  string `json:"type"`
	State string `json:"state"`
}

// WebSocketResize is a JSON resize control message received from xterm.js.
type WebSocketResize struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

// ErrorResponse is a standard JSON error envelope.
type ErrorResponse struct {
	Error string `json:"error"`
}

// OKResponse is a simple success envelope.
type OKResponse struct {
	OK   bool   `json:"ok"`
	Name string `json:"name,omitempty"`
}
