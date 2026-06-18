/**
 * RemoteBash 审计日志 — 前端逻辑
 */

const CLIENTS_API = "/api/clients";
const AUDIT_API = "/api/audit";
const PAGE_SIZE = 50;

let currentPage = 0;
let totalEntries = 0;
let currentEntries = [];
let selectedIds = new Set();

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

function formatTime(iso) {
  const d = new Date(iso + "Z");
  return d.toLocaleString("zh-CN");
}

function exitBadge(code) {
  if (code === 0) return '<span class="text-green text-xs font-mono">0</span>';
  return '<span class="text-red text-xs font-mono">' + code + '</span>';
}

function escapeHtml(s) {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}

function isSftpEntry(entry) {
  return entry && entry.command && /^\[SFTP\s/.test(entry.command);
}

// Parse "[SFTP 上传] /src → /dst" into {label, src, dst}
function parseSftpCommand(cmd) {
  const m = cmd.match(/^\[SFTP\s(上传|下载)\]\s(.+?)\s→\s(.+)$/);
  return m ? { label: m[1], src: m[2], dst: m[3] } : null;
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
  const sftp = isSftpEntry(entry);
  const parsed = sftp ? parseSftpCommand(entry.command) : null;

  document.getElementById("mdClient").textContent = entry.client_name;
  document.getElementById("mdTime").textContent = formatTime(entry.created_at);

  // ── SFTP detail layout ──
  const cmdSection = document.getElementById("cmdSection");
  const sftpSection = document.getElementById("sftpSection");
  const detailMeta = document.getElementById("detailMeta");

  if (sftp && parsed && cmdSection && sftpSection) {
    cmdSection.classList.add("hidden");
    sftpSection.classList.remove("hidden");

    const isUpload = entry.cwd === "local2remote";
    document.getElementById("mdDirectionIcon").innerHTML = isUpload
      ? '<svg class="w-4 h-4 text-blue-400" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M7 16l-4-4m0 0l4-4m-4 4h16m-4 4l4-4m0 0l-4-4"/></svg>'
      : '<svg class="w-4 h-4 text-emerald-400" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M17 8l4 4m0 0l-4 4m4-4H3"/></svg>';
    document.getElementById("mdDirectionTag").textContent = parsed.label;
    document.getElementById("mdDirectionTag").className = isUpload
      ? "text-[10px] text-blue-400 bg-blue-400/10 rounded px-1.5 py-0.5"
      : "text-[10px] text-emerald-400 bg-emerald-400/10 rounded px-1.5 py-0.5";
    document.getElementById("mdSftpSrc").textContent = parsed.src;
    document.getElementById("mdSftpDst").textContent = parsed.dst;

    // Parse size from output
    const sizeMatch = entry.output && entry.output.match(/size_bytes:\s*(\d+)/);
    const sizeBytes = sizeMatch ? parseInt(sizeMatch[1]) : 0;
    document.getElementById("mdSftpSize").textContent = formatSize(sizeBytes);
    document.getElementById("mdSftpSizeRow").classList.toggle("hidden", sizeBytes === 0);

    // Copy button → copy both paths
    document.getElementById("copyCmdLabel").textContent = "复制路径";
    const copyTarget = document.getElementById("mdCopyTarget");
    if (copyTarget) {
      copyTarget.setAttribute("data-copy-target", "sftp");
      copyTarget.setAttribute("data-copy-text",
        (isUpload ? "上传: " : "下载: ") + parsed.src + " → " + parsed.dst);
    }

    detailMeta.innerHTML = `
      <div><span class="text-[10px] uppercase tracking-wide">方向</span> <code class="font-mono ml-1">${sftp ? entry.cwd : ""}</code></div>
      <div><span class="text-[10px] uppercase tracking-wide">文件大小</span> <span class="font-mono ml-1">${formatSize(sizeBytes)}</span></div>
      <div><span class="text-[10px] uppercase tracking-wide">耗时</span> <span id="mdDuration" class="font-mono ml-1">${entry.duration_ms}ms</span></div>
    `;
  } else {
    if (cmdSection) cmdSection.classList.remove("hidden");
    if (sftpSection) sftpSection.classList.add("hidden");
    document.getElementById("mdCmd").textContent = entry.command || "(空)";
    document.getElementById("copyCmdLabel").textContent = "复制";
    const copyTarget = document.getElementById("mdCopyTarget");
    if (copyTarget) {
      copyTarget.setAttribute("data-copy-target", "command");
      copyTarget.setAttribute("data-copy-text", entry.command || "");
    }

    detailMeta.innerHTML = `
      <div><span class="text-[10px] uppercase tracking-wide">工作目录</span> <code id="mdCwd" class="font-mono ml-1">${entry.cwd || "~"}</code></div>
      <div><span class="text-[10px] uppercase tracking-wide">耗时</span> <span id="mdDuration" class="font-mono ml-1">${entry.duration_ms}ms</span></div>
    `;
  }

  document.getElementById("mdExit").innerHTML = exitBadge(entry.exit_code);
  const output = entry.output || "";
  document.getElementById("mdOutput").textContent = output || "(空)";
  document.getElementById("mdOutput").className = output ? "text-xs text-gray-300 bg-surface px-3 py-2.5 rounded-lg overflow-x-auto max-h-72 overflow-y-auto whitespace-pre-wrap break-all" : "text-xs text-muted italic bg-surface px-3 py-2.5 rounded-lg";
  document.getElementById("mdOutputSize").textContent = output ? "(" + formatSize(output.length) + ")" : "";
  m.classList.remove("hidden");
  m.classList.add("flex");
}

function closeDetail() {
  const m = document.getElementById("detailModal");
  m.classList.add("hidden");
  m.classList.remove("flex");
}

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

function flashCopyLabel(labelId) {
  const label = document.getElementById(labelId);
  if (!label) return;
  const orig = label.textContent;
  label.textContent = "已复制";
  setTimeout(() => { label.textContent = orig; }, 1500);
}

async function copyCommand() {
  const target = document.getElementById("mdCopyTarget");
  const text = (target && target.getAttribute("data-copy-text")) || document.getElementById("mdCmd").textContent;
  const ok = await copyToClipboard(text);
  if (ok) { toast("已复制"); flashCopyLabel("copyCmdLabel"); }
  else toast("复制失败", true);
}

async function copyOutput() {
  const ok = await copyToClipboard(document.getElementById("mdOutput").textContent);
  if (ok) { toast("已复制输出"); flashCopyLabel("copyOutputLabel"); }
  else toast("复制失败", true);
}

document.addEventListener("keydown", e => { if (e.key === "Escape") closeDetail(); });

function renderAudit(entries) {
  const list = document.getElementById("auditList");
  currentEntries = entries;

  // 保留仍在当前页中的选中项（轮询刷新时不清空跨页选择）
  const survivingIds = new Set();
  for (const id of selectedIds) {
    if (entries.some(e => e.id === id)) survivingIds.add(id);
  }
  selectedIds = survivingIds;

  if (!entries.length) {
    list.innerHTML = '<div class="text-center py-12 text-muted text-sm">暂无审计记录。</div>';
    updateSelectionUI();
    return;
  }
  list.innerHTML = entries.map((e, i) => {
    const hasOutput = e.output && e.output.length > 0;
    const checked = selectedIds.has(e.id) ? " checked" : "";
    const sftp = isSftpEntry(e);

    if (sftp) {
      const parsed = parseSftpCommand(e.command);
      const isUpload = e.cwd === "local2remote";
      const icon = isUpload
        ? '<svg class="w-3.5 h-3.5 text-blue-400 shrink-0" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M7 16l-4-4m0 0l4-4m-4 4h16m-4 4l4-4m0 0l-4-4"/></svg>'
        : '<svg class="w-3.5 h-3.5 text-emerald-400 shrink-0" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M17 8l4 4m0 0l-4 4m4-4H3"/></svg>';
      const tag = isUpload
        ? '<span class="text-[10px] text-blue-400 bg-blue-400/10 rounded px-1 py-px shrink-0">上传</span>'
        : '<span class="text-[10px] text-emerald-400 bg-emerald-400/10 rounded px-1 py-px shrink-0">下载</span>';
      const sftpCmd = parsed
        ? `<code class="text-xs font-mono text-blue-400 truncate" title="${escapeHtml(parsed.src)}">${escapeHtml(parsed.src)}</code>
           <span class="text-[11px] text-muted shrink-0">→</span>
           <code class="text-xs font-mono text-emerald-400 truncate" title="${escapeHtml(parsed.dst)}">${escapeHtml(parsed.dst)}</code>`
        : `<code class="text-xs text-gray-300 font-mono truncate flex-1 min-w-0">${escapeHtml(e.command)}</code>`;
      return `
      <div class="bg-surface border border-border rounded-lg hover:border-border-hover transition-colors group">
        <div class="flex items-center gap-1.5 px-3 py-2">
          <input type="checkbox" class="audit-checkbox w-3.5 h-3.5 rounded border-border accent-accent cursor-pointer shrink-0"
                 data-id="${e.id}" onchange="toggleSelect(${e.id}, this.checked)"${checked} title="选择此条记录">
          ${icon}
          ${tag}
          <span class="text-[11px] text-muted w-[130px] shrink-0">${formatTime(e.created_at)}</span>
          <code class="text-accent text-[11px] font-mono shrink-0 whitespace-nowrap" title="${escapeHtml(e.client_name)}">${e.client_name}</code>
          <div class="flex items-center gap-1.5 flex-1 min-w-0">${sftpCmd}</div>
          <span class="text-[11px] text-muted w-[46px] text-right shrink-0">${e.duration_ms}ms</span>
          ${exitBadge(e.exit_code)}
          <button onclick="openDetail(${i})" class="ml-1 rounded-md border border-border hover:border-accent hover:text-accent text-muted text-[11px] px-2 py-0.5 transition-colors shrink-0 font-medium">详情</button>
          <button onclick="deleteEntry(${e.id})" class="rounded-md border border-transparent hover:border-red/30 hover:text-red text-muted text-[11px] px-1.5 py-0.5 transition-colors shrink-0 opacity-0 group-hover:opacity-100 font-medium" title="删除此条记录">&times;</button>
        </div>
      </div>`;
    }

    return `
    <div class="bg-surface border border-border rounded-lg hover:border-border-hover transition-colors group">
      <div class="flex items-center gap-1.5 px-3 py-2">
        <input type="checkbox" class="audit-checkbox w-3.5 h-3.5 rounded border-border accent-accent cursor-pointer shrink-0"
               data-id="${e.id}" onchange="toggleSelect(${e.id}, this.checked)"${checked} title="选择此条记录">
        ${hasOutput ? '<span class="w-1.5 h-1.5 rounded-full bg-green shrink-0" title="output: ' + formatSize(e.output.length) + '"></span>' : ''}
        <span class="text-[11px] text-muted w-[130px] shrink-0">${formatTime(e.created_at)}</span>
        <code class="text-accent text-[11px] font-mono shrink-0 whitespace-nowrap" title="${escapeHtml(e.client_name)}">${e.client_name}</code>
        <code class="text-xs text-gray-300 font-mono truncate flex-1 min-w-0" title="${escapeHtml(e.command)}">$ ${escapeHtml(e.command)}</code>
        <span class="text-[11px] text-muted w-[46px] text-right shrink-0">${e.duration_ms}ms</span>
        ${exitBadge(e.exit_code)}
        <button onclick="openDetail(${i})" class="ml-1 rounded-md border border-border hover:border-accent hover:text-accent text-muted text-[11px] px-2 py-0.5 transition-colors shrink-0 font-medium">详情</button>
        <button onclick="deleteEntry(${e.id})" class="rounded-md border border-transparent hover:border-red/30 hover:text-red text-muted text-[11px] px-1.5 py-0.5 transition-colors shrink-0 opacity-0 group-hover:opacity-100 font-medium" title="删除此条记录">&times;</button>
      </div>
    </div>`;
  }).join("");

  updateSelectionUI();
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

async function refreshAudit(silent) {
  const client = document.getElementById("filterClient").value;
  const after = toISO(document.getElementById("filterAfter").value);
  const before = toISO(document.getElementById("filterBefore").value);

  // 轮询刷新时跳过按钮状态变化，避免闪烁
  let btn, origText;
  if (!silent) {
    btn = document.querySelector("button[onclick='refreshAudit()']");
    origText = btn ? btn.textContent : "筛选";
    if (btn) { btn.textContent = "筛选…"; btn.disabled = true; }
  }

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
    if (!silent) toast(e.message, true);
  } finally {
    if (!silent && btn) { btn.textContent = origText; btn.disabled = false; }
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
// 选择与批量删除
// ---------------------------------------------------------------------------

function toggleSelect(id, checked) {
  if (checked) {
    selectedIds.add(id);
  } else {
    selectedIds.delete(id);
  }
  updateSelectionUI();
}

function toggleSelectAll(checked) {
  currentEntries.forEach(e => {
    if (checked) {
      selectedIds.add(e.id);
    } else {
      selectedIds.delete(e.id);
    }
  });
  // Sync all checkboxes on current page
  document.querySelectorAll(".audit-checkbox").forEach(cb => {
    cb.checked = checked;
  });
  updateSelectionUI();
}

function updateSelectionUI() {
  const btn = document.getElementById("deleteSelectedBtn");
  const selectAll = document.getElementById("selectAllCheckbox");
  const count = selectedIds.size;

  if (count > 0) {
    btn.classList.remove("hidden");
    btn.textContent = "删除选中 (" + count + ")";
  } else {
    btn.classList.add("hidden");
  }

  // Sync select-all checkbox with current page state
  if (currentEntries.length > 0) {
    const allChecked = currentEntries.every(e => selectedIds.has(e.id));
    const noneChecked = currentEntries.every(e => !selectedIds.has(e.id));
    selectAll.checked = allChecked;
    selectAll.indeterminate = !allChecked && !noneChecked;
  } else {
    selectAll.checked = false;
    selectAll.indeterminate = false;
  }
}

async function deleteEntry(id) {
  if (!confirm("确定删除此条审计记录？")) return;
  try {
    await api("DELETE", AUDIT_API + "?entry_id=" + id);
    toast("已删除");
    selectedIds.delete(id);
    refreshAudit();
  } catch (e) { toast(e.message, true); }
}

async function deleteSelected() {
  const count = selectedIds.size;
  if (count === 0) return;
  if (!confirm("确定删除选中的 " + count + " 条审计记录？")) return;
  try {
    const idsParam = Array.from(selectedIds).join(",");
    await api("DELETE", AUDIT_API + "?entry_ids=" + encodeURIComponent(idsParam));
    toast("已删除 " + count + " 条");
    selectedIds.clear();
    refreshAudit();
  } catch (e) { toast(e.message, true); }
}

function clearSelection() {
  selectedIds.clear();
  document.querySelectorAll(".audit-checkbox").forEach(cb => { cb.checked = false; });
  updateSelectionUI();
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
// 轮询刷新
// ---------------------------------------------------------------------------

let pollTimer = null;
let pollIntervalMs = 5000;

function setPollInterval(val) {
  pollIntervalMs = parseInt(val);
  // 如果正在轮询，重启以应用新间隔
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
    requestAnimationFrame(() => refreshAudit(true));
  }, pollIntervalMs);

  // 页面隐藏时暂停，显示时恢复
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
    // 页面隐藏 → 暂停轮询，记录偏移量
    if (pollTimer !== null) {
      clearInterval(pollTimer);
      pollTimer = null;
      document.getElementById("pollDot").classList.remove("bg-green", "animate-pulse");
      document.getElementById("pollDot").classList.add("bg-yellow");
    }
  } else {
    // 页面可见 → 立即刷新并恢复轮询
    if (pollTimer === null) {
      refreshAudit(true);
      pollTimer = setInterval(() => {
        requestAnimationFrame(() => refreshAudit(true));
      }, pollIntervalMs);
      document.getElementById("pollDot").classList.remove("bg-yellow");
      document.getElementById("pollDot").classList.add("bg-green", "animate-pulse");
    }
  }
}

// 页面卸裁时清理
window.addEventListener("beforeunload", () => {
  if (pollTimer !== null) clearInterval(pollTimer);
});

// ---------------------------------------------------------------------------
// 初始化
// ---------------------------------------------------------------------------

loadClientFilter();
refreshAudit();
