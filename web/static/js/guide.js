/**
 * RemoteBash 使用指南 — 标签切换与复制
 * 注：本页面不加载 app.js，故 toast/copy 逻辑在此自包含实现。
 */

// ---------------------------------------------------------------------------
// 提示（复用 base.gohtml 中的 #toast 容器）
// ---------------------------------------------------------------------------

function toast(msg, isErr) {
  const el = document.getElementById("toast");
  if (!el) return;
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
  }, 2000);
}

// ---------------------------------------------------------------------------
// 复制（兼容局域网 IP 等非安全上下文）
// ---------------------------------------------------------------------------

async function copyToClipboard(text) {
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text);
      return true;
    }
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

// 复制按钮所在的 .config-block 内 <pre> 的文本。
async function copyBlock(btn) {
  const block = btn.closest(".config-block");
  if (!block) return;
  const pre = block.querySelector("pre");
  if (!pre) return;
  const ok = await copyToClipboard(pre.textContent);
  const label = btn.querySelector(".copy-label");
  if (ok) {
    if (label) {
      const prev = label.textContent;
      label.textContent = "已复制 ✓";
      setTimeout(() => (label.textContent = prev), 1500);
    }
    toast("已复制到剪贴板");
  } else {
    toast("复制失败，请手动选择文本", true);
  }
}

// ---------------------------------------------------------------------------
// 标签切换
// ---------------------------------------------------------------------------

function switchTab(btn) {
  const bar = btn.closest(".tab-bar");
  if (!bar) return;
  const name = btn.dataset.tab;
  const wrapper = bar.parentElement;

  bar.querySelectorAll(".tab").forEach((b) => {
    const active = b === btn;
    b.classList.toggle("tab-active", active);
    // 激活态：填充背景与强调边框；非激活：透明边框、静默文字。
    if (active) {
      b.classList.add("bg-surface-overlay", "text-white", "border-accent");
      b.classList.remove("text-muted", "border-transparent");
    } else {
      b.classList.remove("bg-surface-overlay", "text-white", "border-accent");
      b.classList.add("text-muted", "border-transparent");
    }
  });

  if (wrapper) {
    wrapper.querySelectorAll(".tab-panel").forEach((p) => {
      p.classList.toggle("hidden", p.dataset.panel !== name);
    });
  }
}
