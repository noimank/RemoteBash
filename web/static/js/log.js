/**
 * RemoteBash 运行日志 — 前端逻辑
 */

const ROOT = window.BASE_URL_PREFIX || "";
const LOGS_API = ROOT + "/api/logs";
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
// 渲染
// ---------------------------------------------------------------------------

function formatTime(iso) {
  // SQLite stores without timezone; treat as UTC.
  const d = new Date(iso + "Z");
  return d.toLocaleString("zh-CN");
}

function formatTimeShort(iso) {
  const d = new Date(iso + "Z");
  const hh = String(d.getHours()).padStart(2, "0");
  const mm = String(d.getMinutes()).padStart(2, "0");
  const ss = String(d.getSeconds()).padStart(2, "0");
  return hh + ":" + mm + ":" + ss;
}

function escapeHtml(s) {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}

function levelBadge(level) {
  switch (level) {
    case "DEBUG":
      return '<span class="text-[10px] text-gray-400 bg-gray-400/10 rounded px-1.5 py-px font-mono shrink-0 w-14 text-center">DEBUG</span>';
    case "INFO":
      return '<span class="text-[10px] text-blue-400 bg-blue-400/10 rounded px-1.5 py-px font-mono shrink-0 w-14 text-center">INFO</span>';
    case "WARN":
      return '<span class="text-[10px] text-yellow bg-yellow/10 rounded px-1.5 py-px font-mono shrink-0 w-14 text-center">WARN</span>';
    case "ERROR":
      return '<span class="text-[10px] text-red bg-red/10 rounded px-1.5 py-px font-mono shrink-0 w-14 text-center">ERROR</span>';
    default:
      return '<span class="text-[10px] text-muted bg-muted/10 rounded px-1.5 py-px font-mono shrink-0 w-14 text-center">' + escapeHtml(level) + '</span>';
  }
}

function formatAttrs(attrsJson) {
  if (!attrsJson) return "";
  try {
    const obj = JSON.parse(attrsJson);
    const pairs = Object.entries(obj).map(([k, v]) => {
      let val = typeof v === "string" ? v : JSON.stringify(v);
      return '<span class="text-[11px] text-muted">' + escapeHtml(k) + '</span>=<span class="text-[11px] text-gray-400">' + escapeHtml(val) + '</span>';
    });
    return '<span class="text-border mx-1">|</span> ' + pairs.join(" ");
  } catch (_) {
    return "";
  }
}

function renderLogs(entries) {
  const list = document.getElementById("logList");
  currentEntries = entries;

  if (!entries.length) {
    list.innerHTML = '<div class="text-center py-12 text-muted text-sm">暂无日志记录。</div>';
    return;
  }

  list.innerHTML = entries.map(e => `
    <div class="flex items-start gap-2 px-3 py-1.5 hover:bg-surface/50 rounded transition-colors group">
      <span class="text-[11px] text-muted font-mono w-[68px] shrink-0 pt-px">${formatTimeShort(e.created_at)}</span>
      ${levelBadge(e.level)}
      <span class="text-[12px] text-gray-200 leading-relaxed break-all">${escapeHtml(e.message)}<span class="inline-flex flex-wrap items-center gap-0.5">${formatAttrs(e.attrs)}</span></span>
    </div>
  `).join("");
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
  if (currentPage > 0) { currentPage--; refreshLogs(); }
}

function nextPage() {
  const totalPages = Math.ceil(totalEntries / PAGE_SIZE) || 1;
  if (currentPage < totalPages - 1) { currentPage++; refreshLogs(); }
}

// ---------------------------------------------------------------------------
// 数据获取
// ---------------------------------------------------------------------------

function toISO(val) {
  if (!val) return null;
  return new Date(val).toISOString();
}

async function refreshLogs(silent) {
  const level = document.getElementById("filterLevel").value;
  const after = toISO(document.getElementById("filterAfter").value);
  const before = toISO(document.getElementById("filterBefore").value);

  let btn, origText;
  if (!silent) {
    btn = document.querySelector("button[onclick='refreshLogs()']");
    origText = btn ? btn.textContent : "筛选";
    if (btn) { btn.textContent = "筛选…"; btn.disabled = true; }
  }

  let url = LOGS_API + "?limit=" + PAGE_SIZE + "&offset=" + (currentPage * PAGE_SIZE);
  if (level) url += "&level=" + encodeURIComponent(level);
  if (after) url += "&after=" + encodeURIComponent(after);
  if (before) url += "&before=" + encodeURIComponent(before);

  try {
    const data = await api("GET", url);
    totalEntries = data.total;
    renderLogs(data.entries);
    updatePagination();
    document.getElementById("totalCount").textContent = "（共 " + totalEntries + " 条）";
  } catch (e) {
    if (!silent) toast(e.message, true);
  } finally {
    if (!silent && btn) { btn.textContent = origText; btn.disabled = false; }
  }
}

async function clearLogs() {
  if (!confirm("确定清除全部运行日志？此操作不可撤销。")) return;
  try {
    // Use a large before_id to delete everything.
    await api("DELETE", LOGS_API + "?before_id=999999999");
    toast("日志已清除");
    currentPage = 0;
    refreshLogs();
  } catch (e) { toast(e.message, true); }
}

// ---------------------------------------------------------------------------
// 过滤
// ---------------------------------------------------------------------------

document.getElementById("filterLevel").onchange = () => { currentPage = 0; refreshLogs(); };

function clearFilters() {
  document.getElementById("filterLevel").value = "";
  document.getElementById("filterAfter").value = "";
  document.getElementById("filterBefore").value = "";
  currentPage = 0;
  refreshLogs();
}

// ---------------------------------------------------------------------------
// 轮询刷新
// ---------------------------------------------------------------------------

let pollTimer = null;
let pollIntervalMs = 3000;

function setPollInterval(val) {
  pollIntervalMs = parseInt(val);
  if (pollTimer !== null) {
    stopPolling();
    startPolling();
  }
}

function togglePolling() {
  if (pollTimer !== null) {
    stopPolling();
  } else {
    startPolling();
  }
}

function startPolling() {
  if (pollTimer !== null) return;

  const btn = document.getElementById("pollToggleBtn");
  const dot = document.getElementById("pollDot");
  btn.classList.add("border-accent", "text-accent");
  btn.classList.remove("border-border", "text-muted");
  dot.classList.remove("bg-muted");
  dot.classList.add("bg-green", "animate-pulse");
  btn.title = "停止自动刷新";

  pollTimer = setInterval(() => {
    requestAnimationFrame(() => refreshLogs(true));
  }, pollIntervalMs);

  document.addEventListener("visibilitychange", onVisibilityChange);
}

function stopPolling() {
  if (pollTimer === null) return;

  clearInterval(pollTimer);
  pollTimer = null;

  const btn = document.getElementById("pollToggleBtn");
  const dot = document.getElementById("pollDot");
  btn.classList.remove("border-accent", "text-accent");
  btn.classList.add("border-border", "text-muted");
  dot.classList.add("bg-muted");
  dot.classList.remove("bg-green", "animate-pulse");
  btn.title = "自动刷新";

  document.removeEventListener("visibilitychange", onVisibilityChange);
}

function onVisibilityChange() {
  if (document.hidden) {
    if (pollTimer !== null) {
      clearInterval(pollTimer);
      pollTimer = null;
      document.getElementById("pollDot").classList.remove("bg-green", "animate-pulse");
      document.getElementById("pollDot").classList.add("bg-yellow");
    }
  } else {
    if (pollTimer === null) {
      refreshLogs(true);
      pollTimer = setInterval(() => {
        requestAnimationFrame(() => refreshLogs(true));
      }, pollIntervalMs);
      document.getElementById("pollDot").classList.remove("bg-yellow");
      document.getElementById("pollDot").classList.add("bg-green", "animate-pulse");
    }
  }
}

window.addEventListener("beforeunload", () => {
  if (pollTimer !== null) clearInterval(pollTimer);
});

// ---------------------------------------------------------------------------
// 初始化
// ---------------------------------------------------------------------------

refreshLogs();
