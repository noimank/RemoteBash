/**
 * RemoteBash 审计日志 — 前端逻辑
 */

const CLIENTS_API = "/api/clients";
const AUDIT_API = "/api/audit";
const PAGE_SIZE = 50;

let currentPage = 0;
let totalEntries = 0;
let currentEntries = [];

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
// 客户端筛选下拉
// ---------------------------------------------------------------------------

async function loadClientFilter() {
  try {
    const clients = await api("GET", CLIENTS_API);
    const sel = document.getElementById("filterClient");
    sel.innerHTML = '<option value="">全部主机</option>';
    clients.forEach(c => {
      sel.innerHTML += `<option value="${c.name}">${c.name}</option>`;
    });
  } catch (e) { /* 忽略 */ }
}

// ---------------------------------------------------------------------------
// 审计条目渲染
// ---------------------------------------------------------------------------

function timeAgo(iso) {
  const d = new Date(iso + "Z");
  const now = new Date();
  const diff = Math.floor((now - d) / 1000);
  if (diff < 60) return diff + " 秒前";
  if (diff < 3600) return Math.floor(diff / 60) + " 分钟前";
  if (diff < 86400) return Math.floor(diff / 3600) + " 小时前";
  return d.toLocaleDateString("zh-CN");
}

function exitBadge(code) {
  if (code === 0) return '<span class="text-green text-xs font-mono">0</span>';
  return '<span class="text-red text-xs font-mono">' + code + '</span>';
}

function escapeHtml(s) {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}

function formatSize(bytes) {
  if (bytes < 1024) return bytes + "B";
  if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + "KB";
  return (bytes / (1024 * 1024)).toFixed(2) + "MB";
}

function openDetail(idx) {
  const entry = currentEntries[idx];
  if (!entry) return;
  const m = document.getElementById("detailModal");
  document.getElementById("mdClient").textContent = entry.client_name;
  document.getElementById("mdTime").textContent = timeAgo(entry.created_at) + " · " + new Date(entry.created_at + "Z").toLocaleString("zh-CN");
  document.getElementById("mdExit").innerHTML = exitBadge(entry.exit_code);
  document.getElementById("mdCmd").textContent = entry.command || "(空)";
  document.getElementById("mdCwd").textContent = entry.cwd || "~";
  document.getElementById("mdDuration").textContent = entry.duration_ms + "ms";
  const out = entry.stdout || "";
  const err = entry.stderr || "";
  document.getElementById("mdOut").textContent = out || "(空)";
  document.getElementById("mdOut").className = out ? "text-xs text-gray-300 bg-surface px-3 py-2.5 rounded-lg overflow-x-auto max-h-72 overflow-y-auto whitespace-pre-wrap break-all" : "text-xs text-muted italic bg-surface px-3 py-2.5 rounded-lg";
  document.getElementById("mdOutSize").textContent = out ? "(" + formatSize(out.length) + ")" : "";
  document.getElementById("mdErr").textContent = err || "(空)";
  document.getElementById("mdErr").className = err ? "text-xs text-red/70 bg-surface px-3 py-2.5 rounded-lg overflow-x-auto max-h-72 overflow-y-auto whitespace-pre-wrap break-all" : "text-xs text-muted italic bg-surface px-3 py-2.5 rounded-lg";
  document.getElementById("mdErrSize").textContent = err ? "(" + formatSize(err.length) + ")" : "";
  m.classList.remove("hidden");
  m.classList.add("flex");
}

function closeDetail() {
  const m = document.getElementById("detailModal");
  m.classList.add("hidden");
  m.classList.remove("flex");
}

document.addEventListener("keydown", e => { if (e.key === "Escape") closeDetail(); });

function renderAudit(entries) {
  const list = document.getElementById("auditList");
  currentEntries = entries;
  if (!entries.length) {
    list.innerHTML = '<div class="text-center py-12 text-muted text-sm">暂无审计记录。</div>';
    return;
  }
  list.innerHTML = entries.map((e, i) => {
    const hasOut = e.stdout && e.stdout.length > 0;
    const hasErr = e.stderr && e.stderr.length > 0;
    return `
    <div class="bg-surface border border-border rounded-lg hover:border-border-hover transition-colors group">
      <div class="flex items-center gap-1.5 px-3 py-2">
        ${hasOut ? '<span class="w-1.5 h-1.5 rounded-full bg-green shrink-0" title="stdout: ' + formatSize(e.stdout.length) + '"></span>' : ''}
        ${hasErr ? '<span class="w-1.5 h-1.5 rounded-full bg-red shrink-0" title="stderr: ' + formatSize(e.stderr.length) + '"></span>' : ''}
        <span class="text-[11px] text-muted w-[84px] shrink-0">${timeAgo(e.created_at)}</span>
        <code class="text-accent text-[11px] font-mono w-[88px] shrink-0 truncate" title="${escapeHtml(e.client_name)}">${e.client_name}</code>
        <code class="text-xs text-gray-300 font-mono truncate flex-1 min-w-0" title="${escapeHtml(e.command)}">$ ${escapeHtml(e.command)}</code>
        <span class="text-[11px] text-muted w-[46px] text-right shrink-0">${e.duration_ms}ms</span>
        ${exitBadge(e.exit_code)}
        <button onclick="openDetail(${i})" class="ml-1 rounded-md border border-border hover:border-accent hover:text-accent text-muted text-[11px] px-2 py-0.5 transition-colors shrink-0 font-medium">详情</button>
      </div>
    </div>`;
  }).join("");
}

// ---------------------------------------------------------------------------
// 分页
// ---------------------------------------------------------------------------

function updatePagination() {
  const totalPages = Math.ceil(totalEntries / PAGE_SIZE) || 1;
  document.getElementById("pageInfo").textContent =
    "第 " + (currentPage + 1) + " / " + totalPages + " 页（共 " + totalEntries + " 条）";
  document.getElementById("prevBtn").disabled = currentPage === 0;
  document.getElementById("nextBtn").disabled = currentPage >= totalPages - 1;
}

function prevPage() {
  if (currentPage > 0) { currentPage--; refreshAudit(); }
}
function nextPage() {
  const totalPages = Math.ceil(totalEntries / PAGE_SIZE) || 1;
  if (currentPage < totalPages - 1) { currentPage++; refreshAudit(); }
}

// ---------------------------------------------------------------------------
// 数据获取
// ---------------------------------------------------------------------------

function toISO(val) {
  // datetime-local → ISO 8601 (UTC).  Empty string → null.
  if (!val) return null;
  return new Date(val).toISOString();
}

async function refreshAudit() {
  const client = document.getElementById("filterClient").value;
  const after = toISO(document.getElementById("filterAfter").value);
  const before = toISO(document.getElementById("filterBefore").value);

  const btn = document.querySelector("button[onclick='refreshAudit()']");
  const origText = btn ? btn.textContent : "筛选";
  if (btn) { btn.textContent = "筛选…"; btn.disabled = true; }

  let url = AUDIT_API + "?limit=" + PAGE_SIZE + "&offset=" + (currentPage * PAGE_SIZE);
  if (client) url += "&client_name=" + encodeURIComponent(client);
  if (after) url += "&after=" + encodeURIComponent(after);
  if (before) url += "&before=" + encodeURIComponent(before);

  try {
    const data = await api("GET", url);
    totalEntries = data.total;
    renderAudit(data.entries);
    updatePagination();
    document.getElementById("totalCount").textContent = "（共 " + totalEntries + " 条）";
  } catch (e) {
    toast(e.message, true);
  } finally {
    if (btn) { btn.textContent = origText; btn.disabled = false; }
  }
}

async function clearAudit() {
  const client = document.getElementById("filterClient").value;
  const label = client ? "主机 " + client + " 的所有记录" : "全部审计记录";
  if (!confirm("确定删除 " + label + "？")) return;
  try {
    const params = client ? "?client_name=" + encodeURIComponent(client) : "?before_id=999999999";
    await api("DELETE", AUDIT_API + params);
    toast("已清除");
    currentPage = 0;
    refreshAudit();
  } catch (e) { toast(e.message, true); }
}

// ---------------------------------------------------------------------------
// 事件
// ---------------------------------------------------------------------------

document.getElementById("filterClient").onchange = () => { currentPage = 0; refreshAudit(); };

function clearFilters() {
  document.getElementById("filterClient").value = "";
  document.getElementById("filterAfter").value = "";
  document.getElementById("filterBefore").value = "";
  currentPage = 0;
  refreshAudit();
}

// ---------------------------------------------------------------------------
// 初始化
// ---------------------------------------------------------------------------

loadClientFilter();
refreshAudit();
