package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"remotebash/internal/manager"
	"remotebash/internal/models"
)

// Routes holds the HTTP handlers and depends on the ConnectionManager.
type Routes struct {
	Mgr *manager.ConnectionManager
}

// Register adds all REST API routes to the given ServeMux.
func (r *Routes) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/clients", r.listClients)
	mux.HandleFunc("POST /api/clients", r.addClient)
	mux.HandleFunc("DELETE /api/clients/{name}", r.removeClient)
	mux.HandleFunc("PATCH /api/clients/{name}", r.updateClient)

	mux.HandleFunc("POST /api/clients/{name}/connect", r.connectClient)
	mux.HandleFunc("POST /api/clients/{name}/disconnect", r.disconnectClient)
	mux.HandleFunc("POST /api/clients/{name}/test", r.testClient)

	mux.HandleFunc("GET /api/audit", r.listAudit)
	mux.HandleFunc("DELETE /api/audit", r.deleteAudit)
}

// ═══════════════════════════════════════════════════════════════════════
// Clients
// ═══════════════════════════════════════════════════════════════════════

func (r *Routes) listClients(w http.ResponseWriter, req *http.Request) {
	writeJSON(w, http.StatusOK, r.Mgr.ListAll())
}

func (r *Routes) addClient(w http.ResponseWriter, req *http.Request) {
	// Limit request body size to 16KB.
	req.Body = http.MaxBytesReader(w, req.Body, 16<<10)

	var body models.ClientRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, models.ErrorResponse{Error: "invalid JSON"})
		return
	}

	name := strings.TrimSpace(body.Name)
	host := strings.TrimSpace(body.Host)
	user := strings.TrimSpace(body.User)
	password := body.Password

	if name == "" || host == "" || user == "" {
		writeJSON(w, http.StatusBadRequest, models.ErrorResponse{Error: "名称、主机和用户为必填项"})
		return
	}
	if !validName(name) {
		writeJSON(w, http.StatusBadRequest, models.ErrorResponse{Error: "名称不允许包含斜杠字符"})
		return
	}

	port := body.Port
	if port == 0 {
		port = 22
	}
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	safeRm := false
	if body.SafeRm != nil {
		safeRm = *body.SafeRm
	}
	via := body.Via
	autoConnect := true
	if body.AutoConnect != nil {
		autoConnect = *body.AutoConnect
	}

	info, err := r.Mgr.Add(name, host, user, password, via, port, enabled, safeRm)
	if err != nil {
		writeJSON(w, http.StatusConflict, models.ErrorResponse{Error: err.Error()})
		return
	}

	if autoConnect && enabled {
		s, getErr := r.Mgr.Get(name)
		if getErr != nil {
			writeJSON(w, http.StatusInternalServerError, models.ErrorResponse{Error: fmt.Sprintf("已添加但连接失败: %v", getErr)})
			return
		}
		if err := s.Connect(); err != nil {
			writeJSON(w, http.StatusInternalServerError, models.ErrorResponse{
				Name:  name,
				Error: fmt.Sprintf("已添加但连接失败: %v", err),
			})
			return
		}
		// Eagerly start the shell so shell_type is populated
		// for list_remote_clients / dashboard.
		if _, shellErr := s.EnsureShell(); shellErr != nil {
			slog.Warn("添加客户端时启动shell失败", "name", name, "err", shellErr)
		}
		// Refresh info after connect.
		updated := s.ToInfo()
		info = &updated
	}

	writeJSON(w, http.StatusCreated, info)
}

func (r *Routes) removeClient(w http.ResponseWriter, req *http.Request) {
	name := req.PathValue("name")
	if err := r.Mgr.Remove(name); err != nil {
		if strings.Contains(err.Error(), "不存在") {
			writeJSON(w, http.StatusNotFound, models.ErrorResponse{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusConflict, models.ErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, models.OKResponse{OK: true})
}

func (r *Routes) connectClient(w http.ResponseWriter, req *http.Request) {
	name := req.PathValue("name")
	s, err := r.Mgr.Get(name)
	if err != nil {
		writeJSON(w, http.StatusNotFound, models.ErrorResponse{Error: fmt.Sprintf("客户端 '%s' 不存在", name)})
		return
	}
	if err := s.Connect(); err != nil {
		writeJSON(w, http.StatusInternalServerError, models.ErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, models.OKResponse{OK: true, Name: name})
}

func (r *Routes) disconnectClient(w http.ResponseWriter, req *http.Request) {
	name := req.PathValue("name")
	s, err := r.Mgr.Get(name)
	if err != nil {
		writeJSON(w, http.StatusNotFound, models.ErrorResponse{Error: fmt.Sprintf("客户端 '%s' 不存在", name)})
		return
	}
	r.Mgr.CloseTerminal(name)
	s.Disconnect()
	writeJSON(w, http.StatusOK, models.OKResponse{OK: true, Name: name})
}

func (r *Routes) testClient(w http.ResponseWriter, req *http.Request) {
	name := req.PathValue("name")
	s, err := r.Mgr.Get(name)
	if err != nil {
		writeJSON(w, http.StatusNotFound, models.ErrorResponse{Error: fmt.Sprintf("客户端 '%s' 不存在", name)})
		return
	}

	err = s.TestConnection()
	if err == nil {
		if _, shellErr := s.EnsureShell(); shellErr != nil {
			slog.Warn("测试连接时启动shell失败", "name", name, "err", shellErr)
		}
		info := s.ToInfo()
		writeJSON(w, http.StatusOK, info)
		return
	}

	msg := err.Error()
	status := http.StatusInternalServerError

	switch {
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline"):
		status = http.StatusGatewayTimeout
		msg = fmt.Sprintf("连接超时 — 无法在有效时间内连接到 %s:%d，请检查网络或防火墙", s.Host, s.Port)
	case strings.Contains(msg, "unable to authenticate") || strings.Contains(msg, "no supported methods"):
		status = http.StatusUnauthorized
		msg = fmt.Sprintf("认证失败 — 用户名或密码错误 (%s@%s:%d)", s.User, s.Host, s.Port)
	case strings.Contains(msg, "password expired") || strings.Contains(msg, "change required"):
		status = http.StatusUnauthorized
		msg = fmt.Sprintf("认证失败 — 密码已过期，需要更改密码 (%s@%s:%d)", s.User, s.Host, s.Port)
	case strings.Contains(msg, "refused") || strings.Contains(msg, "Refused"):
		msg = fmt.Sprintf("连接被拒绝 — %s:%d 端口未开放或 SSH 服务未运行", s.Host, s.Port)
	case strings.Contains(msg, "no such host") || strings.Contains(msg, "no address") || strings.Contains(msg, "resolve"):
		msg = fmt.Sprintf("无法解析主机名 — %s，请检查主机地址是否正确", s.Host)
	case strings.Contains(msg, "no route") || strings.Contains(msg, "unreachable") || strings.Contains(msg, "network"):
		msg = fmt.Sprintf("网络不可达 — 无法访问 %s:%d", s.Host, s.Port)
	default:
		msg = fmt.Sprintf("连接失败 — %s:%d，%v", s.Host, s.Port, err)
	}

	writeJSON(w, status, models.ErrorResponse{Error: msg})
}

func (r *Routes) updateClient(w http.ResponseWriter, req *http.Request) {
	req.Body = http.MaxBytesReader(w, req.Body, 16<<10)

	name := req.PathValue("name")
	var body models.ClientUpdate
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, models.ErrorResponse{Error: "invalid JSON"})
		return
	}

	info, err := r.Mgr.Update(name, body)
	if err != nil {
		if strings.Contains(err.Error(), "不存在") {
			writeJSON(w, http.StatusNotFound, models.ErrorResponse{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusConflict, models.ErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// ═══════════════════════════════════════════════════════════════════════
// Audit
// ═══════════════════════════════════════════════════════════════════════

func (r *Routes) listAudit(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	clientName := q.Get("client_name")
	after := q.Get("after")
	before := q.Get("before")
	limit := intQuery(q, "limit", 200)
	if limit < 1 {
		limit = 1
	}
	if limit > 1000 {
		limit = 1000
	}
	offset := intQuery(q, "offset", 0)
	if offset < 0 {
		offset = 0
	}

	var cn *string
	if clientName != "" {
		cn = &clientName
	}
	var af, bf *string
	if after != "" {
		af = &after
	}
	if before != "" {
		bf = &before
	}

	resp, err := r.Mgr.AuditList(cn, af, bf, limit, offset)
	if err != nil {
		slog.Warn("审计日志查询失败", "err", err)
		writeJSON(w, http.StatusInternalServerError, models.ErrorResponse{Error: "查询审计日志失败"})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (r *Routes) deleteAudit(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()

	if idStr := q.Get("entry_id"); idStr != "" {
		id, err := strconv.Atoi(idStr)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, models.ErrorResponse{Error: "invalid entry_id"})
			return
		}
		deleted, _ := r.Mgr.AuditDelete(&id, nil, nil, nil)
		writeJSON(w, http.StatusOK, models.AuditDeleteResponse{OK: true, Deleted: int(deleted)})
		return
	}

	if idsStr := q.Get("entry_ids"); idsStr != "" {
		var ids []int
		for _, s := range strings.Split(idsStr, ",") {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			id, err := strconv.Atoi(s)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, models.ErrorResponse{Error: "invalid entry_ids"})
				return
			}
			ids = append(ids, id)
		}
		deleted, _ := r.Mgr.AuditDelete(nil, ids, nil, nil)
		writeJSON(w, http.StatusOK, models.AuditDeleteResponse{OK: true, Deleted: int(deleted)})
		return
	}

	if cn := q.Get("client_name"); cn != "" {
		deleted, _ := r.Mgr.AuditDelete(nil, nil, &cn, nil)
		writeJSON(w, http.StatusOK, models.AuditDeleteResponse{OK: true, Deleted: int(deleted)})
		return
	}

	if bf := q.Get("before_id"); bf != "" {
		id, err := strconv.Atoi(bf)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, models.ErrorResponse{Error: "invalid before_id"})
			return
		}
		deleted, _ := r.Mgr.AuditDelete(nil, nil, nil, &id)
		writeJSON(w, http.StatusOK, models.AuditDeleteResponse{OK: true, Deleted: int(deleted)})
		return
	}

	writeJSON(w, http.StatusBadRequest, models.ErrorResponse{Error: "请提供删除条件: entry_id, entry_ids, client_name, 或 before_id"})
}

// ═══════════════════════════════════════════════════════════════════════
// Helpers
// ═══════════════════════════════════════════════════════════════════════

// validName rejects names containing '/' which would break URL path routing.
func validName(s string) bool {
	for _, c := range s {
		if c == '/' {
			return false
		}
	}
	return true
}

func intQuery(q url.Values, key string, defaultVal int) int {
	vals := q[key]
	if len(vals) == 0 {
		return defaultVal
	}
	v, err := strconv.Atoi(vals[0])
	if err != nil {
		return defaultVal
	}
	return v
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
