"use strict";

// Host glue providing the globals expected by context.js and ctxtext.js.
const $ = (s) => document.querySelector(s);
const $$ = (s) => document.querySelectorAll(s);

const CHEV = "m5.5 6.5 2.5 2.5 2.5-2.5";
const COPY_ICON = "M5.5 5.5V3.5h7v7h-2M3.5 5.5h7v7h-7z";
const IOS_EASE = "cubic-bezier(.32, .72, 0, 1)";

const state = {
  agents: [
    { id: "claude-code", name: "Claude Code", icon: "claude-color" },
    { id: "codex", name: "Codex", icon: "openai" },
    { id: "joycode", name: "JoyCode", icon: "jcode" },
    { id: "jcode", name: "JoyCode", icon: "jcode" },
    { id: "opencode", name: "OpenCode", icon: "generic" },
    { id: "cursor", name: "Cursor", icon: "generic" },
    { id: "gemini", name: "Gemini CLI", icon: "gemini-color" },
    { id: "OpenAI", name: "OpenAI", icon: "openai" },
    { id: "proxy-client", name: "Proxy Client", icon: "generic" },
    { id: "imported", name: "Imported", icon: "generic" },
  ],
  clients: [],
};
state.clients = state.agents;

const prefs = {};
const featuresOff = new Set();
const gatewayMode = false;
let confirmAsk = null;

function calm() { return false; }

function el(tag, cls, text) {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text !== undefined && text !== null) n.textContent = text;
  return n;
}

function svg(d, size = 12, stroke = 1.5) {
  const s = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  s.setAttribute("viewBox", "0 0 16 16");
  s.setAttribute("width", String(size));
  s.setAttribute("height", String(size));
  s.setAttribute("aria-hidden", "true");
  const p = document.createElementNS("http://www.w3.org/2000/svg", "path");
  p.setAttribute("d", d);
  p.setAttribute("fill", "none");
  p.setAttribute("stroke", "currentColor");
  p.setAttribute("stroke-width", String(stroke));
  p.setAttribute("stroke-linecap", "round");
  p.setAttribute("stroke-linejoin", "round");
  s.append(p);
  return s;
}

function icon(name) {
  const e = el("span", "ic");
  e.dataset.icon = name || "";
  if (!name || name === "generic") {
    e.append(svg("M8 2.2 13.2 5.1v5.8L8 13.8 2.8 10.9V5.1Z M8 8v5.8 M2.8 5.1 8 8l5.2-2.9", 16, 1.4));
    return e;
  }
  if (name.endsWith("-color")) {
    const img = el("img");
    img.src = "/icons/" + encodeURIComponent(name) + ".svg";
    img.alt = "";
    img.onerror = () => e.replaceWith(icon("generic"));
    e.append(img);
    return e;
  }
  const m = el("span", "mask");
  m.style.setProperty("--i", `url("/icons/${encodeURIComponent(name)}.svg")`);
  const probe = new Image();
  probe.onerror = () => e.replaceWith(icon("generic"));
  probe.src = `/icons/${encodeURIComponent(name)}.svg`;
  e.append(m);
  return e;
}

function status(msg, kind = "", ms = kind === "err" ? 7000 : 3200) {
  const s = $("#status");
  if (!s) return;
  s.textContent = msg;
  s.title = msg;
  s.className = "status " + kind;
  clearTimeout(status._t);
  if (!msg) return;
  status._t = setTimeout(() => { s.textContent = ""; s.className = "status"; }, ms);
}

function statusErr(e) {
  status(e?.message || String(e), "err");
}

function copyBtn(text, what, message) {
  const b = el("button", "copy");
  b.type = "button";
  b.title = t("Copy");
  b.append(svg(COPY_ICON, 12, 1.5));
  b.onclick = async (ev) => {
    ev.stopPropagation();
    try {
      await navigator.clipboard.writeText(text);
      status(message || ((what ? what + ": " : "") + t("Copied")), "ok", 2200);
    } catch {
      status(t("Copy failed"), "err");
    }
  };
  return b;
}

function segs(items, current, onPick) {
  const box = el("div", "segs");
  for (const [id, name] of items) {
    const b = el("button", "opt" + (id === current ? " on" : ""), name);
    b.type = "button";
    b.dataset.seg = id;
    b.setAttribute("aria-pressed", String(id === current));
    b.onclick = () => {
      for (const x of box.querySelectorAll(".opt")) {
        x.classList.toggle("on", x === b);
        x.setAttribute("aria-pressed", String(x === b));
      }
      onPick(id);
    };
    box.append(b);
  }
  return box;
}

const hitPct = (h, d = 0) => {
  let s = (100 * h).toFixed(d);
  while (d < 2 && h > 0 && h < 1 && (+s >= 100 || +s <= 0)) s = (100 * h).toFixed(++d);
  return (h < 1 && +s >= 100 ? "99.99" : h > 0 && +s <= 0 ? "0.01" : s) + "%";
};

function ago(when) {
  const sec = (new Date(when) - Date.now()) / 1000;
  try {
    const rtf = new Intl.RelativeTimeFormat(intlLang(), { numeric: "auto" });
    for (const [unit, n] of [["year", 31536000], ["month", 2592000], ["week", 604800], ["day", 86400], ["hour", 3600], ["minute", 60]]) {
      if (Math.abs(sec) >= n) return rtf.format(Math.round(sec / n), unit);
    }
  } catch {}
  return t("just now");
}

function fmtDur(sec) {
  const m = Math.round(sec / 60);
  if (!sec) return "0";
  if (m < 1) return t("<1m");
  if (m < 60) return t("{m}m", { m });
  return t("{h}h {m}m", { h: Math.floor(m / 60), m: m % 60 });
}

function openModal(content) {
  const m = $("#modal"), d = m.firstElementChild;
  d.classList.toggle("wide", content.classList.contains("wide"));
  d.replaceChildren(content);
  m.hidden = false;
}

function closeConfirmAsk() {
  confirmAsk = null;
  const m = $("#modal");
  m.hidden = true;
  m.classList.remove("lib");
  m.firstElementChild?.replaceChildren();
}

$("#modal")?.addEventListener("click", (e) => {
  if (e.target === $("#modal")) closeConfirmAsk();
});
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape" && !$("#modal")?.hidden) {
    e.preventDefault();
    closeConfirmAsk();
  }
});

// Active viewer state
const viewerState = {
  source: localStorage.getItem("ctxview.source") || "all",
  lang: localStorage.getItem("ctxview.lang") || "zh",
  theme: localStorage.getItem("ctxview.theme") || "dark",
  redact: localStorage.getItem("ctxview.redact") !== "0",
  routes: [],
  sessions: [],
  counts: { all: 0, local: 0, proxy: 0, upload: 0, demo: 0 },
  activeRouteId: 0,
  heroCard: null,
};

document.documentElement.dataset.theme = viewerState.theme;
setLocale(viewerState.lang);

const _origT = typeof window.t === "function" ? window.t : (k) => k;
const _neutralI18n = {
  zh: {
    "Through magpie": "Agent 概览",
    "What each agent's prompts hold, as magpie's gateway read them: the score and tags are worked out from the requests of the range.":
      "每个 Agent 的提示词结构与健康度概览；评分和标签根据当前范围内的请求计算。",
    "Requests your agents send through magpie show here.":
      "采集到的 Agent 请求与本地 Session 会话会显示在这里。",
    "Set by magpie": "系统设置",
    "As magpie sent it, read from the request archive, masked where masking is on.":
      "读自请求存档或本地会话记录，开启脱敏时为脱敏后的内容。",
    "As magpie sent it, masked where masking is on. Kept in memory for the last {n} requests only.":
      "读自内存存档，开启脱敏时为脱敏后的内容。仅在内存中保留最近 {n} 个请求。",
    "An image: magpie doesn't show it here": "图片内容（此处省略预览）",
    "This request's text is no longer kept: magpie keeps the text of the last {n} requests, in memory only, while it runs.":
      "该请求的原始文本已不在内存缓存中（仅保留最近 {n} 个请求）。",
    "Not in the request archive: it is saved just after the call, the oldest are removed past the archive's size, or the save failed (magpie's log says why)":
      "未找到该请求的原始正文存档。",
    "{who}: the share needs Codex 0.163 or later; until it's updated magpie writes only async_compaction = true, at 65%":
      "{who}：该设置需要 Codex 0.163 或更高版本；升级前仅在 65% 时写入 async_compaction = true",
  },
  en: {
    "Through magpie": "Agent Overview",
    "What each agent's prompts hold, as magpie's gateway read them: the score and tags are worked out from the requests of the range.":
      "What each agent's prompts hold: the score and tags are worked out from the requests in range.",
    "Requests your agents send through magpie show here.":
      "Captured agent requests and local sessions show here.",
    "Set by magpie": "System set",
    "As magpie sent it, read from the request archive, masked where masking is on.":
      "Read from the request or session archive, masked when redaction is on.",
    "As magpie sent it, masked where masking is on. Kept in memory for the last {n} requests only.":
      "Read from memory archive, masked when redaction is on. Kept for the last {n} requests.",
    "An image: magpie doesn't show it here": "An image (preview omitted)",
    "This request's text is no longer kept: magpie keeps the text of the last {n} requests, in memory only, while it runs.":
      "This request's text is no longer in memory (kept for the last {n} requests).",
    "Not in the request archive: it is saved just after the call, the oldest are removed past the archive's size, or the save failed (magpie's log says why)":
      "Not found in the request archive.",
    "{who}: the share needs Codex 0.163 or later; until it's updated magpie writes only async_compaction = true, at 65%":
      "{who}: the share needs Codex 0.163 or later; until it's updated only async_compaction = true at 65% is written",
  },
};
window.t = function (key, vars) {
  const lang = viewerState.lang === "en" ? "en" : "zh";
  const table = _neutralI18n[lang] || _neutralI18n.zh;
  if (table && Object.prototype.hasOwnProperty.call(table, key)) {
    let out = table[key];
    if (vars && typeof vars === "object") {
      for (const [k, v] of Object.entries(vars)) {
        out = out.replaceAll("{" + k + "}", String(v));
      }
    }
    return out;
  }
  const res = _origT(key, vars);
  return typeof res === "string"
    ? res.replace(/喜雀/g, "").replace(/\bmagpie\b/gi, lang === "en" ? "system" : "系统").trim()
    : res;
};

function withRedactParam(url) {
  if (typeof url !== "string" || !url.startsWith("/api/") || /[?&]redact=/.test(url)) {
    return url;
  }
  return url + (url.includes("?") ? "&" : "?") + "redact=" + (viewerState.redact ? "1" : "0");
}

const rawFetch = window.fetch.bind(window);
window.fetch = (input, init) => {
  if (typeof input === "string" && input.startsWith("/api/")) {
    input = withRedactParam(input);
  }
  return rawFetch(input, init);
};

async function api(path, body) {
  let url = "/api/" + path;
  if (path.startsWith("context?")) {
    url += "&source=" + encodeURIComponent(viewerState.source);
  }
  const init = body !== undefined ? {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  } : {};
  const res = await fetch(url, init);
  if (!res.ok) throw new Error((await res.text()).trim() || res.statusText);
  const data = await res.json();
  if (path.startsWith("context?") && data?.source_counts) {
    viewerState.counts = data.source_counts;
    viewerState.sessions = data.sessions || [];
    renderSourceTabs();
    if (!viewerState.activeRouteId || !viewerState.routes.some((r) => r.id === viewerState.activeRouteId)) {
      const target = data.latest_route?.id || data.sessions?.[0]?.latestId;
      if (target) await selectHeroRoute(target);
      else renderHeroEmpty();
    }
  }
  return data;
}

window.openRoute = async (id, _time, opts) => {
  await selectHeroRoute(id);
  if (opts?.context) {
    $("#heroInspector")?.scrollIntoView({ behavior: "smooth", block: "start" });
  }
};

function isSkillsHash(h) {
  return (
    h.startsWith("#/global-workspace") ||
    h.startsWith("#/lobster-workspace") ||
    h.startsWith("#/skills-library") ||
    h === "#/projects" ||
    h.startsWith("#/projects/") ||
    h.startsWith("#/project/")
  );
}

function initialViewFromLocation() {
  const h = location.hash || "";
  if (isSkillsHash(h)) return "skills";
  if (h === "#/context" || h.startsWith("#/context/")) return "context";
  const v = typeof params !== "undefined" ? params.get("view") : null;
  if (v === "skills" || v === "context") return v;
  return localStorage.getItem("ctxview.view") === "skills" ? "skills" : "context";
}

function syncViewDom() {
  const isSkills = viewerState.view === "skills";
  const vc = $("#viewContext");
  const vs = $("#viewSkills");
  if (vc) vc.hidden = isSkills;
  if (vs) vs.hidden = !isSkills;
  const navCtx = $("#navContext");
  const navSk = $("#navSkills");
  if (navCtx) {
    if (!isSkills) navCtx.setAttribute("aria-current", "page");
    else navCtx.removeAttribute("aria-current");
  }
  if (navSk) {
    if (isSkills) navSk.setAttribute("aria-current", "page");
    else navSk.removeAttribute("aria-current");
  }
  for (const id of ["#btnScanDirs", "#btnProxyHelp", "#btnRescan", "#btnImport"]) {
    const b = $(id);
    if (b) b.hidden = isSkills;
  }
}

async function setActiveView(nextView, opts = {}) {
  const normalized = nextView === "skills" ? "skills" : "context";
  viewerState.view = normalized;
  localStorage.setItem("ctxview.view", normalized);
  if (!opts.fromHash) {
    if (normalized === "context") {
      if (location.hash && !location.hash.startsWith("#/context")) {
        location.hash = "#/context";
      }
    } else if (!isSkillsHash(location.hash || "")) {
      location.hash = "#/global-workspace";
    }
  }
  syncViewDom();
  applyThemeAndLangLabels();
  if (normalized === "skills") {
    await window.SkillsWorkspace?.onActivate?.();
  } else {
    await refreshAll();
  }
}

function applyThemeAndLangLabels() {
  const isZh = viewerState.lang === "zh";
  const isSkills = viewerState.view === "skills";
  $("#cvTitle").textContent = isSkills
    ? (isZh ? "全局与项目 Agent Skills 工作区" : "Global & Project Agent Skills Workspace")
    : (isZh ? "上下文窗口 · Context Window" : "Context Window Viewer");
  $("#cvSub").textContent = isSkills
    ? (isZh
        ? "56+ AI Agent 全局技能 · 项目与关联工作区 · 中央仓库管理 · 软链/复制同步 · Preset 编排"
        : "56+ AI Agents Global Skills · Project & Linked Workspaces · Central Library · Presets")
    : (isZh
        ? "支持网关抓包（方案1）/ 本地 Session（Claude · Codex · JoyCode）/ 手动导入"
        : "Reverse Proxy Capture / Local Sessions (Claude · Codex · JoyCode) / Manual Import");
  const navCtx = $("#navContext");
  const navSk = $("#navSkills");
  const viewNav = $("#viewNav");
  if (navCtx) navCtx.textContent = isZh ? "上下文窗口" : "Context Window";
  if (navSk) navSk.textContent = isZh ? "Agent Skills 工作区" : "Agent Skills";
  if (viewNav) viewNav.setAttribute("aria-label", isZh ? "视图切换" : "View Switcher");
  const btnRedact = $("#btnRedact");
  if (btnRedact) {
    btnRedact.classList.toggle("on", viewerState.redact);
    btnRedact.textContent = isZh
      ? (viewerState.redact ? "脱敏: 开" : "脱敏: 关")
      : (viewerState.redact ? "Redact: ON" : "Redact: OFF");
    btnRedact.title = isZh
      ? "默认开启脱敏：密钥、个人隐私、本机用户名路径将替换为安全占位符"
      : "Redaction replaces API keys, PII, and home paths with placeholders by default";
  }
  const btnScanDirs = $("#btnScanDirs");
  if (btnScanDirs) btnScanDirs.textContent = isZh ? "扫描目录" : "Scan Dirs";
  $("#btnProxyHelp").textContent = isZh ? "代理网关配置" : "Proxy Setup";
  $("#btnRescan").textContent = isZh ? "重新扫描本地" : "Rescan Local";
  $("#btnImport").textContent = isZh ? "导入 / 粘贴数据" : "Import / Paste";
  $("#btnLang").textContent = isZh ? "中文 / EN" : "EN / 中文";
  $("#btnTheme").textContent = isZh ? (viewerState.theme === "dark" ? "深色" : "浅色") : (viewerState.theme === "dark" ? "Dark" : "Light");
  $("#lblSession").textContent = isZh ? "会话 / 来源" : "Session";
  $("#lblTurn").textContent = isZh ? "轮次 / 请求" : "Turn / Request";
  $("#btnPrevTurn").textContent = isZh ? "← 上一轮" : "← Prev";
  $("#btnNextTurn").textContent = isZh ? "下一轮 →" : "Next →";
  $("#proxyHint").textContent = (isZh ? "代理地址: " : "Proxy: ") + location.origin;
}

function renderSourceTabs() {
  const box = $("#sourceTabs");
  if (!box) return;
  const isZh = viewerState.lang === "zh";
  const defs = [
    ["all", isZh ? "全部" : "All"],
    ["local", isZh ? "本地 Session" : "Local Sessions"],
    ["proxy", isZh ? "网关抓包 (方案1)" : "Proxy Capture"],
    ["upload", isZh ? "手动导入" : "Imported"],
    ["demo", isZh ? "示例卡片" : "Demo"],
  ];
  box.replaceChildren();
  for (const [key, label] of defs) {
    const btn = el("button", "cv-src-btn" + (viewerState.source === key ? " on" : ""));
    btn.type = "button";
    btn.append(el("span", "", label), el("span", "cv-src-count", String(viewerState.counts[key] || 0)));
    btn.onclick = async () => {
      viewerState.source = key;
      localStorage.setItem("ctxview.source", key);
      viewerState.activeRouteId = 0;
      renderSourceTabs();
      await refreshAll();
    };
    box.append(btn);
  }
}

async function refreshRoutesList() {
  const data = await fetch("/api/routes?limit=200&source=" + encodeURIComponent(viewerState.source)).then((r) => r.json());
  viewerState.routes = data.routes || [];
  if (data.counts) {
    viewerState.counts = data.counts;
    renderSourceTabs();
  }
  populatePickers();
}

function populatePickers() {
  const sessSel = $("#sessionSelect");
  const turnSel = $("#turnSelect");
  if (!sessSel || !turnSel) return;

  const groups = new Map();
  for (const r of viewerState.routes) {
    const key = (r.agent || "agent") + "::" + (r.session || String(r.id));
    if (!groups.has(key)) {
      groups.set(key, {
        key,
        session: r.session || String(r.id),
        label: `[${r.source}] ${r.agent || "agent"} · ${r.session_title || r.model || r.session || ("#" + r.id)}`,
        routes: [],
      });
    }
    groups.get(key).routes.push(r);
  }

  for (const g of groups.values()) {
    g.routes.sort((a, b) => new Date(a.time) - new Date(b.time) || a.id - b.id);
  }

  let active = viewerState.routes.find((r) => r.id === viewerState.activeRouteId) || viewerState.routes[0];
  let activeKey = active ? ((active.agent || "agent") + "::" + (active.session || String(active.id))) : "";

  sessSel.replaceChildren();
  for (const g of groups.values()) {
    const opt = document.createElement("option");
    opt.value = g.key;
    opt.textContent = `${g.label} (${g.routes.length})`;
    if (g.key === activeKey) opt.selected = true;
    sessSel.append(opt);
  }

  const curGroup = groups.get(activeKey);
  turnSel.replaceChildren();
  const groupRoutes = curGroup ? curGroup.routes : [];
  groupRoutes.forEach((r, idx) => {
    const opt = document.createElement("option");
    opt.value = String(r.id);
    const tok = r.prompt?.tokens ? window.ctxFmt(r.prompt.tokens) : "—";
    const turnLabel = r.turn ? `Turn ${r.turn}` : `#${idx + 1}`;
    opt.textContent = `${turnLabel} · ${tok} · ${r.model || r.protocol || ""}`;
    if (active && r.id === active.id) opt.selected = true;
    turnSel.append(opt);
  });

  const curIdx = active ? groupRoutes.findIndex((r) => r.id === active.id) : -1;
  $("#btnPrevTurn").disabled = curIdx <= 0;
  $("#btnNextTurn").disabled = curIdx < 0 || curIdx >= groupRoutes.length - 1;

  sessSel.onchange = () => {
    const g = groups.get(sessSel.value);
    if (g?.routes.length) {
      selectHeroRoute(g.routes[g.routes.length - 1].id);
    }
  };
  turnSel.onchange = () => {
    const id = Number(turnSel.value);
    if (id) selectHeroRoute(id);
  };
  $("#btnPrevTurn").onclick = () => {
    if (curIdx > 0) selectHeroRoute(groupRoutes[curIdx - 1].id);
  };
  $("#btnNextTurn").onclick = () => {
    if (curIdx >= 0 && curIdx < groupRoutes.length - 1) selectHeroRoute(groupRoutes[curIdx + 1].id);
  };
}

function renderHeroEmpty() {
  viewerState.heroCard = null;
  const mount = $("#heroCtxMount");
  if (!mount) return;
  const isZh = viewerState.lang === "zh";
  mount.replaceChildren(el("div", "cv-empty-card", isZh
    ? "当前筛选下暂无请求数据。你可以点击右上角「导入 / 粘贴数据」，或通过本地代理发送请求。"
    : "No requests captured in this filter yet. Click Import / Paste above or send requests through the local proxy."));
}

async function selectHeroRoute(id) {
  if (!id) return renderHeroEmpty();
  try {
    const route = await fetch("/api/route?id=" + encodeURIComponent(id)).then((r) => {
      if (!r.ok) throw new Error("not found");
      return r.json();
    });
    viewerState.activeRouteId = route.id;
    populatePickers();
    if (!route.prompt) {
      return renderHeroEmpty();
    }
    const sess = viewerState.sessions.find((s) => s.key === route.session && s.agent === route.agent)
      || viewerState.sessions.find((s) => s.points?.some((p) => p.id === route.id));
    const series = sess?.points || viewerState.routes
      .filter((r) => r.session && r.session === route.session && r.prompt)
      .sort((a, b) => new Date(a.time) - new Date(b.time) || a.id - b.id)
      .map((r) => ({
        id: r.id,
        time: r.time,
        tokens: r.prompt.tokens,
        window: r.prompt.window,
        cache: r.cache_read || 0,
      }));

    const opts = {
      place: "hero",
      session: route.session || String(route.id),
      series,
      crumbs: [route.source, route.agent, route.session_title || route.session].filter(Boolean),
      onPoint: (pt) => selectHeroRoute(pt.id),
    };

    const mount = $("#heroCtxMount");
    if (viewerState.heroCard && mount.contains(viewerState.heroCard)) {
      viewerState.heroCard.ctxUpdate(route, opts);
    } else {
      viewerState.heroCard = window.ctxCard(route, opts);
      mount.replaceChildren(viewerState.heroCard);
    }
  } catch (e) {
    statusErr(e);
  }
}

function openImportModal() {
  const isZh = viewerState.lang === "zh";
  const ed = el("div", "editor wide");
  const head = el("div", "ehead");
  head.append(el("b", "", isZh ? "导入 LLM 请求 JSON / Session JSONL" : "Import Request JSON or Session JSONL"));

  const body = el("div", "ebody");
  const drop = el("div", "cv-dropzone", isZh
    ? "点击选择文件，或拖拽 .json / .jsonl 文件到这里（支持 Anthropic / OpenAI / Gemini 请求体及 Claude Code / Codex 会话文件）"
    : "Click to choose or drop a .json / .jsonl file here");
  const fileIn = el("input");
  fileIn.type = "file";
  fileIn.accept = ".json,.jsonl,.txt";
  fileIn.hidden = true;
  drop.onclick = () => fileIn.click();

  const ta = el("textarea", "cv-textarea");
  ta.placeholder = isZh
    ? '粘贴完整请求 JSON，例如 {"model": "claude-sonnet-4-5", "system": "...", "tools": [...], "messages": [...]} 或多行 .jsonl'
    : 'Paste raw request JSON or multi-line .jsonl here...';

  const pathIn = el("input");
  pathIn.placeholder = isZh ? "或直接输入本机文件路径，如 ~/.claude/projects/.../xxx.jsonl" : "Or enter a local file path";
  const pathField = el("label", "cv-field");
  pathField.append(el("span", "", isZh ? "本机文件路径 (可选)" : "Local file path (optional)"), pathIn);

  const grid = el("div", "cv-form-grid");
  const mkField = (label, placeholder, type = "text") => {
    const f = el("label", "cv-field");
    const inp = el("input");
    inp.type = type;
    inp.placeholder = placeholder;
    f.append(el("span", "", label), inp);
    return [f, inp];
  };
  const [fModel, inModel] = mkField(isZh ? "模型 (可选)" : "Model (optional)", "claude-sonnet-4-5");
  const [fWin, inWin] = mkField(isZh ? "窗口上限 (可选)" : "Window (optional)", "200000", "number");
  const [fIn, inFresh] = mkField(isZh ? "新增 Input Token (可选)" : "Fresh input tokens", "", "number");
  const [fCache, inCache] = mkField(isZh ? "缓存命中 Cache Read (可选)" : "Cache read tokens", "", "number");
  grid.append(fModel, fWin, fIn, fCache);

  const readFile = async (file) => {
    if (!file) return;
    const text = await file.text();
    ta.value = text;
    drop.textContent = (isZh ? "已载入文件: " : "Loaded: ") + file.name + ` (${Math.round(file.size / 1024)} KB)`;
    drop.dataset.filename = file.name;
  };
  fileIn.onchange = () => readFile(fileIn.files?.[0]);
  drop.ondragover = (e) => { e.preventDefault(); drop.classList.add("over"); };
  drop.ondragleave = () => drop.classList.remove("over");
  drop.ondrop = (e) => {
    e.preventDefault();
    drop.classList.remove("over");
    readFile(e.dataTransfer?.files?.[0]);
  };

  body.append(drop, fileIn, pathField, ta, grid);

  const bar = el("div", "bar");
  const cancel = el("button", "text", t("Cancel"));
  cancel.type = "button";
  cancel.onclick = closeConfirmAsk;
  const submit = el("button", "text primary", isZh ? "解析并展示" : "Parse & Visualize");
  submit.type = "button";
  submit.onclick = async () => {
    submit.disabled = true;
    try {
      const payload = {
        raw: ta.value.trim(),
        file_path: pathIn.value.trim(),
        filename: drop.dataset.filename || "",
        model: inModel.value.trim(),
        window: Number(inWin.value) || 0,
        input: Number(inFresh.value) || 0,
        cache_read: Number(inCache.value) || 0,
      };
      const res = await api("upload", payload);
      closeConfirmAsk();
      viewerState.source = "upload";
      localStorage.setItem("ctxview.source", "upload");
      if (res.route?.id) viewerState.activeRouteId = res.route.id;
      await refreshAll();
      if (res.route?.id) await selectHeroRoute(res.route.id);
      status(isZh ? `已导入 ${res.count} 条记录` : `Imported ${res.count} records`, "ok");
    } catch (e) {
      statusErr(e);
    } finally {
      submit.disabled = false;
    }
  };
  bar.append(el("span", "grow"), cancel, submit);

  ed.append(head, body, bar);
  confirmAsk = ed;
  openModal(ed);
}

function openScanDirsModal() {
  const isZh = viewerState.lang === "zh";
  const ed = el("div", "editor");
  const head = el("div", "ehead");
  head.append(el("b", "", isZh ? "本地 Session 扫描目录配置" : "Local Session Scan Directories"));

  const body = el("div", "ebody");
  body.append(el("p", "usage-note", isZh
    ? "可添加额外目录或单个 .jsonl / .json 会话文件（支持 ~/ 路径与逗号分隔多个路径）。配置自动持久化至 data/scan_dirs.json，所有内容展示与路径均遵循脱敏规则。"
    : "Add extra folders or individual .jsonl / .json files (~ supported, comma-separated). Persisted to data/scan_dirs.json and sanitized consistently."));

  const addRow = el("div", "cv-scan-add");
  const pathInput = el("input");
  pathInput.type = "text";
  pathInput.placeholder = isZh ? "例如: ~/workspace/logs/claude 或 /tmp/session.json" : "e.g. ~/workspace/logs/claude or /tmp/session.json";
  const addBtn = el("button", "text primary", isZh ? "添加并扫描" : "Add & Scan");
  addBtn.type = "button";
  addRow.append(pathInput, addBtn);
  body.append(addRow);

  const homeLabel = el("label", "cv-scan-toggle");
  const homeCb = el("input");
  homeCb.type = "checkbox";
  homeLabel.append(homeCb, el("span", "", isZh
    ? "扫描默认用户目录（~/.claude/projects、~/.codex/sessions 与 ~/.joycode/sessions，各最多最近 200 个文件）"
    : "Scan default home directories (~/.claude/projects, ~/.codex/sessions & ~/.joycode/sessions, latest 200 each)"));
  body.append(homeLabel);

  const listsHost = el("div", "cv-scan-list");
  body.append(listsHost);

  const renderDirRow = (d, removable) => {
    const row = el("div", "cv-scan-row");
    row.append(el("span", "cv-scan-badge", d.kind || "dir"));
    const shown = d.display_path || d.path;
    const pathSpan = el("span", "cv-scan-path", shown);
    pathSpan.title = shown;
    row.append(pathSpan);
    const meta = d.exists
      ? (isZh ? `${d.files || 0} 文件` : `${d.files || 0} files`)
      : (isZh ? "路径不存在" : "missing");
    row.append(el("span", "cv-scan-meta" + (d.exists ? "" : " missing"), meta));
    if (removable) {
      const rmBtn = el("button", "text", isZh ? "移除" : "Remove");
      rmBtn.type = "button";
      rmBtn.onclick = async () => {
        rmBtn.disabled = true;
        try {
          const data = await api("scan-dirs", { action: "remove", path: d.path });
          renderData(data);
          status(isZh ? `已移除目录，当前共 ${data.local ?? 0} 条本地记录` : `Removed; ${data.local ?? 0} local records`, "ok");
          await refreshAll();
        } catch (e) {
          statusErr(e);
        } finally {
          rmBtn.disabled = false;
        }
      };
      row.append(rmBtn);
    }
    return row;
  };

  const renderData = (data) => {
    homeCb.checked = !!data?.include_home;
    listsHost.replaceChildren();

    const customSec = el("div", "cv-scan-section");
    customSec.append(el("div", "cv-scan-label", isZh
      ? "自定义目录 / 文件（全量扫描 .jsonl / .json）"
      : "Custom Directories / Files (all .jsonl / .json)"));
    const customList = data?.custom_dirs || [];
    if (customList.length === 0) {
      customSec.append(el("div", "cv-scan-empty", isZh ? "暂无自定义目录" : "No custom directories configured"));
    } else {
      for (const d of customList) customSec.append(renderDirRow(d, true));
    }
    listsHost.append(customSec);

    const defSec = el("div", "cv-scan-section");
    defSec.append(el("div", "cv-scan-label", isZh ? "默认扫描目录" : "Default Directories"));
    const defList = data?.default_dirs || [];
    if (defList.length === 0) {
      defSec.append(el("div", "cv-scan-empty", isZh ? "未检测到用户主目录" : "Home directory unavailable"));
    } else {
      for (const d of defList) defSec.append(renderDirRow(d, false));
    }
    listsHost.append(defSec);
  };

  const submitAdd = async () => {
    const raw = pathInput.value.trim();
    const paths = raw.split(/[\n,]+/).map((s) => s.trim()).filter(Boolean);
    if (paths.length === 0) {
      status(isZh ? "请输入目录或文件路径" : "Please enter a directory or file path", "warn");
      return;
    }
    addBtn.disabled = true;
    try {
      const data = await api("scan-dirs", { action: "add", paths });
      pathInput.value = "";
      renderData(data);
      status(isZh
        ? `已添加 ${data.added?.length || paths.length} 项，本地共 ${data.local ?? 0} 条记录`
        : `Added ${data.added?.length || paths.length} path(s); ${data.local ?? 0} local records`, "ok");
      await refreshAll();
    } catch (e) {
      statusErr(e);
    } finally {
      addBtn.disabled = false;
    }
  };

  addBtn.onclick = submitAdd;
  pathInput.onkeydown = (ev) => {
    if (ev.key === "Enter") {
      ev.preventDefault();
      submitAdd();
    }
  };
  homeCb.onchange = async () => {
    homeCb.disabled = true;
    try {
      const data = await api("scan-dirs", { action: "toggle_home", include_home: homeCb.checked });
      renderData(data);
      status(isZh ? `已更新默认目录设置，当前共 ${data.local ?? 0} 条本地记录` : `Updated default dirs; ${data.local ?? 0} local records`, "ok");
      await refreshAll();
    } catch (e) {
      statusErr(e);
    } finally {
      homeCb.disabled = false;
    }
  };

  const bar = el("div", "bar");
  const rescanBtn = el("button", "text", isZh ? "立即重新扫描" : "Rescan Now");
  rescanBtn.type = "button";
  rescanBtn.onclick = async () => {
    rescanBtn.disabled = true;
    try {
      const data = await api("scan-dirs", { action: "rescan" });
      renderData(data);
      status(isZh ? `重新扫描完成：${data.local ?? 0} 条本地记录` : `Rescanned: ${data.local ?? 0} local records`, "ok");
      await refreshAll();
    } catch (e) {
      statusErr(e);
    } finally {
      rescanBtn.disabled = false;
    }
  };
  const close = el("button", "text primary", t("Close"));
  close.type = "button";
  close.onclick = closeConfirmAsk;
  bar.append(rescanBtn, el("span", "grow"), close);

  ed.append(head, body, bar);
  confirmAsk = ed;
  openModal(ed);

  api("scan-dirs").then(renderData).catch(statusErr);
}

function openProxyModal() {
  const isZh = viewerState.lang === "zh";
  const ed = el("div", "editor");
  const head = el("div", "ehead");
  head.append(el("b", "", isZh ? "方案 1：本地反向代理网关接入方法" : "Reverse Proxy Gateway Setup"));

  const body = el("div", "ebody");
  const origin = location.origin;
  const snippets = [
    ["Claude Code (Anthropic Messages)", `export ANTHROPIC_BASE_URL=${origin}`],
    ["Codex / OpenAI SDK (Responses & Chat)", `export OPENAI_BASE_URL=${origin}/v1`],
    ["Gemini API", `export GEMINI_API_BASE=${origin}`],
    [isZh ? "自定义上游透传（任意目标 URL）" : "Custom upstream pass-through", `curl ${origin}/proxy/https://api.openai.com/v1/chat/completions ...`],
  ];

  body.append(el("p", "usage-note", isZh
    ? "将 Agent 或 SDK 的 Base URL 指向本服务后，所有完整请求体（包含 System Prompt、工具定义、记忆文件、文件读取结果）与响应 Usage/SSE 流都会自动解析并落盘。"
    : "Point your agent or SDK base URL here to capture full request payloads and SSE/JSON usage telemetry."));

  for (const [label, cmd] of snippets) {
    const wrap = el("div", "cv-field");
    wrap.append(el("span", "", label));
    const card = el("div", "cv-code-card");
    card.append(el("code", "", cmd), copyBtn(cmd, label));
    wrap.append(card);
    body.append(wrap);
  }

  const bar = el("div", "bar");
  const close = el("button", "text primary", t("Close"));
  close.type = "button";
  close.onclick = closeConfirmAsk;
  bar.append(el("span", "grow"), close);

  ed.append(head, body, bar);
  confirmAsk = ed;
  openModal(ed);
}

async function refreshAll() {
  await refreshRoutesList();
  await window.loadContext();
  if (viewerState.activeRouteId) {
    await selectHeroRoute(viewerState.activeRouteId);
  }
}

window.initViewer = async () => {
  viewerState.view = initialViewFromLocation();
  window.ctxViewerHost = {
    state: viewerState,
    setView: setActiveView,
    icon: typeof icon === "function" ? icon : null,
    status,
    statusErr,
    applyThemeAndLangLabels,
  };
  syncViewDom();
  applyThemeAndLangLabels();
  renderSourceTabs();

  $("#navContext")?.addEventListener("click", () => setActiveView("context"));
  $("#navSkills")?.addEventListener("click", () => setActiveView("skills"));
  window.addEventListener("hashchange", async () => {
    const h = location.hash || "";
    if (isSkillsHash(h)) {
      if (viewerState.view !== "skills") {
        await setActiveView("skills", { fromHash: true });
      } else {
        await window.SkillsWorkspace?.handleHash?.();
      }
    } else if (h === "#/context" || h.startsWith("#/context/")) {
      if (viewerState.view !== "context") {
        await setActiveView("context", { fromHash: true });
      }
    }
  });

  const btnRedact = $("#btnRedact");
  if (btnRedact) {
    btnRedact.onclick = async () => {
      viewerState.redact = !viewerState.redact;
      localStorage.setItem("ctxview.redact", viewerState.redact ? "1" : "0");
      applyThemeAndLangLabels();
      viewerState.heroCard = null;
      const isZh = viewerState.lang === "zh";
      status(isZh
        ? (viewerState.redact ? "已开启隐私脱敏展示" : "已关闭脱敏（仅建议本地调试使用）")
        : (viewerState.redact ? "Redaction enabled" : "Redaction disabled (local debug only)"),
        viewerState.redact ? "ok" : "warn");
      if (viewerState.view === "skills") {
        await window.SkillsWorkspace?.refreshOnHostChange?.();
      } else {
        await refreshAll();
      }
    };
  }
  const btnScanDirs = $("#btnScanDirs");
  if (btnScanDirs) btnScanDirs.onclick = openScanDirsModal;

  $("#btnImport").onclick = openImportModal;
  $("#btnProxyHelp").onclick = openProxyModal;
  $("#btnRescan").onclick = async () => {
    try {
      const res = await api("rescan", {});
      status(viewerState.lang === "zh" ? `已扫描本地会话：${res.local} 条记录` : `Scanned ${res.local} local turns`, "ok");
      await refreshAll();
    } catch (e) {
      statusErr(e);
    }
  };
  $("#btnLang").onclick = async () => {
    viewerState.lang = viewerState.lang === "zh" ? "en" : "zh";
    localStorage.setItem("ctxview.lang", viewerState.lang);
    setLocale(viewerState.lang);
    applyThemeAndLangLabels();
    renderSourceTabs();
    viewerState.heroCard = null;
    if (viewerState.view === "skills") {
      await window.SkillsWorkspace?.refreshOnHostChange?.();
    } else {
      await refreshAll();
    }
  };
  $("#btnTheme").onclick = () => {
    viewerState.theme = viewerState.theme === "dark" ? "light" : "dark";
    localStorage.setItem("ctxview.theme", viewerState.theme);
    document.documentElement.dataset.theme = viewerState.theme;
    applyThemeAndLangLabels();
  };

  window.SkillsWorkspace?.init?.();
  if (viewerState.view === "skills") {
    await window.SkillsWorkspace?.onActivate?.();
  } else {
    await refreshAll();
  }
};

