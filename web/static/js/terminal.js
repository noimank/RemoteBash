/**
 * RemoteBash 浏览器终端 — 基于 xterm.js + WebSocket
 *
 * 一个全局 TerminalSession 管理器：同一时刻只打开一个终端弹窗，
 * 打开新终端前会先关闭旧的（释放 WebSocket）。终端 shell 状态在
 * 服务端持久，关闭弹窗再打开仍能继续之前的会话。
 */

/** @type {TerminalSession|null} */
let _activeTerm = null;

const TERM_CONNECT_TIMEOUT = 15000;  // 15 s — matched to server-side shell start
const TERM_RECONNECT_DELAY = 2000;   // retry after this many ms on transient errors

class TerminalSession {
  constructor(clientName, mountEl) {
    this.clientName = clientName;
    this.mountEl = mountEl;
    this.term = null;
    this.fitAddon = null;
    this.ws = null;
    this.closed = false;
    this._connectTimer = null;
    this._connectAttempt = 0;
  }

  async open() {
    const term = new Terminal({
      cursorBlink: true,
      fontSize: 13,
      fontFamily: "Menlo, Consolas, 'Courier New', monospace",
      theme: {
        background: "#0f1117",
        foreground: "#e2e8f0",
        cursor: "#58a6ff",
        selectionBackground: "#264f78aa",
      },
      allowProposedApi: true,
    });
    this.fitAddon = new FitAddon.FitAddon();
    term.loadAddon(this.fitAddon);
    term.open(this.mountEl);
    requestAnimationFrame(() => { try { this.fitAddon.fit(); } catch (_) {} });
    this.term = term;

    term.onData((data) => {
      if (this.ws && this.ws.readyState === WebSocket.OPEN) {
        this.ws.send(new TextEncoder().encode(data));
      }
    });
    term.onResize(({ cols, rows }) => this._sendResize(cols, rows));

    this._connect();
    term.focus();
  }

  _wsUrl() {
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    const root = window.BASE_URL_PREFIX || "";
    return `${proto}//${location.host}${root}/api/clients/${encodeURIComponent(this.clientName)}/terminal`;
  }

  _connect() {
    this._connectAttempt++;
    this._showStatus("\x1b[2m正在连接…\x1b[0m");

    const ws = new WebSocket(this._wsUrl());
    ws.binaryType = "arraybuffer";
    this.ws = ws;

    // Connection timeout — if the server doesn't respond within the limit,
    // close the socket and let the user retry.
    this._connectTimer = setTimeout(() => {
      if (this.closed) return;
      if (ws.readyState !== WebSocket.OPEN) {
        ws.close();
        this._showStatus(
          `\x1b[31m连接超时（${TERM_CONNECT_TIMEOUT / 1000}s）。请关闭弹窗后重试，或检查客户端是否在线。\x1b[0m`
        );
      }
    }, TERM_CONNECT_TIMEOUT);

    ws.onopen = () => {
      // WebSocket transport is up; the server will send {"type":"status",...}
      // once the shell is actually ready.  We report the current terminal size
      // now so the PTY is the right shape.
      this._sendResize(this.term.cols, this.term.rows);
    };

    ws.onmessage = (ev) => {
      // Binary frame → raw PTY bytes, rendered by xterm.js.
      if (ev.data instanceof ArrayBuffer) {
        this.term.write(new Uint8Array(ev.data));
        return;
      }
      // Text frame → JSON control/status message.
      try {
        const msg = JSON.parse(ev.data);
        if (msg.type === "status") {
          if (this._connectTimer) { clearTimeout(this._connectTimer); this._connectTimer = null; }
          if (msg.state === "connecting") {
            this._showStatus("\x1b[2m正在启动远程 shell…\x1b[0m");
          } else if (msg.state === "ready") {
            this.term.clear();
            this._showStatus("\x1b[32m✓ 已连接\x1b[0m");
          } else if (msg.state === "timeout") {
            this._showStatus("\x1b[31m远程 shell 启动超时，请重试。\x1b[0m");
          }
        }
      } catch (_) { /* ignore malformed JSON */ }
    };

    ws.onerror = () => {
      // onclose will fire next; we just suppress the browser's default error.
    };

    ws.onclose = (ev) => {
      if (this._connectTimer) { clearTimeout(this._connectTimer); this._connectTimer = null; }
      if (this.closed) return;
      if (ev.code === 4404) {
        this.term.writeln(`\x1b[31m客户端「${this.clientName}」不存在。\x1b[0m`);
      } else if (ev.code === 4403) {
        this.term.writeln(`\x1b[33m客户端「${this.clientName}」已禁用。\x1b[0m`);
      } else if (ev.code === 4500) {
        this.term.writeln(`\x1b[31m终端启动失败：${ev.reason || "未知错误"}\x1b[0m`);
      } else {
        this.term.writeln("\x1b[33m连接已断开（服务端保留会话，可关闭弹窗后重新打开继续）。\x1b[0m");
      }
    };
  }

  _showStatus(text) {
    if (!this.term) return;
    // Clear the last status line(s) and write the new one.  We use a simple
    // approach: move to column 0 of the current row and write the message.
    // On the very first call the terminal is empty, so writeln is fine.
    this.term.write("\r\x1b[2K" + text + "\r\n");
  }

  _sendResize(cols, rows) {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify({ type: "resize", cols, rows }));
    }
  }

  fit() {
    if (this.fitAddon) { try { this.fitAddon.fit(); } catch (_) {} }
  }

  close() {
    this.closed = true;
    if (this._connectTimer) { clearTimeout(this._connectTimer); this._connectTimer = null; }
    if (this.ws) {
      try { this.ws.close(); } catch (_) {}
      this.ws = null;
    }
    if (this.term) {
      try { this.term.dispose(); } catch (_) {}
      this.term = null;
    }
  }
}

// ─── 弹窗控制 ─────────────────────────────────────────────────────

function openTerminal(clientName) {
  const mask = document.getElementById("termModal");
  const mount = document.getElementById("termMount");
  const title = document.getElementById("termTitle");

  // 先关掉可能存在的旧会话
  if (_activeTerm) {
    _activeTerm.close();
    _activeTerm = null;
    mount.innerHTML = ""; // 清掉旧 xterm DOM
  }

  title.textContent = "终端 · " + clientName;
  mask.classList.remove("opacity-0", "pointer-events-none");
  mask.classList.add("opacity-100", "pointer-events-auto");

  const session = new TerminalSession(clientName, mount);
  _activeTerm = session;
  session.open().catch((e) => {
    console.error(e);
    mount.textContent = "终端初始化失败：" + e;
  });
}

function closeTerminal() {
  const mask = document.getElementById("termModal");
  mask.classList.add("opacity-0", "pointer-events-none");
  mask.classList.remove("opacity-100", "pointer-events-auto");
  if (_activeTerm) {
    _activeTerm.close();
    _activeTerm = null;
  }
  const mount = document.getElementById("termMount");
  if (mount) mount.innerHTML = "";
}

// 弹窗遮罩点击 / Esc 关闭；窗口缩放时 fit
document.addEventListener("DOMContentLoaded", () => {
  const mask = document.getElementById("termModal");
  if (mask) {
    mask.addEventListener("click", (e) => { if (e.target === mask) closeTerminal(); });
  }
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && _activeTerm) closeTerminal();
  });
  window.addEventListener("resize", () => {
    if (_activeTerm) _activeTerm.fit();
  });
});
