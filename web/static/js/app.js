/**
 * RemoteBash 连接管理 — 前端逻辑
 */

const CLIENTS_API = "/api/clients";

// ---------------------------------------------------------------------------
// 提示
// ---------------------------------------------------------------------------

function toast(msg, isErr) {
  const el = document.getElementById("toast");
  el.textContent = msg;
  el.className =
    el.className.replace(/ (opacity-0|translate-y-2|border-red)$/g, "") +
    (isErr ? " border-red" : "") +
    " fixed top-5 right-5 z-50 bg-surface-overlay border border-border rounded-xl px-5 py-3 text-sm shadow-2xl opacity-100 translate-y-0 transition-all duration-300 pointer-events-auto";
  setTimeout(() => {
    el.className = el.className
      .replace("opacity-100", "opacity-0")
      .replace("translate-y-0", "translate-y-2")
      .replace("pointer-events-auto", "pointer-events-none");
  }, 2800);
}

// ---------------------------------------------------------------------------
// 复制
// ---------------------------------------------------------------------------

async function copyToClipboard(text) {
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text);
      return true;
    }
    // 非安全上下文（如局域网 IP 访问）回退到 execCommand
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.select();
    const ok = document.execCommand("copy");
    document.body.removeChild(ta);
    return ok;
  } catch (_) {
    return false;
  }
}

async function copyName(name) {
  const ok = await copyToClipboard(name);
  toast(ok ? "已复制: " + name : "复制失败", !ok);
}

// ---------------------------------------------------------------------------
// API 封装
// ---------------------------------------------------------------------------

async function api(method, path, body) {
  const res = await fetch(path, {
    method,
    headers: body ? { "Content-Type": "application/json" } : {},
    body: body ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) {
    const t = await res.text();
    throw new Error(t || res.statusText);
  }
  return res.json();
}

// ---------------------------------------------------------------------------
// 客户端列表渲染
// ---------------------------------------------------------------------------

function populateViaDropdowns(clients) {
  const selects = [document.getElementById("via"), document.getElementById("editVia")];
  selects.forEach(sel => {
    if (!sel) return;
    const current = sel.value;
    sel.innerHTML = '<option value="">(直连)</option>';
    clients.forEach(c => {
      const opt = document.createElement("option");
      opt.value = c.name;
      opt.textContent = c.name;
      sel.appendChild(opt);
    });
    sel.value = current || "";
  });
}

function statusDot(connected, enabled) {
  const color = !enabled ? "bg-yellow" : connected ? "bg-green" : "bg-red";
  const label = !enabled ? "已禁用" : connected ? "已连接" : "未连接";
  return `<span class="relative flex h-2.5 w-2.5" title="${label}">
    <span class="animate-ping absolute inline-flex h-full w-full rounded-full ${color} opacity-30"></span>
    <span class="relative inline-flex rounded-full h-2.5 w-2.5 ${color}"></span>
  </span>`;
}

function renderClient(c) {
  const js = (s) => String(s).replace(/\\/g, "\\\\").replace(/'/g, "\\'");
  return `
    <div class="flex items-center gap-4 py-3.5 px-2 border-b border-border last:border-b-0 flex-wrap hover:bg-surface/50 rounded-lg transition-colors -mx-2">
      ${statusDot(c.connected, c.enabled)}
      <code onclick="copyName('${js(c.name)}')" class="text-accent text-[13px] font-mono shrink-0 cursor-pointer hover:underline whitespace-nowrap" title="点击复制名称">${c.name}</code>
      <span class="font-medium text-sm min-w-[140px]">${c.host}:${c.port}</span>
      <span class="text-muted text-xs">${c.user}</span>
      <span class="text-muted text-xs max-w-[200px] truncate" title="${c.cwd || "~"}">${c.cwd || "~"}</span>
      ${c.safe_rm
        ? '<span class="text-[10px] uppercase tracking-wider bg-green/10 text-green border border-green/30 rounded px-1.5 py-0.5 font-medium" title="rm → mv /tmp">安全删除</span>'
        : ''}
      ${c.via
        ? '<span class="text-[10px] tracking-wider bg-accent/10 text-accent border border-accent/30 rounded px-1.5 py-0.5 font-medium" title="通过 ' + js(c.via) + ' 透传">⎇ ' + js(c.via) + '</span>'
        : ''}
      <span class="ml-auto flex gap-2">
        ${c.enabled ? `<button onclick="openTerminal('${js(c.name)}')" class="rounded-lg bg-accent/10 border border-accent/40 hover:bg-accent/20 text-accent text-xs px-3 py-1.5 transition-colors font-medium" title="打开浏览器终端">终端</button>` : ''}
        ${c.enabled ? `<button onclick="testConnect('${c.name}')" class="rounded-lg border border-border hover:border-accent text-muted hover:text-white text-xs px-2.5 py-1.5 transition-colors font-medium" title="测试连接">测试</button>` : ''}
        <button onclick="editClient('${js(c.name)}','${js(c.host)}','${js(c.port)}','${js(c.user)}','','${js(c.via || '')}')" class="rounded-lg border border-border hover:border-accent text-muted hover:text-white text-xs px-3 py-1.5 transition-colors font-medium" title="编辑连接信息">编辑</button>
        ${c.enabled
          ? `<button onclick="disableClient('${js(c.name)}')" class="rounded-lg border border-yellow/30 hover:bg-yellow/10 text-yellow text-xs px-3 py-1.5 transition-colors font-medium">禁用</button>`
          : `<button onclick="enableClient('${js(c.name)}')" class="rounded-lg border border-green/30 hover:bg-green/10 text-green text-xs px-3 py-1.5 transition-colors font-medium">启用</button>`}
        ${c.safe_rm
          ? `<button onclick="toggleSafeRm('${js(c.name)}',false)" class="rounded-lg border border-yellow/30 hover:bg-yellow/10 text-yellow text-xs px-3 py-1.5 transition-colors font-medium" title="关闭安全删除">🔰</button>`
          : `<button onclick="toggleSafeRm('${js(c.name)}',true)"  class="rounded-lg border border-border hover:border-accent text-muted hover:text-white text-xs px-3 py-1.5 transition-colors font-medium" title="开启安全删除 (rm→mv /tmp)">🔰</button>`}
        ${c.connected
          ? `<button onclick="disconnect('${js(c.name)}')" class="rounded-lg border border-red/30 hover:bg-red/10 text-red text-xs px-3 py-1.5 transition-colors font-medium">断开</button>`
          : `<button onclick="connect('${js(c.name)}')" class="rounded-lg bg-green/10 border border-green/30 hover:bg-green/20 text-green text-xs px-3 py-1.5 transition-colors font-medium">连接</button>`}
        <button onclick="removeClient('${js(c.name)}')" class="rounded-lg border border-border hover:border-red/40 hover:text-red text-muted text-xs px-3 py-1.5 transition-colors font-medium">&times;</button>
      </span>
    </div>`;
}

function render(clients) {
  const list = document.getElementById("list");
  const count = document.getElementById("count");
  count.textContent = clients.length ? "(" + clients.length + ")" : "";
  if (!clients.length) {
    list.innerHTML = '<div class="text-center py-12 text-muted text-sm">暂无连接，请先添加。</div>';
    return;
  }
  list.innerHTML = clients.map(renderClient).join("");
}

// ---------------------------------------------------------------------------
// 数据获取
// ---------------------------------------------------------------------------

async function refresh() {
  try { const clients = await api("GET", CLIENTS_API); render(clients); populateViaDropdowns(clients); } catch (e) { toast(e.message, true); }
}

// ---------------------------------------------------------------------------
// 事件处理
// ---------------------------------------------------------------------------

document.getElementById("addForm").onsubmit = async (e) => {
  e.preventDefault();
  const fd = new FormData(e.target);
  const body = {
    name: fd.get("name"),
    host: fd.get("host"),
    port: +fd.get("port") || 22,
    user: fd.get("user"),
    password: fd.get("password"),
    via: fd.get("via") || null,
  };
  try {
    await api("POST", CLIENTS_API, body);
    e.target.reset();
    toast("已添加并连接");
  } catch (err) {
    // 客户端可能已入库，只是自动连接失败 — 尝试解析后端返回的 JSON
    let msg = err.message;
    let added = false;
    try {
      const data = JSON.parse(err.message);
      if (data.name) { added = true; msg = data.error || msg; }
    } catch (_) { /* 非 JSON 响应，按普通错误处理 */ }
    // 无论是否入库，这里都是失败信息，按错误样式提示；但已入库时仍需刷新列表
    toast(msg, true);
    if (!added) return; // 真正的入库失败（409 等），不重置表单也不刷新
  }
  refresh();
};

async function connect(name)    { try { await api("POST", CLIENTS_API + "/" + name + "/connect");    toast("已连接");  refresh(); } catch (e) { toast(e.message, true); } }
async function disconnect(name) { try { await api("POST", CLIENTS_API + "/" + name + "/disconnect"); toast("已断开");  refresh(); } catch (e) { toast(e.message, true); } }

async function testConnect(name) {
  toast("测试中…");
  try {
    const data = await api("POST", CLIENTS_API + "/" + name + "/test");
    toast("连接正常 — " + data.user + "@" + data.host + ":" + data.port);
  } catch (e) { toast(e.message, true); }
}

async function disableClient(name) {
  try { await api("PATCH", CLIENTS_API + "/" + name, { enabled: false }); toast("已禁用: " + name); refresh(); } catch (e) { toast(e.message, true); }
}

async function enableClient(name) {
  try { await api("PATCH", CLIENTS_API + "/" + name, { enabled: true });  toast("已启用: " + name); refresh(); } catch (e) { toast(e.message, true); }
}

async function toggleSafeRm(name, enable) {
  try {
    await api("PATCH", CLIENTS_API + "/" + name, { safe_rm: enable });
    toast(enable ? "已开启安全删除: " + name : "已关闭安全删除: " + name);
    refresh();
  } catch (e) { toast(e.message, true); }
}

async function removeClient(name) {
  if (!confirm("确定删除 " + name + "？")) return;
  try { await api("DELETE", CLIENTS_API + "/" + name); toast("已删除"); refresh(); } catch (e) { toast(e.message, true); }
}

// ---------------------------------------------------------------------------
// 编辑弹窗
// ---------------------------------------------------------------------------

function editClient(name, host, port, user, password, via) {
  document.getElementById("editName").value = name;
  document.getElementById("editHost").value = host;
  document.getElementById("editPort").value = port;
  document.getElementById("editUser").value = user;
  document.getElementById("editPassword").value = password;
  const viaEl = document.getElementById("editVia");
  if (viaEl) viaEl.value = via || "";
  showModal();
}

function showModal() {
  const mask = document.getElementById("editModal");
  const inner = document.getElementById("editModalInner");
  mask.classList.remove("opacity-0", "pointer-events-none");
  mask.classList.add("opacity-100", "pointer-events-auto");
  inner.classList.remove("scale-95");
  inner.classList.add("scale-100");
}

function closeEdit() {
  const mask = document.getElementById("editModal");
  const inner = document.getElementById("editModalInner");
  mask.classList.add("opacity-0", "pointer-events-none");
  mask.classList.remove("opacity-100", "pointer-events-auto");
  inner.classList.add("scale-95");
  inner.classList.remove("scale-100");
}

document.getElementById("editModal").addEventListener("click", function(e) {
  if (e.target === this) closeEdit();
});

document.getElementById("editForm").onsubmit = async function(e) {
  e.preventDefault();
  const fd = new FormData(e.target);
  const name = document.getElementById("editName").value;
  const body = {
    host: fd.get("host").trim(),
    port: parseInt(fd.get("port")) || 22,
    user: fd.get("user").trim(),
    via: fd.get("via") || null,
  };
  const pwd = fd.get("password").trim();
  if (pwd) body.password = pwd;
  try {
    await api("PATCH", CLIENTS_API + "/" + name, body);
    closeEdit();
    toast("已更新: " + name);
    refresh();
  } catch (err) { toast(err.message, true); }
};

// ---------------------------------------------------------------------------
// 初始化
// ---------------------------------------------------------------------------

refresh();
