/**
 * RemoteBash 审计日志 — 前端逻辑
 */

const CLIENTS_API = "/api/clients";
const AUDIT_API = "/api/audit";
const PAGE_SIZE = 50;

let currentPage = 0;
let totalEntries = 0;

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
      sel.innerHTML += `<option value="${c.name}">${c.label ? c.label + " (" + c.name + ")" : c.name}</option>`;
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

function truncate(s, max) {
  if (!s) return "";
  return s.length > max ? s.slice(0, max) + "…" : s;
}

function exitBadge(code) {
  if (code === 0) return '<span class="text-green text-xs font-mono">0</span>';
  return '<span class="text-red text-xs font-mono">' + code + '</span>';
}

function renderAudit(entries) {
  const list = document.getElementById("auditList");
  if (!entries.length) {
    list.innerHTML = '<div class="text-center py-12 text-muted text-sm">暂无审计记录。</div>';
    return;
  }
  list.innerHTML = entries.map(e => `
    <div class="bg-surface border border-border rounded-lg p-3.5 hover:border-border-hover transition-colors">
      <div class="flex items-center gap-3 mb-2">
        <code class="text-accent text-xs font-mono">${e.client_name}</code>
        <span class="text-[11px] text-muted">${timeAgo(e.created_at)}</span>
        <span class="text-[11px] text-muted">${e.duration_ms}ms</span>
        ${exitBadge(e.exit_code)}
        <span class="text-[11px] text-muted truncate max-w-[300px]" title="${e.cwd}">${e.cwd || "~"}</span>
      </div>
      <code class="block text-xs font-mono text-gray-300 bg-surface px-3 py-2 rounded-md overflow-x-auto">$ ${e.command}</code>
      ${e.stdout ? '<pre class="text-xs text-muted mt-2 px-3 max-h-24 overflow-y-auto">' + truncate(e.stdout, 500) + '</pre>' : ''}
      ${e.stderr ? '<pre class="text-xs text-red/70 mt-1 px-3 max-h-24 overflow-y-auto">' + truncate(e.stderr, 500) + '</pre>' : ''}
    </div>`).join("");
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

async function refreshAudit() {
  const client = document.getElementById("filterClient").value;
  let url = AUDIT_API + "?limit=" + PAGE_SIZE + "&offset=" + (currentPage * PAGE_SIZE);
  if (client) url += "&client_name=" + encodeURIComponent(client);
  try {
    const data = await api("GET", url);
    totalEntries = data.total;
    renderAudit(data.entries);
    updatePagination();
    document.getElementById("totalCount").textContent = "（共 " + totalEntries + " 条）";
  } catch (e) { toast(e.message, true); }
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

// ---------------------------------------------------------------------------
// 初始化
// ---------------------------------------------------------------------------

loadClientFilter();
refreshAudit();
