package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"remotebash/internal/manager"

	"nhooyr.io/websocket"
)

// Max queued WebSocket frames before backpressure drops incoming PTY chunks.
const sendQueueMax = 256

// TerminalHandler manages the WebSocket terminal endpoint.
type TerminalHandler struct {
	Mgr *manager.ConnectionManager
}

// ServeHTTP handles the WebSocket upgrade and PTY bridge for xterm.js.
func (h *TerminalHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	name := req.PathValue("name")

	sess, err := h.Mgr.Get(name)
	if err != nil {
		http.Error(w, "客户端不存在", http.StatusNotFound)
		return
	}
	if !sess.Enabled {
		http.Error(w, "客户端已禁用", http.StatusForbidden)
		return
	}

	conn, err := websocket.Accept(w, req, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		slog.Warn("WebSocket 升级失败", "err", err)
		return
	}

	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()

	// All writes (Write, Ping, Close) must be serialized —
	// nhooyr.io/websocket requires exclusive write access.
	var writeMu sync.Mutex

	sendStatus(ctx, conn, &writeMu, "connecting")

	shell, err := h.Mgr.GetOrCreateTerminal(name)
	if err != nil {
		slog.Warn("终端 shell 创建失败", "client", name, "err", err)
		sendStatus(ctx, conn, &writeMu, "failed")
		conn.Close(websocket.StatusInternalError, "终端启动失败: "+err.Error())
		return
	}

	sendStatus(ctx, conn, &writeMu, "ready")

	// WebSocket ping/pong keepalive — proxies typically drop idle connections
	// after 60 seconds. Pinging every 30 seconds keeps the tunnel alive.
	// Writes (Ping, Write) must be serialized — nhooyr.io/websocket
	// requires exclusive write access to the connection.
	pingDone := make(chan struct{})
	go func() {
		defer close(pingDone)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				writeMu.Lock()
				err := conn.Ping(ctx)
				writeMu.Unlock()
				if err != nil {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// Output pump: tap callback → channel → WebSocket binary frames.
	sendQueue := make(chan []byte, sendQueueMax)

	detach := shell.AttachTap(func(chunk []byte) {
		select {
		case sendQueue <- chunk:
		default:
			// Drop under backpressure; keep shell alive.
		}
	})

	shell.ClearBuffer()
	shell.FeedRaw([]byte("\n")) // trigger fresh prompt

	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		for {
			select {
			case chunk := <-sendQueue:
				writeMu.Lock()
				err := conn.Write(ctx, websocket.MessageBinary, chunk)
				writeMu.Unlock()
				if err != nil {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// Read loop: forward binary frames to PTY, handle resize messages.
	var closeStatus websocket.StatusCode = websocket.StatusNormalClosure
	var closeReason string
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			break
		}

		switch typ {
		case websocket.MessageBinary:
			if err := shell.FeedRaw(data); err != nil {
				slog.Warn("PTY 写入失败", "client", name, "err", err)
				closeStatus = websocket.StatusInternalError
				closeReason = "stdin write failed"
				goto done
			}

		case websocket.MessageText:
			var msg struct {
				Type string `json:"type"`
				Cols int    `json:"cols"`
				Rows int    `json:"rows"`
			}
			if err := json.Unmarshal(data, &msg); err != nil {
				continue
			}
			if msg.Type == "resize" && msg.Cols > 0 && msg.Rows > 0 {
				if err := shell.Resize(msg.Cols, msg.Rows); err != nil {
					slog.Warn("PTY 调整大小失败", "client", name, "err", err)
				}
			}
		}
	}

done:
	detach()
	cancel()
	<-pumpDone
	<-pingDone

	conn.Close(closeStatus, closeReason)
	slog.Info("终端 WebSocket 已关闭", "client", name, "status", closeStatus)
}

func sendStatus(ctx context.Context, conn *websocket.Conn, mu *sync.Mutex, state string) {
	msg, _ := json.Marshal(map[string]string{"type": "status", "state": state})
	mu.Lock()
	err := conn.Write(ctx, websocket.MessageText, msg)
	mu.Unlock()
	if err != nil {
		slog.Debug("发送状态失败", "state", state, "err", err)
	}
}
