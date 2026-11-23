(() => {
  "use strict";

  // -------------------- Host integration helpers --------------------
  function getLang() {
    const hs = window.ctxViewerHost?.state;
    if (hs?.lang) return hs.lang;
    return localStorage.getItem("ctxview.lang") === "en" ? "en" : "zh";
  }

  function isZh() {
    return getLang() !== "en";
  }

  function t(zh, en) {
    return isZh() ? zh : en;
  }

  function getRedact() {
    const hs = window.ctxViewerHost?.state;
    if (hs && typeof hs.redact === "boolean") return hs.redact;
    return localStorage.getItem("ctxview.redact") !== "0";
  }

  function notify(msg, kind = "ok") {
    if (window.ctxViewerHost?.status) {
      window.ctxViewerHost.status(msg, kind);
      return;
    }
    const el = document.getElementById("status");
    if (el) {
      el.textContent = msg;
      el.className = "status " + (kind || "");
    }
  }

  async function copyText(text) {
    try {
      await navigator.clipboard.writeText(text);
      notify(t("已复制路径", "Copied to clipboard"), "ok");
    } catch {
      notify(t("复制失败", "Copy failed"), "warn");
    }
  }

  // -------------------- Pure DOM builder (no raw innerHTML) --------------------
  function withPreservedFocus(rootEl, fn) {
    const ae = document.activeElement;
    const focusKey = ae && rootEl && rootEl.contains(ae) ? ae.dataset?.swFocus : null;
    const selStart = focusKey && typeof ae.selectionStart === "number" ? ae.selectionStart : null;
    const selEnd = focusKey && typeof ae.selectionEnd === "number" ? ae.selectionEnd : null;
    fn();
    if (focusKey && rootEl) {
      const next = rootEl.querySelector(`[data-sw-focus="${focusKey}"]`);
      if (next) {
        next.focus({ preventScroll: true });
        if (selStart !== null && typeof next.setSelectionRange === "function") {
          try {
            next.setSelectionRange(selStart, selEnd ?? selStart);
          } catch {}
        }
      }
    }
  }

  function makeSearchInput({ focusKey, value, placeholder, onUpdate }) {
    const inp = h("input", {
      type: "search",
      className: "sw-input sw-search",
      "data-sw-focus": focusKey,
      value: value || "",
      placeholder,
    });
    inp.addEventListener("input", (e) => {
      onUpdate(e.target.value, !e.isComposing);
    });
    inp.addEventListener("compositionend", (e) => {
      onUpdate(e.target.value, true);
    });
    return inp;
  }

  function h(tag, props, ...children) {
    const node = document.createElement(tag);
    let deferredValue;
    if (props) {
      for (const [k, v] of Object.entries(props)) {
        if (k === "checked" || k === "disabled") {
          node[k] = Boolean(v);
          continue;
        }
        if (k === "value" && (tag === "input" || tag === "select" || tag === "textarea")) {
          if (v !== undefined && v !== null) deferredValue = String(v);
          continue;
        }
        if (v === undefined || v === null || v === false) continue;
        if (k === "className") {
          node.className = v;
        } else if (k === "style" && typeof v === "object") {
          Object.assign(node.style, v);
        } else if (k === "dataset" && typeof v === "object") {
          for (const [dk, dv] of Object.entries(v)) {
            if (dv !== undefined && dv !== null) node.dataset[dk] = String(dv);
          }
        } else if (k.startsWith("on") && typeof v === "function") {
          node.addEventListener(k.slice(2).toLowerCase(), v);
        } else if (v === true) {
          node.setAttribute(k, "");
        } else {
          node.setAttribute(k, String(v));
        }
      }
    }
    for (const child of children.flat(Infinity)) {
      if (child === undefined || child === null || child === false) continue;
      if (child instanceof Node) {
        node.appendChild(child);
      } else {
        node.appendChild(document.createTextNode(String(child)));
      }
    }
    if (deferredValue !== undefined) {
      node.value = deferredValue;
    }
    return node;
  }

  function renderIcon(iconName, label, large = false) {
    const cls = large ? "sw-ic lg" : "sw-ic";
    const initials = (label || "?").replace(/[^A-Za-z0-9\u4e00-\u9fa5]/g, "").slice(0, 2).toUpperCase() || "?";
    const fallback = () => h("span", { className: "sw-ic-fallback", "aria-hidden": "true" }, initials);
    if (!iconName) {
      return h("span", { className: cls }, fallback());
    }
    const safeName = encodeURIComponent(String(iconName).replace(/\.svg$/i, ""));
    const wrap = h("span", { className: cls, "aria-hidden": "true" });
    if (String(iconName).endsWith("-color")) {
      const img = h("img", {
        className: "sw-ic-img",
        src: `/icons/${safeName}.svg`,
        alt: "",
      });
      img.addEventListener("error", () => {
        wrap.replaceChildren(fallback());
      });
      wrap.appendChild(img);
    } else {
      const mask = h("span", { className: "sw-ic-mask" });
      mask.style.setProperty("--sw-ic-url", `url("/icons/${safeName}.svg")`);
      wrap.appendChild(mask);
    }
    return wrap;
  }

  // -------------------- API client --------------------
  async function apiJSON(path, options = {}) {
    const url = new URL(path, location.origin);
    if (!url.searchParams.has("redact")) {
      url.searchParams.set("redact", getRedact() ? "1" : "0");
    }
    const init = { ...options };
    if (init.body && typeof init.body !== "string") {
      init.body = JSON.stringify(init.body);
      init.headers = { "Content-Type": "application/json", ...(init.headers || {}) };
    }
    const resp = await fetch(url.pathname + url.search, init);
    const text = await resp.text();
    let data = {};
    if (text) {
      try {
        data = JSON.parse(text);
      } catch {
        data = { error: text };
      }
    }
    if (!resp.ok) {
      const err = new Error(data.error || `HTTP ${resp.status}`);
      err.status = resp.status;
      err.code = data.code || "";
      err.conflicts = data.conflicts || [];
      throw err;
    }
    return data;
  }

  // -------------------- Safe Markdown + Frontmatter + LCS Diff --------------------
  function escHtml(s) {
    return String(s)
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;")
      .replace(/'/g, "&#39;");
  }

  function safeUrl(raw) {
    const trimmed = String(raw || "").trim();
    try {
      const u = new URL(trimmed);
      if (u.protocol === "http:" || u.protocol === "https:") return u.href;
    } catch {
      return null;
    }
    return null;
  }

  function splitFrontmatterAndBody(md) {
    const src = String(md || "").replace(/\r\n/g, "\n");
    if (!src.startsWith("---\n")) {
      return { meta: [], body: src };
    }
    const endIdx = src.indexOf("\n---\n", 4);
    if (endIdx < 0) {
      return { meta: [], body: src };
    }
    const fmRaw = src.slice(4, endIdx);
    const body = src.slice(endIdx + 5);
    const meta = [];
    for (const line of fmRaw.split("\n")) {
      const m = line.match(/^([A-Za-z0-9_-]+)\s*:\s*(.*)$/);
      if (m) meta.push({ key: m[1], val: m[2].trim() });
    }
    return { meta, body };
  }

  function tokenizeInline(line) {
    const tokens = [];
    const re = /(`[^`]+`)|(\[[^\]]+\]\([^)]+\))|(\*\*[^*]+\*\*)|(\*[^*\s][^*]*\*)/g;
    let last = 0;
    let m;
    while ((m = re.exec(line)) !== null) {
      if (m.index > last) tokens.push({ kind: "text", text: line.slice(last, m.index) });
      const tok = m[0];
      if (m[1]) {
        tokens.push({ kind: "code", text: tok.slice(1, -1) });
      } else if (m[2]) {
        const inner = tok.match(/^\[([^\]]+)\]\(([^)]+)\)$/);
        if (inner) {
          const href = safeUrl(inner[2]);
          if (href) {
            tokens.push({ kind: "link", text: inner[1], href });
          } else {
            tokens.push({ kind: "text", text: `${inner[1]} (${inner[2]})` });
          }
        }
      } else if (m[3]) {
        tokens.push({ kind: "strong", text: tok.slice(2, -2) });
      } else if (m[4]) {
        tokens.push({ kind: "em", text: tok.slice(1, -1) });
      }
      last = re.lastIndex;
    }
    if (last < line.length) tokens.push({ kind: "text", text: line.slice(last) });
    return tokens;
  }

  function parseMarkdownBlocks(body) {
    const lines = String(body || "").replace(/\r\n/g, "\n").split("\n");
    const blocks = [];
    let i = 0;
    while (i < lines.length) {
      const line = lines[i];
      if (line.trim() === "") {
        i++;
        continue;
      }
      const fence = line.match(/^```(.*)$/);
      if (fence) {
        const lang = fence[1].trim();
        const codeLines = [];
        i++;
        while (i < lines.length && !lines[i].startsWith("```")) {
          codeLines.push(lines[i]);
          i++;
        }
        if (i < lines.length && lines[i].startsWith("```")) i++;
        blocks.push({ type: "code", lang, text: codeLines.join("\n") });
        continue;
      }
      const heading = line.match(/^(#{1,6})\s+(.*)$/);
      if (heading) {
        blocks.push({ type: "heading", level: heading[1].length, text: heading[2].trim() });
        i++;
        continue;
      }
      if (/^(-{3,}|\*{3,})$/.test(line.trim())) {
        blocks.push({ type: "hr" });
        i++;
        continue;
      }
      if (line.startsWith(">")) {
        const qLines = [];
        while (i < lines.length && lines[i].startsWith(">")) {
          qLines.push(lines[i].replace(/^>\s?/, ""));
          i++;
        }
        blocks.push({ type: "quote", text: qLines.join("\n") });
        continue;
      }
      if (/^\s*[-*]\s+/.test(line)) {
        const items = [];
        while (i < lines.length && /^\s*[-*]\s+/.test(lines[i])) {
          items.push(lines[i].replace(/^\s*[-*]\s+/, ""));
          i++;
        }
        blocks.push({ type: "ul", items });
        continue;
      }
      if (/^\s*\d+\.\s+/.test(line)) {
        const items = [];
        while (i < lines.length && /^\s*\d+\.\s+/.test(lines[i])) {
          items.push(lines[i].replace(/^\s*\d+\.\s+/, ""));
          i++;
        }
        blocks.push({ type: "ol", items });
        continue;
      }
      const pLines = [];
      while (
        i < lines.length &&
        lines[i].trim() !== "" &&
        !/^(```|#{1,6}\s|>|\s*[-*]\s+|\s*\d+\.\s+|(-{3,}|\*{3,})$)/.test(lines[i])
      ) {
        pLines.push(lines[i]);
        i++;
      }
      blocks.push({ type: "p", text: pLines.join(" ") });
    }
    return blocks;
  }

  function inlineToSafeHtml(text) {
    return tokenizeInline(text)
      .map((tok) => {
        if (tok.kind === "code") return `<code>${escHtml(tok.text)}</code>`;
        if (tok.kind === "strong") return `<strong>${escHtml(tok.text)}</strong>`;
        if (tok.kind === "em") return `<em>${escHtml(tok.text)}</em>`;
        if (tok.kind === "link") {
          return `<a href="${escHtml(tok.href)}" target="_blank" rel="noopener noreferrer">${escHtml(tok.text)}</a>`;
        }
        return escHtml(tok.text);
      })
      .join("");
  }

  function inlineToNodes(text) {
    return tokenizeInline(text).map((tok) => {
      if (tok.kind === "code") return h("code", null, tok.text);
      if (tok.kind === "strong") return h("strong", null, tok.text);
      if (tok.kind === "em") return h("em", null, tok.text);
      if (tok.kind === "link") {
        return h("a", { href: tok.href, target: "_blank", rel: "noopener noreferrer" }, tok.text);
      }
      return document.createTextNode(tok.text);
    });
  }

  // Adheres strictly to modern-web-guidance (sanitize-untrusted-html):
  // uses Element.prototype.setHTML when available, otherwise safe DOM creation — never raw innerHTML.
  function renderMarkdownDocument(container, mdText) {
    const { meta, body } = splitFrontmatterAndBody(mdText);
    const frag = document.createDocumentFragment();
    if (meta.length > 0) {
      const fmBox = h(
        "div",
        { className: "sw-frontmatter" },
        meta.map((entry) =>
          h("div", null, h("span", { className: "sw-fm-k" }, entry.key + ":"), h("span", null, entry.val || "—"))
        )
      );
      frag.appendChild(fmBox);
    }

    const mdEl = h("div", { className: "sw-md" });
    const blocks = parseMarkdownBlocks(body);
    if ("setHTML" in Element.prototype) {
      const html = blocks
        .map((blk) => {
          if (blk.type === "heading") return `<h${blk.level}>${inlineToSafeHtml(blk.text)}</h${blk.level}>`;
          if (blk.type === "code") return `<pre><code>${escHtml(blk.text)}</code></pre>`;
          if (blk.type === "hr") return `<hr />`;
          if (blk.type === "quote") return `<blockquote>${inlineToSafeHtml(blk.text)}</blockquote>`;
          if (blk.type === "ul") return `<ul>${blk.items.map((it) => `<li>${inlineToSafeHtml(it)}</li>`).join("")}</ul>`;
          if (blk.type === "ol") return `<ol>${blk.items.map((it) => `<li>${inlineToSafeHtml(it)}</li>`).join("")}</ol>`;
          return `<p>${inlineToSafeHtml(blk.text)}</p>`;
        })
        .join("\n");
      mdEl.setHTML(html);
    } else {
      for (const blk of blocks) {
        if (blk.type === "heading") mdEl.appendChild(h(`h${blk.level}`, null, inlineToNodes(blk.text)));
        else if (blk.type === "code") mdEl.appendChild(h("pre", null, h("code", null, blk.text)));
        else if (blk.type === "hr") mdEl.appendChild(h("hr"));
        else if (blk.type === "quote") mdEl.appendChild(h("blockquote", null, inlineToNodes(blk.text)));
        else if (blk.type === "ul") mdEl.appendChild(h("ul", null, blk.items.map((it) => h("li", null, inlineToNodes(it)))));
        else if (blk.type === "ol") mdEl.appendChild(h("ol", null, blk.items.map((it) => h("li", null, inlineToNodes(it)))));
        else mdEl.appendChild(h("p", null, inlineToNodes(blk.text)));
      }
    }
    frag.appendChild(mdEl);
    container.replaceChildren(frag);
  }

  function computeLineDiff(localText, centerText) {
    const a = String(localText || "").replace(/\r\n/g, "\n").split("\n");
    const b = String(centerText || "").replace(/\r\n/g, "\n").split("\n");
    const n = a.length;
    const m = b.length;
    if (n * m > 400000) {
      const out = [];
      for (let i = 0; i < n; i++) out.push({ kind: "del", la: i + 1, lb: "", text: a[i] });
      for (let j = 0; j < m; j++) out.push({ kind: "add", la: "", lb: j + 1, text: b[j] });
      return out;
    }
    const dp = Array.from({ length: n + 1 }, () => new Int32Array(m + 1));
    for (let i = n - 1; i >= 0; i--) {
      for (let j = m - 1; j >= 0; j--) {
        if (a[i] === b[j]) dp[i][j] = dp[i + 1][j + 1] + 1;
        else dp[i][j] = dp[i + 1][j] >= dp[i][j + 1] ? dp[i + 1][j] : dp[i][j + 1];
      }
    }
    const rows = [];
    let i = 0;
    let j = 0;
    while (i < n && j < m) {
      if (a[i] === b[j]) {
        rows.push({ kind: "eq", la: i + 1, lb: j + 1, text: a[i] });
        i++;
        j++;
      } else if (dp[i + 1][j] >= dp[i][j + 1]) {
        rows.push({ kind: "del", la: i + 1, lb: "", text: a[i] });
        i++;
      } else {
        rows.push({ kind: "add", la: "", lb: j + 1, text: b[j] });
        j++;
      }
    }
    while (i < n) {
      rows.push({ kind: "del", la: i + 1, lb: "", text: a[i] });
      i++;
    }
    while (j < m) {
      rows.push({ kind: "add", la: "", lb: j + 1, text: b[j] });
      j++;
    }
    return rows;
  }

  // -------------------- Dialog helpers (modern-web-guidance: dialog) --------------------
  function setupNativeDialog(dlg, { resetOnClose = true } = {}) {
    if (!dlg || dlg.dataset.swInit) return;
    dlg.dataset.swInit = "1";
    if (resetOnClose) {
      dlg.addEventListener("close", () => {
        dlg.returnValue = "";
      });
    }
    if (!("closedBy" in HTMLDialogElement.prototype)) {
      let downOutside = false;
      dlg.addEventListener("pointerdown", (e) => {
        if (e.target !== dlg) {
          downOutside = false;
          return;
        }
        const r = dlg.getBoundingClientRect();
        downOutside = e.clientX < r.left || e.clientX > r.right || e.clientY < r.top || e.clientY > r.bottom;
      });
      dlg.addEventListener("pointerup", (e) => {
        if (!downOutside) return;
        downOutside = false;
        const r = dlg.getBoundingClientRect();
        const outside = e.clientX < r.left || e.clientX > r.right || e.clientY < r.top || e.clientY > r.bottom;
        if (outside && dlg.open) dlg.close("backdrop");
      });
    }
  }

  function showConfirm({ title, message, confirmLabel, danger = true }) {
    return new Promise((resolve) => {
      const dlg = document.getElementById("skillConfirmDialog");
      if (!dlg) return resolve(false);
      setupNativeDialog(dlg, { resetOnClose: false });

      let settled = false;
      const finish = (val) => {
        if (settled) return;
        settled = true;
        dlg.removeEventListener("close", onClose);
        resolve(val);
      };
      const onClose = () => {
        const ok = dlg.returnValue === "confirm";
        dlg.returnValue = "";
        finish(ok);
      };
      dlg.addEventListener("close", onClose);

      const content = h(
        "form",
        { method: "dialog" },
        h(
          "div",
          { className: "sw-dialog-header" },
          h("h2", { id: "skillConfirmTitle", className: "sw-dialog-title" }, title)
        ),
        h("div", { className: "sw-dialog-body" }, h("p", { style: { margin: 0, lineHeight: "1.5" } }, message)),
        h(
          "div",
          { className: "sw-dialog-footer" },
          h("span"),
          h(
            "div",
            { className: "sw-actions" },
            h(
              "button",
              { type: "submit", value: "cancel", className: "text", autofocus: true },
              t("取消", "Cancel")
            ),
            h(
              "button",
              { type: "submit", value: "confirm", className: danger ? "text sw-btn-danger" : "text primary" },
              confirmLabel || t("确认", "Confirm")
            )
          )
        )
      );
      dlg.replaceChildren(content);
      dlg.returnValue = "";
      dlg.showModal();
    });
  }

  // -------------------- Workspace State --------------------
  const state = {
    initialized: false,
    loadedOnce: false,
    loading: false,
    routeKind: "global", // "global" | "lobster" | "projects" | "project" | "library"
    selectedAgentKey: null,
    selectedProjectId: null,
    overview: { agents: [], sync_mode: "symlink", central_dir: "", tool_order: [] },
    centerSkills: [],
    presets: [],
    projects: [],
    agentSkills: [],
    agentLoading: false,
    projectSkills: [],
    projectTargets: [],
    projectLoading: false,
    searchAgent: "",
    searchSkill: "",
    statusFilter: "all",
    tagFilter: "all",
    projectFilterMode: "all", // "all" | "enabled" | "disabled"
    projectStatusFilter: "all",
    projectTagFilter: "all",
    projectSearch: "",
    viewMode: localStorage.getItem("ctxview.skills.viewMode") || "grid",
    collapseCoding: false,
    collapseLobster: false,
    collapseProjects: false,
    selectedSkillRelPaths: new Set(),
    projectSelectedGroupIds: new Set(),
    lastConflict: null,
    librarySearch: "",
    libraryTag: "all",
  };

  function agentMap() {
    const m = new Map();
    for (const a of state.overview.agents || []) m.set(a.key, a);
    return m;
  }

  function projectMap() {
    const m = new Map();
    for (const p of state.projects || []) m.set(p.id, p);
    return m;
  }

  function centerMap() {
    const m = new Map();
    for (const s of state.centerSkills || []) m.set(s.id, s);
    return m;
  }

  function orderedAgents(category) {
    const orderIdx = new Map();
    (state.overview.tool_order || []).forEach((k, i) => orderIdx.set(k, i));
    const list = (state.overview.agents || []).filter((a) => a.category === category);
    return list.sort((a, b) => {
      const oa = orderIdx.has(a.key) ? orderIdx.get(a.key) : 99999;
      const ob = orderIdx.has(b.key) ? orderIdx.get(b.key) : 99999;
      if (oa !== ob) return oa - ob;
      if (a.installed !== b.installed) return a.installed ? -1 : 1;
      return 0;
    });
  }

  function activeAgentsForCategory(category) {
    return orderedAgents(category).filter((a) => a.enabled && a.installed);
  }

  async function handleRouteChange() {
    const prevAgent = state.selectedAgentKey;
    const prevProject = state.selectedProjectId;
    const prevKind = state.routeKind;
    parseHashIntoState();
    if (state.selectedAgentKey && (state.selectedAgentKey !== prevAgent || state.routeKind !== prevKind)) {
      state.statusFilter = "all";
      state.tagFilter = "all";
      state.searchSkill = "";
      state.lastConflict = null;
      await loadAgentSkills(state.selectedAgentKey, true);
    } else if (state.selectedProjectId && (state.selectedProjectId !== prevProject || state.routeKind !== prevKind)) {
      state.projectFilterMode = "all";
      state.projectStatusFilter = "all";
      state.projectTagFilter = "all";
      state.projectSearch = "";
      state.lastConflict = null;
      await loadProjectData(state.selectedProjectId, true);
    } else {
      render();
    }
  }

  function navigateTo(hash, replace = false) {
    const targetHash = hash.startsWith("#") ? hash : "#" + hash;
    if (location.hash !== targetHash) {
      if (replace) {
        history.replaceState(null, "", targetHash);
        void handleRouteChange();
      } else {
        location.hash = targetHash;
      }
      return;
    }
    void handleRouteChange();
  }

  function parseHashIntoState() {
    const raw = location.hash || "#/global-workspace";
    state.selectedAgentKey = null;
    state.selectedProjectId = null;
    if (raw.startsWith("#/lobster-workspace/")) {
      state.routeKind = "lobster";
      state.selectedAgentKey = decodeURIComponent(raw.slice("#/lobster-workspace/".length)) || null;
    } else if (raw === "#/lobster-workspace") {
      state.routeKind = "lobster";
    } else if (raw.startsWith("#/project/")) {
      state.routeKind = "project";
      state.selectedProjectId = decodeURIComponent(raw.slice("#/project/".length)) || null;
    } else if (raw === "#/projects" || raw.startsWith("#/projects/")) {
      state.routeKind = "projects";
    } else if (raw === "#/skills-library") {
      state.routeKind = "library";
    } else if (raw.startsWith("#/global-workspace/")) {
      state.routeKind = "global";
      state.selectedAgentKey = decodeURIComponent(raw.slice("#/global-workspace/".length)) || null;
    } else {
      state.routeKind = "global";
    }
  }

  // -------------------- Data Refresh --------------------
  async function loadAll(preserveSelection = true) {
    state.loading = true;
    try {
      const [overview, centerResp, presetsResp, projectsResp] = await Promise.all([
        apiJSON("/api/skills/agents"),
        apiJSON("/api/skills/center"),
        apiJSON("/api/skills/presets"),
        apiJSON("/api/skills/projects"),
      ]);
      state.overview = overview || { agents: [], sync_mode: "symlink", central_dir: "", tool_order: [] };
      state.centerSkills = centerResp?.skills || [];
      state.presets = presetsResp?.presets || [];
      state.projects = projectsResp?.projects || [];
      state.loadedOnce = true;

      if (state.selectedAgentKey) {
        const found = agentMap().get(state.selectedAgentKey);
        if (found) {
          await loadAgentSkills(state.selectedAgentKey, !preserveSelection);
        } else {
          state.selectedAgentKey = null;
          state.agentSkills = [];
        }
      }
      if (state.selectedProjectId) {
        const foundProj = projectMap().get(state.selectedProjectId);
        if (foundProj) {
          await loadProjectData(state.selectedProjectId, !preserveSelection);
        } else {
          state.selectedProjectId = null;
          state.projectSkills = [];
          state.projectTargets = [];
        }
      }
    } catch (err) {
      notify(err.message || t("加载工作区失败", "Failed to load workspace"), "err");
    } finally {
      state.loading = false;
      render();
    }
  }

  async function loadAgentSkills(agentKey, clearSelection = true) {
    state.agentLoading = true;
    if (clearSelection) state.selectedSkillRelPaths.clear();
    render();
    try {
      const resp = await apiJSON(`/api/skills/agents/${encodeURIComponent(agentKey)}/skills`);
      state.agentSkills = resp?.skills || [];
      const existing = new Set(state.agentSkills.map((s) => s.relative_path));
      for (const rel of Array.from(state.selectedSkillRelPaths)) {
        if (!existing.has(rel)) state.selectedSkillRelPaths.delete(rel);
      }
    } catch (err) {
      state.agentSkills = [];
      notify(err.message || t("读取 Agent 技能失败", "Failed to load agent skills"), "err");
    } finally {
      state.agentLoading = false;
      render();
    }
  }

  function buildProjectSkillGroups(skills) {
    const priority = ["diverged", "project_newer", "center_newer", "project_only", "in_sync"];
    const groups = new Map();
    for (const sk of skills || []) {
      const key = (sk.relative_path || "").toLowerCase();
      let g = groups.get(key);
      if (!g) {
        g = {
          id: key,
          name: sk.name,
          dir_name: sk.dir_name,
          relative_path: sk.relative_path,
          description: sk.description || "",
          files: [...(sk.files || [])],
          variants: [sk],
          enabledCount: sk.enabled ? 1 : 0,
          totalCount: 1,
          tags: [...(sk.tags || [])],
          centerSkillIds: sk.center_skill_id ? [sk.center_skill_id] : [],
        };
        groups.set(key, g);
      } else {
        g.variants.push(sk);
        g.enabledCount += sk.enabled ? 1 : 0;
        g.totalCount += 1;
        g.files = Array.from(new Set([...g.files, ...(sk.files || [])])).sort();
        g.tags = Array.from(new Set([...g.tags, ...(sk.tags || [])])).sort((a, b) => a.localeCompare(b));
        if (sk.center_skill_id && !g.centerSkillIds.includes(sk.center_skill_id)) {
          g.centerSkillIds.push(sk.center_skill_id);
          g.centerSkillIds.sort();
        }
        if (!g.description && sk.description) g.description = sk.description;
      }
    }
    return Array.from(groups.values())
      .map((g) => {
        const sortedVariants = [...g.variants].sort((a, b) =>
          (a.agent_display_name || a.agent).localeCompare(b.agent_display_name || b.agent)
        );
        let status = "project_only";
        for (const p of priority) {
          if (sortedVariants.some((v) => v.sync_status === p)) {
            status = p;
            break;
          }
        }
        return {
          ...g,
          variants: sortedVariants,
          primaryVariant: sortedVariants[0],
          status,
        };
      })
      .sort((a, b) => a.name.toLowerCase().localeCompare(b.name.toLowerCase()));
  }

  async function loadProjectData(projectID, clearSelection = true) {
    state.projectLoading = true;
    if (clearSelection) state.projectSelectedGroupIds.clear();
    render();
    try {
      const [skillsResp, targetsResp] = await Promise.all([
        apiJSON(`/api/skills/projects/${encodeURIComponent(projectID)}/skills`),
        apiJSON(`/api/skills/projects/${encodeURIComponent(projectID)}/targets`),
      ]);
      state.projectSkills = skillsResp?.skills || [];
      state.projectTargets = targetsResp?.targets || [];
      const validIds = new Set(buildProjectSkillGroups(state.projectSkills).map((g) => g.id));
      for (const gid of Array.from(state.projectSelectedGroupIds)) {
        if (!validIds.has(gid)) state.projectSelectedGroupIds.delete(gid);
      }
    } catch (err) {
      state.projectSkills = [];
      state.projectTargets = [];
      notify(err.message || t("读取项目工作区失败", "Failed to load project workspace"), "err");
    } finally {
      state.projectLoading = false;
      render();
    }
  }

  // -------------------- Status helpers --------------------
  function statusMeta(syncStatus) {
    switch (syncStatus) {
      case "in_sync":
        return { label: t("已同步", "In Sync"), cls: "in-sync" };
      case "center_newer":
        return { label: t("中心有更新", "Center Newer"), cls: "center-newer" };
      case "project_newer":
        return { label: t("本地有更新", "Local Newer"), cls: "local-newer" };
      case "diverged":
        return { label: t("已分歧", "Diverged"), cls: "diverged" };
      default:
        return { label: t("仅本地", "Local Only"), cls: "project-only" };
    }
  }

  function formatRelTime(ms) {
    if (!ms) return "";
    try {
      return new Date(ms).toLocaleString(isZh() ? "zh-CN" : "en-US", {
        month: "short",
        day: "numeric",
        hour: "2-digit",
        minute: "2-digit",
      });
    } catch {
      return "";
    }
  }

  // -------------------- Action Handlers --------------------
  async function handleImportLocalSkill(agentKey, relPath) {
    try {
      const res = await apiJSON(`/api/skills/agents/${encodeURIComponent(agentKey)}/import`, {
        method: "POST",
        body: { rel_path: relPath },
      });
      notify(t(`已导入并纳管: ${res.name || relPath}`, `Imported to library: ${res.name || relPath}`), "ok");
      await loadAll(false);
    } catch (err) {
      notify(err.message, "err");
    }
  }

  async function handlePullLocalSkill(agentKey, skill) {
    if (skill.sync_status === "diverged") {
      const ok = await showConfirm({
        title: t("覆盖本地已修改内容？", "Overwrite diverged local skill?"),
        message: t(
          `将用中央仓库版本覆盖 ${skill.name} (${skill.relative_path}) 的本地内容，未导入中央的本地修改将丢失。`,
          `This will overwrite local changes in ${skill.name} (${skill.relative_path}) with the Central Library copy.`
        ),
        confirmLabel: t("从中央覆盖", "Overwrite Local"),
        danger: true,
      });
      if (!ok) return;
    }
    try {
      await apiJSON(`/api/skills/agents/${encodeURIComponent(agentKey)}/pull`, {
        method: "POST",
        body: { rel_path: skill.relative_path },
      });
      notify(t(`已更新本地技能: ${skill.name}`, `Updated from library: ${skill.name}`), "ok");
      await loadAll(false);
    } catch (err) {
      notify(err.message, "err");
    }
  }

  async function handleUnsyncSkills(skillIDs, agentKeys) {
    if (!skillIDs.length || !agentKeys.length) return;
    try {
      const res = await apiJSON("/api/skills/unsync", {
        method: "POST",
        body: { skill_ids: skillIDs, agents: agentKeys },
      });
      notify(
        t(`已移出 ${res.applied} 项同步部署（保留中央仓库副本）`, `Removed ${res.applied} deployment(s) (central copy kept)`),
        "ok"
      );
      await loadAll(false);
    } catch (err) {
      notify(err.message, "err");
    }
  }

  async function handleDeleteLocalSkills(agentKey, relPaths, displayName) {
    if (!relPaths.length) return;
    const ok = await showConfirm({
      title: t("永久删除本地技能？", "Permanently delete local skill(s)?"),
      message:
        relPaths.length === 1
          ? t(
              `将从磁盘永久删除未纳管的本地技能 "${displayName || relPaths[0]}"，此操作不可恢复。`,
              `This permanently removes unmanaged local skill "${displayName || relPaths[0]}" from disk.`
            )
          : t(
              `将永久删除 ${relPaths.length} 个未纳管的本地技能目录，此操作不可恢复。`,
              `This permanently deletes ${relPaths.length} unmanaged local skill directories from disk.`
            ),
      confirmLabel: t("永久删除", "Delete Permanently"),
      danger: true,
    });
    if (!ok) return;
    let deleted = 0;
    let firstErr = "";
    for (const rp of relPaths) {
      try {
        await apiJSON(`/api/skills/agents/${encodeURIComponent(agentKey)}/delete-local`, {
          method: "POST",
          body: { rel_path: rp },
        });
        deleted++;
      } catch (err) {
        if (!firstErr) firstErr = err.message;
      }
    }
    if (firstErr) notify(t(`已删除 ${deleted} 项；部分失败: ${firstErr}`, `Deleted ${deleted}; error: ${firstErr}`), "warn");
    else notify(t(`已删除 ${deleted} 个本地技能`, `Deleted ${deleted} local skill(s)`), "ok");
    await loadAll(false);
  }

  async function handleBatchSync(skillIDs, agentKeys, { showConflictBanner = true } = {}) {
    state.lastConflict = null;
    try {
      const res = await apiJSON("/api/skills/sync", {
        method: "POST",
        body: { skill_ids: skillIDs, agents: agentKeys },
      });
      if (!res.ok) {
        state.lastConflict = res;
        notify(
          t(
            `已同步 ${res.applied} 项，跳过 ${res.skipped} 项，冲突 ${res.conflicts?.length || 0} 项，失败 ${res.failed || 0} 项`,
            `Synced ${res.applied}, skipped ${res.skipped}, conflicts ${res.conflicts?.length || 0}, failed ${res.failed || 0}`
          ),
          "warn"
        );
      } else {
        notify(
          t(`同步完成：新增/更新 ${res.applied} 项，已最新跳过 ${res.skipped} 项`, `Synced ${res.applied}, ${res.skipped} already up-to-date`),
          "ok"
        );
      }
      await loadAll(false);
      return res;
    } catch (err) {
      if (showConflictBanner && err.conflicts?.length) {
        state.lastConflict = { ok: false, conflicts: err.conflicts, failures: [{ error: err.message }] };
        render();
      }
      notify(err.message, "err");
      throw err;
    }
  }

  async function handlePresetClick(preset, scopedAgentKey) {
    if (!preset.skill_ids?.length) {
      openPresetDialog(preset);
      return;
    }
    if (scopedAgentKey) {
      // Check how many preset skills are currently managed/present on this agent
      const deployedIds = new Set(
        state.agentSkills.filter((s) => s.managed && s.center_skill_id).map((s) => s.center_skill_id)
      );
      const allApplied = preset.skill_ids.every((sid) => deployedIds.has(sid));
      if (allApplied) {
        const ok = await showConfirm({
          title: t(`从当前 Agent 移出 Preset "${preset.name}"？`, `Remove preset "${preset.name}" from this agent?`),
          message: t(
            `将从当前 Agent 移出 ${preset.skill_ids.length} 个由本 Preset 纳管的技能（中央仓库副本会保留）。`,
            `This unsyncs ${preset.skill_ids.length} skill(s) in this preset from this agent.`
          ),
          confirmLabel: t("移出同步", "Remove Preset"),
          danger: false,
        });
        if (!ok) return;
        await handleUnsyncSkills(preset.skill_ids, [scopedAgentKey]);
      } else {
        await handleBatchSync(preset.skill_ids, [scopedAgentKey]);
      }
      return;
    }

    // Overview level: apply preset to all enabled & installed agents of current category
    const category = state.routeKind === "lobster" ? "lobster" : "coding";
    const agents = activeAgentsForCategory(category);
    if (!agents.length) {
      notify(t("当前分类下没有已安装且启用的 Agent", "No installed & enabled agents in this category"), "warn");
      return;
    }
    const ok = await showConfirm({
      title: t(`应用 Preset "${preset.name}"？`, `Apply preset "${preset.name}"?`),
      message: t(
        `将把 Preset 中的 ${preset.skill_ids.length} 个技能同步部署到 ${agents.length} 个已启用的 Agent 全局目录。`,
        `Sync ${preset.skill_ids.length} skill(s) to ${agents.length} active agent(s).`
      ),
      confirmLabel: t("应用到全部已启用 Agent", "Apply to Active Agents"),
      danger: false,
    });
    if (!ok) return;
    await handleBatchSync(preset.skill_ids, agents.map((a) => a.key));
  }

  // -------------------- Dialogs --------------------
  function openSkillDetailDialog(opts) {
    // opts: { agentKey, skill } OR { centerSkill }
    const dlg = document.getElementById("skillDialog");
    if (!dlg) return;
    setupNativeDialog(dlg);

    const isCenterMode = Boolean(opts.centerSkill && !opts.skill);
    const agentSkill = opts.skill || null;
    const centerSkill = opts.centerSkill || (agentSkill?.center_skill_id ? centerMap().get(agentSkill.center_skill_id) : null);
    const hasCenter = Boolean(centerSkill);
    const showDiffAndCenter = !isCenterMode && hasCenter;

    let activeTab = isCenterMode ? "center" : "local";
    let rawMode = false;
    let localDoc = null;
    let centerDoc = null;
    let localErr = "";
    let centerErr = "";

    const bodyMount = h("div", { className: "sw-dialog-body", style: { padding: 0 } });
    const footerMount = h("div", { className: "sw-dialog-footer" });

    const tabButtons = {};
    const panels = {};

    function setTab(nextTab) {
      activeTab = nextTab;
      for (const [k, btn] of Object.entries(tabButtons)) {
        const selected = k === activeTab;
        btn.setAttribute("aria-selected", selected ? "true" : "false");
        if (!("focusGroup" in HTMLElement.prototype)) {
          btn.setAttribute("tabindex", selected ? "0" : "-1");
        }
      }
      for (const [k, panel] of Object.entries(panels)) {
        if (k === activeTab) {
          panel.removeAttribute("hidden");
        } else {
          // Adheres to modern-web-guidance (tabs): hidden="until-found" preserves in-page findability
          panel.setAttribute("hidden", "until-found");
        }
      }
      renderTabContents();
    }

    function renderDocInto(targetInner, doc, errMsg, fallbackLabel) {
      if (errMsg) {
        targetInner.replaceChildren(h("div", { className: "sw-empty" }, errMsg));
        return;
      }
      if (!doc) {
        targetInner.replaceChildren(h("div", { className: "sw-empty" }, t("正在加载文档…", "Loading document...")));
        return;
      }
      const headerRow = h(
        "div",
        { className: "sw-row", style: { justifyContent: "space-between" } },
        h("span", { className: "sw-path-chip" }, doc.filename || fallbackLabel || "SKILL.md"),
        h(
          "button",
          {
            type: "button",
            className: rawMode ? "text on" : "text",
            onClick: () => {
              rawMode = !rawMode;
              renderTabContents();
            },
          },
          rawMode ? t("切换到渲染视图", "Rendered View") : t("查看源码", "Raw Source")
        )
      );
      const viewBox = h("div");
      if (rawMode) {
        viewBox.replaceChildren(h("pre", { className: "sw-raw-pre" }, doc.content || ""));
      } else {
        renderMarkdownDocument(viewBox, doc.content || "");
      }
      targetInner.replaceChildren(headerRow, viewBox);
    }

    function renderDiffInto(targetInner) {
      if (localErr || centerErr) {
        targetInner.replaceChildren(h("div", { className: "sw-empty" }, localErr || centerErr));
        return;
      }
      if (!localDoc || !centerDoc) {
        targetInner.replaceChildren(h("div", { className: "sw-empty" }, t("正在加载对比内容…", "Loading comparison...")));
        return;
      }
      if ((localDoc.content || "") === (centerDoc.content || "")) {
        targetInner.replaceChildren(
          h("div", { className: "sw-empty" }, t("本地 SKILL.md 与中央仓库内容完全一致。", "Local and Central SKILL.md are identical."))
        );
        return;
      }
      const rows = computeLineDiff(localDoc.content, centerDoc.content);
      const diffEl = h(
        "div",
        { className: "sw-diff" },
        rows.map((r) =>
          h(
            "div",
            { className: `sw-diff-line ${r.kind}` },
            h("span", { className: "sw-diff-ln" }, r.la),
            h("span", { className: "sw-diff-ln" }, r.lb),
            h("span", null, r.kind === "add" ? "+" : r.kind === "del" ? "-" : " "),
            h("span", null, r.text)
          )
        )
      );
      targetInner.replaceChildren(
        h("div", { className: "sw-row", style: { fontSize: "12px", color: "var(--muted)" } }, t("红色 = 仅本地 / 绿色 = 仅中央仓库", "Red = Local / Green = Center")),
        diffEl
      );
    }

    function renderTabContents() {
      if (panels.local) renderDocInto(panels.local.firstChild, localDoc, localErr, "Local SKILL.md");
      if (panels.diff) renderDiffInto(panels.diff.firstChild);
      if (panels.center) renderDocInto(panels.center.firstChild, centerDoc, centerErr, "Center SKILL.md");
    }

    const titleText = agentSkill?.name || centerSkill?.name || "Skill";
    const metaBadges = [];
    if (agentSkill) {
      const sm = statusMeta(agentSkill.sync_status);
      metaBadges.push(h("span", { className: `sw-badge ${sm.cls}` }, sm.label));
      metaBadges.push(h("span", { className: "sw-path-chip", title: agentSkill.path }, agentSkill.path));
      if (agentSkill.is_symlink && agentSkill.link_target) {
        metaBadges.push(h("span", { className: "sw-badge no-dot accent" }, `symlink → ${agentSkill.link_target}`));
      }
    } else if (centerSkill) {
      metaBadges.push(h("span", { className: "sw-badge no-dot ok" }, t("中央仓库", "Central Library")));
      metaBadges.push(h("span", { className: "sw-path-chip" }, centerSkill.central_path));
    }

    const header = h(
      "div",
      { className: "sw-dialog-header" },
      h(
        "div",
        { style: { minWidth: 0 } },
        h("h2", { id: "skillDialogTitle", className: "sw-dialog-title" }, titleText),
        h("div", { className: "sw-dialog-sub" }, metaBadges)
      ),
      h(
        "form",
        { method: "dialog" },
        h("button", { type: "submit", className: "text", "aria-label": t("关闭", "Close") }, "✕")
      )
    );

    const tabsSpec = [];
    if (!isCenterMode) {
      tabsSpec.push({ id: "local", label: t("本地文档", "Local Doc") });
    }
    if (showDiffAndCenter) {
      tabsSpec.push({ id: "diff", label: t("版本对比", "Diff") });
      tabsSpec.push({ id: "center", label: t("中央文档", "Center Doc") });
    }
    if (isCenterMode) {
      tabsSpec.push({ id: "center", label: t("中央文档", "Center Doc") });
    }

    const tablist = h("div", {
      className: "sw-tablist",
      role: "tablist",
      focusgroup: "tablist nomemory",
      "aria-label": t("技能文档视图", "Skill Document Views"),
    });

    tabsSpec.forEach((spec, idx) => {
      const isSelected = spec.id === activeTab;
      const btn = h(
        "button",
        {
          type: "button",
          className: "sw-tab",
          role: "tab",
          id: `sw-tab-${spec.id}`,
          "aria-selected": isSelected ? "true" : "false",
          "aria-controls": `sw-panel-${spec.id}`,
          focusgroupstart: idx === 0 ? true : false,
          tabindex: !("focusGroup" in HTMLElement.prototype) && !isSelected ? "-1" : false,
          onClick: () => setTab(spec.id),
        },
        spec.label
      );
      tabButtons[spec.id] = btn;
      tablist.appendChild(btn);

      const panel = h(
        "div",
        {
          id: `sw-panel-${spec.id}`,
          role: "tabpanel",
          "aria-labelledby": `sw-tab-${spec.id}`,
          hidden: isSelected ? false : "until-found",
        },
        h("div", { className: "sw-tabpanel-inner" })
      );
      panel.addEventListener("beforematch", () => setTab(spec.id));
      panels[spec.id] = panel;
      bodyMount.appendChild(panel);
    });

    if (!("focusGroup" in HTMLElement.prototype)) {
      tablist.addEventListener("keydown", (e) => {
        const list = Object.values(tabButtons);
        const cur = list.indexOf(document.activeElement);
        if (cur < 0) return;
        let next = -1;
        if (e.key === "ArrowRight") next = (cur + 1) % list.length;
        else if (e.key === "ArrowLeft") next = (cur - 1 + list.length) % list.length;
        else if (e.key === "Home") next = 0;
        else if (e.key === "End") next = list.length - 1;
        if (next >= 0) {
          e.preventDefault();
          list[next].focus();
          const id = tabsSpec[next].id;
          setTab(id);
        }
      });
    }

    // Footer actions
    const leftFooter = h(
      "button",
      {
        type: "button",
        className: "text",
        onClick: () => copyText(agentSkill?.path || centerSkill?.central_path || ""),
      },
      t("复制路径", "Copy Path")
    );
    const rightBtns = [];
    if (agentSkill) {
      if (agentSkill.sync_status === "project_only") {
        rightBtns.push(
          h(
            "button",
            {
              type: "button",
              className: "text primary",
              onClick: async () => {
                dlg.close();
                await handleImportLocalSkill(opts.agentKey, agentSkill.relative_path);
              },
            },
            t("导入到中央仓库", "Import to Center")
          )
        );
      } else if (agentSkill.sync_status === "project_newer" || agentSkill.sync_status === "diverged") {
        rightBtns.push(
          h(
            "button",
            {
              type: "button",
              className: "text primary",
              onClick: async () => {
                dlg.close();
                await handleImportLocalSkill(opts.agentKey, agentSkill.relative_path);
              },
            },
            t("推送更新到中央", "Push to Center")
          )
        );
      }
      if (agentSkill.sync_status === "center_newer" || agentSkill.sync_status === "diverged") {
        rightBtns.push(
          h(
            "button",
            {
              type: "button",
              className: "text",
              onClick: async () => {
                dlg.close();
                await handlePullLocalSkill(opts.agentKey, agentSkill);
              },
            },
            t("从中央覆盖本地", "Pull from Center")
          )
        );
      }
      if (agentSkill.managed && agentSkill.center_skill_id) {
        rightBtns.push(
          h(
            "button",
            {
              type: "button",
              className: "text",
              onClick: async () => {
                dlg.close();
                await handleUnsyncSkills([agentSkill.center_skill_id], [opts.agentKey]);
              },
            },
            t("移出当前 Agent", "Unsync")
          )
        );
      }
    }
    rightBtns.push(
      h(
        "form",
        { method: "dialog", style: { display: "inline" } },
        h("button", { type: "submit", className: "text" }, t("关闭", "Close"))
      )
    );
    footerMount.replaceChildren(leftFooter, h("div", { className: "sw-actions" }, rightBtns));

    dlg.replaceChildren(header, tablist, bodyMount, footerMount);
    setTab(activeTab);
    dlg.showModal();

    if (agentSkill) {
      apiJSON(
        `/api/skills/agents/${encodeURIComponent(opts.agentKey)}/document?rel_path=${encodeURIComponent(
          agentSkill.relative_path
        )}`
      )
        .then((d) => {
          localDoc = d;
          renderTabContents();
        })
        .catch((err) => {
          localErr = err.message;
          renderTabContents();
        });
    }
    if (centerSkill) {
      apiJSON(`/api/skills/center/${encodeURIComponent(centerSkill.id)}/document`)
        .then((d) => {
          centerDoc = d;
          renderTabContents();
        })
        .catch((err) => {
          centerErr = err.message;
          renderTabContents();
        });
    }
  }

  function openAddSkillsDialog(lockedAgentKey = null) {
    const dlg = document.getElementById("skillDialog");
    if (!dlg) return;
    setupNativeDialog(dlg);

    const category = state.routeKind === "lobster" ? "lobster" : "coding";
    const activeAgents = activeAgentsForCategory(category);
    const chosenAgents = new Set(lockedAgentKey ? [lockedAgentKey] : activeAgents.map((a) => a.key));
    const selectedIDs = new Set();
    let searchText = "";
    let tagFilter = "all";
    let onlyAvailable = false;
    let lastClickedIndex = null;
    let importPathVal = "";
    let conflictBox = null;

    const bodyMount = h("div", { className: "sw-dialog-body" });
    const footerMount = h("div", { className: "sw-dialog-footer" });

    function getAlreadyDeployedSet() {
      const set = new Set();
      if (!chosenAgents.size) return set;
      for (const sk of state.centerSkills) {
        const targets = new Set((sk.targets || []).map((t) => t.tool));
        let onAll = true;
        for (const ak of chosenAgents) {
          if (!targets.has(ak)) {
            onAll = false;
            break;
          }
        }
        if (onAll) set.add(sk.id);
      }
      return set;
    }

    function filteredSkills(deployedSet) {
      const q = searchText.trim().toLowerCase();
      return (state.centerSkills || []).filter((sk) => {
        if (onlyAvailable && deployedSet.has(sk.id)) return false;
        if (tagFilter !== "all" && !(sk.tags || []).includes(tagFilter)) return false;
        if (!q) return true;
        return (
          sk.name.toLowerCase().includes(q) ||
          (sk.description || "").toLowerCase().includes(q) ||
          (sk.tags || []).some((tg) => tg.toLowerCase().includes(q))
        );
      });
    }

    function renderDialogBody() {
      const deployedSet = getAlreadyDeployedSet();
      const visible = filteredSkills(deployedSet);
      const allTags = Array.from(new Set((state.centerSkills || []).flatMap((s) => s.tags || []))).sort();

      const pieces = [];
      if (conflictBox) {
        pieces.push(
          h(
            "div",
            { className: "sw-conflict-box" },
            h("strong", null, t("发现目标目录冲突：", "Target Conflict Detected:")),
            conflictBox.map((c) => h("div", null, `${c.path}: ${c.reason}`))
          )
        );
      }

      if (!lockedAgentKey) {
        const agChips = activeAgents.map((a) =>
          h(
            "button",
            {
              type: "button",
              className: "sw-chip",
              "aria-pressed": chosenAgents.has(a.key) ? "true" : "false",
              onClick: () => {
                if (chosenAgents.has(a.key)) chosenAgents.delete(a.key);
                else chosenAgents.add(a.key);
                renderDialogBody();
              },
            },
            a.display_name
          )
        );
        pieces.push(
          h(
            "div",
            { className: "sw-field" },
            h("label", null, t("目标 Agent（已安装且启用）", "Target Agents (Installed & Enabled)")),
            activeAgents.length
              ? h("div", { className: "sw-chips" }, agChips)
              : h("div", { style: { color: "var(--muted)" } }, t("当前没有已启用的 Agent", "No active agents"))
          )
        );
      }

      // Quick local path import
      const importInput = h("input", {
        type: "text",
        className: "sw-input",
        style: { flex: 1 },
        value: importPathVal,
        placeholder: t("输入本地 Skill 目录路径直接导入并勾选（如 ~/my-skill）", "Import local skill dir into Central Library (e.g. ~/my-skill)"),
        onInput: (e) => {
          importPathVal = e.target.value;
        },
      });
      pieces.push(
        h(
          "div",
          { className: "sw-row" },
          importInput,
          h(
            "button",
            {
              type: "button",
              className: "text",
              onClick: async () => {
                if (!importPathVal.trim()) return;
                try {
                  const sk = await apiJSON("/api/skills/center/import-path", {
                    method: "POST",
                    body: { path: importPathVal.trim() },
                  });
                  importPathVal = "";
                  selectedIDs.add(sk.id);
                  const centerResp = await apiJSON("/api/skills/center");
                  state.centerSkills = centerResp?.skills || [];
                  notify(t(`已导入到中央仓库并选中: ${sk.name}`, `Imported & selected: ${sk.name}`), "ok");
                  renderDialogBody();
                } catch (err) {
                  notify(err.message, "err");
                }
              },
            },
            t("+ 导入路径", "+ Import Path")
          )
        )
      );

      // Search & filters
      const searchEl = makeSearchInput({
        focusKey: "add-skills-search",
        value: searchText,
        placeholder: t("搜索中央仓库 Skills（按名称 / 描述 / 标签）…", "Search Central Library..."),
        onUpdate: (val, shouldRender) => {
          searchText = val;
          lastClickedIndex = null;
          if (shouldRender) renderDialogBody();
        },
      });
      const availBtn = h(
        "button",
        {
          type: "button",
          className: "sw-chip",
          "aria-pressed": onlyAvailable ? "true" : "false",
          onClick: () => {
            onlyAvailable = !onlyAvailable;
            renderDialogBody();
          },
        },
        t("仅看未部署", "Available Only")
      );
      const selectAllBtn = h(
        "button",
        {
          type: "button",
          className: "text",
          onClick: () => {
            visible.forEach((sk) => {
              if (!deployedSet.has(sk.id)) selectedIDs.add(sk.id);
            });
            renderDialogBody();
          },
        },
        t("全选可用项", "Select All Available")
      );
      const clearBtn = h(
        "button",
        {
          type: "button",
          className: "text",
          onClick: () => {
            selectedIDs.clear();
            renderDialogBody();
          },
        },
        t("清空", "Clear")
      );
      pieces.push(h("div", { className: "sw-toolbar" }, searchEl, h("div", { className: "sw-actions" }, availBtn, selectAllBtn, clearBtn)));

      if (allTags.length) {
        pieces.push(
          h(
            "div",
            { className: "sw-chips" },
            h(
              "button",
              {
                type: "button",
                className: "sw-chip",
                "aria-pressed": tagFilter === "all" ? "true" : "false",
                onClick: () => {
                  tagFilter = "all";
                  renderDialogBody();
                },
              },
              t("全部标签", "All Tags")
            ),
            allTags.map((tg) =>
              h(
                "button",
                {
                  type: "button",
                  className: "sw-chip",
                  "aria-pressed": tagFilter === tg ? "true" : "false",
                  onClick: () => {
                    tagFilter = tg;
                    renderDialogBody();
                  },
                },
                tg
              )
            )
          )
        );
      }

      if (!visible.length) {
        pieces.push(
          h(
            "div",
            { className: "sw-empty" },
            state.centerSkills.length
              ? t("没有匹配的技能", "No matching central skills")
              : t("中央仓库暂无技能，请先从 Agent 导入或填写上方路径导入", "Central library is empty. Import a skill directory above or from an agent.")
          )
        );
      } else {
        const listBox = h("div", { className: "sw-list" });
        visible.forEach((sk, idx) => {
          const isDeployed = deployedSet.has(sk.id);
          const isChecked = isDeployed || selectedIDs.has(sk.id);
          const cb = h("input", {
            type: "checkbox",
            checked: isChecked,
            disabled: isDeployed,
            onClick: (e) => {
              e.stopPropagation();
              if (isDeployed) return;
              const nextVal = cb.checked;
              if (e.shiftKey && lastClickedIndex !== null) {
                const lo = Math.min(lastClickedIndex, idx);
                const hi = Math.max(lastClickedIndex, idx);
                for (let k = lo; k <= hi; k++) {
                  const cand = visible[k];
                  if (!deployedSet.has(cand.id)) {
                    if (nextVal) selectedIDs.add(cand.id);
                    else selectedIDs.delete(cand.id);
                  }
                }
              } else {
                if (nextVal) selectedIDs.add(sk.id);
                else selectedIDs.delete(sk.id);
              }
              lastClickedIndex = idx;
              renderDialogBody();
            },
          });
          const row = h(
            "label",
            {
              className: `sw-pick-row${isChecked && !isDeployed ? " is-checked" : ""}${isDeployed ? " is-disabled" : ""}`,
            },
            h(
              "div",
              { className: "sw-row", style: { minWidth: 0, flex: 1 } },
              cb,
              h(
                "div",
                { style: { minWidth: 0 } },
                h("div", { style: { fontWeight: 600, fontSize: "13px" } }, sk.name),
                sk.description && h("div", { style: { fontSize: "12px", color: "var(--muted)" } }, sk.description)
              )
            ),
            h(
              "div",
              { className: "sw-chips" },
              (sk.tags || []).map((tg) => h("span", { className: "sw-count-pill" }, tg)),
              isDeployed
                ? h("span", { className: "sw-badge in-sync" }, t("已部署", "Deployed"))
                : h("span", { className: "sw-count-pill" }, `${(sk.targets || []).length} agents`)
            )
          );
          listBox.appendChild(row);
        });
        pieces.push(listBox);
      }

      withPreservedFocus(dlg, () => {
        bodyMount.replaceChildren(...pieces);
      });

      footerMount.replaceChildren(
        h(
          "span",
          { style: { fontSize: "12px", color: "var(--muted)" } },
          t(`已选择 ${selectedIDs.size} 个技能 · 支持 Shift 连选`, `${selectedIDs.size} selected · Shift+click range select`)
        ),
        h(
          "div",
          { className: "sw-actions" },
          h(
            "form",
            { method: "dialog", style: { display: "inline" } },
            h("button", { type: "submit", className: "text" }, t("取消", "Cancel"))
          ),
          h(
            "button",
            {
              type: "button",
              className: "text primary",
              disabled: !selectedIDs.size || !chosenAgents.size,
              onClick: async () => {
                try {
                  const res = await handleBatchSync(Array.from(selectedIDs), Array.from(chosenAgents), {
                    showConflictBanner: false,
                  });
                  if (res && !res.ok && res.conflicts?.length) {
                    conflictBox = res.conflicts;
                    renderDialogBody();
                    return;
                  }
                  dlg.close("synced");
                } catch (err) {
                  if (err.conflicts?.length) {
                    conflictBox = err.conflicts;
                    renderDialogBody();
                  }
                }
              },
            },
            t(`同步到目标 Agent (${selectedIDs.size})`, `Sync Selected (${selectedIDs.size})`)
          )
        )
      );
    }

    const header = h(
      "div",
      { className: "sw-dialog-header" },
      h(
        "div",
        null,
        h(
          "h2",
          { id: "skillDialogTitle", className: "sw-dialog-title" },
          lockedAgentKey
            ? t(`为 ${agentMap().get(lockedAgentKey)?.display_name || lockedAgentKey} 添加 Skills`, `Add Skills to ${agentMap().get(lockedAgentKey)?.display_name || lockedAgentKey}`)
            : t("从中央仓库批量部署 Skills", "Deploy Skills from Central Library")
        ),
        h(
          "div",
          { className: "sw-dialog-sub" },
          t(`同步模式: ${state.overview.sync_mode || "symlink"}`, `Sync mode: ${state.overview.sync_mode || "symlink"}`)
        )
      ),
      h(
        "form",
        { method: "dialog" },
        h("button", { type: "submit", className: "text", "aria-label": t("关闭", "Close") }, "✕")
      )
    );

    dlg.replaceChildren(header, bodyMount, footerMount);
    renderDialogBody();
    dlg.showModal();
  }

  function openPresetDialog(existing = null, seedSkillIDs = null) {
    const dlg = document.getElementById("skillDialog");
    if (!dlg) return;
    setupNativeDialog(dlg);

    let name = existing?.name || "";
    let desc = existing?.description || "";
    const chosen = new Set(existing?.skill_ids || seedSkillIDs || []);
    let search = "";

    const bodyMount = h("div", { className: "sw-dialog-body" });
    const footerMount = h("div", { className: "sw-dialog-footer" });

    function renderList() {
      const q = search.trim().toLowerCase();
      const filtered = (state.centerSkills || []).filter(
        (s) => !q || s.name.toLowerCase().includes(q) || (s.description || "").toLowerCase().includes(q)
      );
      const list = h(
        "div",
        { className: "sw-list" },
        filtered.length
          ? filtered.map((sk) => {
              const checked = chosen.has(sk.id);
              const cb = h("input", {
                type: "checkbox",
                checked,
                onChange: () => {
                  if (cb.checked) chosen.add(sk.id);
                  else chosen.delete(sk.id);
                  renderForm();
                },
              });
              return h(
                "label",
                { className: `sw-pick-row${checked ? " is-checked" : ""}` },
                h(
                  "div",
                  { className: "sw-row" },
                  cb,
                  h("span", { style: { fontWeight: 600 } }, sk.name),
                  h("span", { style: { fontSize: "12px", color: "var(--muted)" } }, sk.description || "")
                )
              );
            })
          : h("div", { className: "sw-empty" }, t("中央仓库暂无可选技能", "No central skills found"))
      );
      return list;
    }

    function renderForm() {
      const nameInput = h("input", {
        type: "text",
        className: "sw-input",
        value: name,
        placeholder: t("例如：前端开发 / Rust 审计", "e.g. Frontend / Security Audit"),
        onInput: (e) => {
          name = e.target.value;
        },
      });
      const descInput = h("input", {
        type: "text",
        className: "sw-input",
        value: desc,
        placeholder: t("可选描述", "Optional description"),
        onInput: (e) => {
          desc = e.target.value;
        },
      });
      const searchInput = h("input", {
        type: "search",
        className: "sw-input",
        value: search,
        placeholder: t("筛选技能…", "Filter skills..."),
        onInput: (e) => {
          search = e.target.value;
          const slot = bodyMount.querySelector(".sw-preset-skills-slot");
          if (slot) slot.replaceChildren(renderList());
        },
      });

      const children = [
        h("div", { className: "sw-field" }, h("label", null, t("Preset 名称", "Preset Name")), nameInput),
        h("div", { className: "sw-field" }, h("label", null, t("描述", "Description")), descInput),
        h("div", { className: "sw-field" }, h("label", null, t(`选择包含的技能 (${chosen.size})`, `Skills in Preset (${chosen.size})`)), searchInput),
        h("div", { className: "sw-preset-skills-slot" }, renderList()),
      ];
      bodyMount.replaceChildren(...children);

      footerMount.replaceChildren(
        h("span", { style: { fontSize: "12px", color: "var(--muted)" } }, t(`已选 ${chosen.size} 项`, `${chosen.size} skill(s)`)),
        h(
          "div",
          { className: "sw-actions" },
          h(
            "form",
            { method: "dialog", style: { display: "inline" } },
            h("button", { type: "submit", className: "text" }, t("取消", "Cancel"))
          ),
          h(
            "button",
            {
              type: "button",
              className: "text primary",
              onClick: async () => {
                if (!name.trim()) {
                  notify(t("请输入 Preset 名称", "Preset name is required"), "warn");
                  return;
                }
                try {
                  await apiJSON("/api/skills/presets", {
                    method: "POST",
                    body: {
                      id: existing?.id || "",
                      name: name.trim(),
                      description: desc.trim(),
                      skill_ids: Array.from(chosen),
                    },
                  });
                  notify(t("已保存 Preset", "Preset saved"), "ok");
                  dlg.close("saved");
                  await loadAll(true);
                } catch (err) {
                  notify(err.message, "err");
                }
              },
            },
            t("保存 Preset", "Save Preset")
          )
        )
      );
    }

    const header = h(
      "div",
      { className: "sw-dialog-header" },
      h("h2", { id: "skillDialogTitle", className: "sw-dialog-title" }, existing ? t(`编辑 Preset: ${existing.name}`, `Edit Preset: ${existing.name}`) : t("新建 Preset 组合", "New Skill Preset")),
      h(
        "form",
        { method: "dialog" },
        h("button", { type: "submit", className: "text", "aria-label": t("关闭", "Close") }, "✕")
      )
    );

    dlg.replaceChildren(header, bodyMount, footerMount);
    renderForm();
    dlg.showModal();
  }

  function openTagsDialog(centerSkill) {
    const dlg = document.getElementById("skillDialog");
    if (!dlg) return;
    setupNativeDialog(dlg);

    let tagsStr = (centerSkill.tags || []).join(", ");
    const input = h("input", {
      type: "text",
      className: "sw-input",
      value: tagsStr,
      placeholder: t("使用逗号分隔标签，例如：react, security, audit", "Comma-separated tags, e.g. react, security"),
      onInput: (e) => {
        tagsStr = e.target.value;
      },
    });
    const dlgContent = h(
      "div",
      null,
      h(
        "div",
        { className: "sw-dialog-header" },
        h("h2", { id: "skillDialogTitle", className: "sw-dialog-title" }, t(`编辑标签: ${centerSkill.name}`, `Edit Tags: ${centerSkill.name}`)),
        h(
          "form",
          { method: "dialog" },
          h("button", { type: "submit", className: "text", "aria-label": t("关闭", "Close") }, "✕")
        )
      ),
      h("div", { className: "sw-dialog-body" }, h("div", { className: "sw-field" }, h("label", null, t("标签列表", "Tags")), input)),
      h(
        "div",
        { className: "sw-dialog-footer" },
        h("span"),
        h(
          "div",
          { className: "sw-actions" },
          h(
            "form",
            { method: "dialog", style: { display: "inline" } },
            h("button", { type: "submit", className: "text" }, t("取消", "Cancel"))
          ),
          h(
            "button",
            {
              type: "button",
              className: "text primary",
              onClick: async () => {
                const tags = tagsStr
                  .split(/[,，]/)
                  .map((x) => x.trim())
                  .filter(Boolean);
                try {
                  await apiJSON(`/api/skills/center/${encodeURIComponent(centerSkill.id)}/tags`, {
                    method: "POST",
                    body: { tags },
                  });
                  notify(t(`已更新标签: ${centerSkill.name}`, `Updated tags: ${centerSkill.name}`), "ok");
                  dlg.close("saved");
                  await loadAll(false);
                } catch (err) {
                  notify(err.message, "err");
                }
              },
            },
            t("保存标签", "Save Tags")
          )
        )
      )
    );
    dlg.replaceChildren(...Array.from(dlgContent.childNodes));
    dlg.showModal();
  }

  // -------------------- Project Workspace Actions & Dialogs --------------------
  function projectHealthDotClass(project) {
    const h = project?.sync_health;
    if (!project || project.skill_count === 0) return "muted";
    if (!h) return "muted";
    if (h.diverged > 0) return "danger";
    if (h.project_newer > 0 || h.center_newer > 0) return "warn";
    if (h.in_sync === project.skill_count) return "ok";
    return "muted";
  }

  async function confirmUnsyncedRemoval(projectID, variants) {
    if (!variants || !variants.length) return true;
    let fresh = null;
    try {
      const resp = await apiJSON(`/api/skills/projects/${encodeURIComponent(projectID)}/skills`);
      fresh = resp?.skills || [];
    } catch {
      fresh = null;
    }
    const unsynced = variants.filter((v) => {
      const curr = fresh?.find((f) => f.relative_path === v.relative_path && f.agent === v.agent);
      return curr?.sync_status !== "in_sync";
    });
    if (!unsynced.length) return true;
    const otherClean = variants.length - unsynced.length;
    const listText = unsynced.map((v) => `• ${v.name} · ${v.agent_display_name || v.agent} (${v.sync_status})`).join("\n");
    const extraText = otherClean > 0
      ? t(`\n（另有 ${otherClean} 份已与中央同步的副本也将一并移出）`, `\n(${otherClean} other in-sync copies will also be removed.)`)
      : "";
    return showConfirm({
      title: t("移除未与中央同步的项目副本？", "Remove unsynced project copies?"),
      message:
        t(
          "以下项目副本尚未与中央仓库完全同步，从项目移出可能会丢失未导入中央的修改：\n",
          "The following project copies are not verified in_sync with Central Library and may hold unsaved changes:\n"
        ) +
        listText +
        extraText,
      confirmLabel: t("仍要移除", "Remove Anyway"),
      danger: true,
    });
  }

  async function pushProjectSkillToCenterAndAlign(projectID, group) {
    const unproven = group.variants.filter((v) => v.sync_status !== "in_sync");
    if (unproven.length > 1) {
      return { conflicting: unproven.length, alignFailed: 0 };
    }
    const winner = unproven[0] || group.primaryVariant;
    await apiJSON(`/api/skills/projects/${encodeURIComponent(projectID)}/import`, {
      method: "POST",
      body: { rel_path: winner.relative_path, agent: winner.agent },
    });
    let alignFailed = 0;
    for (const variant of group.variants) {
      if (variant === winner) continue;
      try {
        await apiJSON(`/api/skills/projects/${encodeURIComponent(projectID)}/pull`, {
          method: "POST",
          body: { rel_path: variant.relative_path, agent: variant.agent, force: false },
        });
      } catch {
        alignFailed++;
      }
    }
    return { conflicting: 0, alignFailed };
  }

  async function handleRemoveProject(project) {
    const ok = await showConfirm({
      title: t(`移除工作区 "${project.name}"？`, `Remove workspace "${project.name}"?`),
      message: t(
        "仅从列表中移除该工作区注册记录，不会删除磁盘上的任何技能目录或文件。",
        "This only unregisters the workspace from the list; disk files remain untouched."
      ),
      confirmLabel: t("移除记录", "Remove"),
      danger: false,
    });
    if (!ok) return;
    try {
      await apiJSON(`/api/skills/projects/${encodeURIComponent(project.id)}`, { method: "DELETE" });
      notify(t(`已移除工作区: ${project.name}`, `Removed workspace: ${project.name}`), "ok");
      if (state.selectedProjectId === project.id) {
        await loadAll(false);
        navigateTo("#/projects");
        return;
      }
      await loadAll(true);
    } catch (err) {
      notify(err.message, "err");
    }
  }

  async function handleReorderProject(index, delta) {
    const ids = (state.projects || []).map((p) => p.id);
    const next = index + delta;
    if (index < 0 || next < 0 || next >= ids.length) return;
    [ids[index], ids[next]] = [ids[next], ids[index]];
    try {
      await apiJSON("/api/skills/projects/reorder", { method: "POST", body: { ids } });
      await loadAll(true);
    } catch (err) {
      notify(err.message, "err");
    }
  }

  function openAddProjectDialog() {
    const dlg = document.getElementById("skillDialog");
    if (!dlg) return;
    setupNativeDialog(dlg);

    let activeTab = "manual";
    let manualPath = "";
    let scanRoot = "";
    let scanResults = [];
    let scanPerformed = false;
    let scanSelected = new Set();
    let linkedName = "";
    let linkedPath = "";
    let linkedDisabledPath = "";
    let busy = false;

    const bodyMount = h("div", { className: "sw-dialog-body", style: { padding: 0 } });
    const footerMount = h("div", { className: "sw-dialog-footer" });
    const tabButtons = {};
    const panels = {};

    function setTab(nextTab) {
      activeTab = nextTab;
      for (const [k, btn] of Object.entries(tabButtons)) {
        const selected = k === activeTab;
        btn.setAttribute("aria-selected", selected ? "true" : "false");
        if (!("focusGroup" in HTMLElement.prototype)) {
          btn.setAttribute("tabindex", selected ? "0" : "-1");
        }
      }
      for (const [k, panel] of Object.entries(panels)) {
        if (k === activeTab) panel.removeAttribute("hidden");
        else panel.setAttribute("hidden", "until-found");
      }
      renderTabContent();
    }

    function renderTabContent() {
      // Tab 1: Manual
      if (panels.manual) {
        const inner = panels.manual.firstChild;
        inner.replaceChildren(
          h(
            "p",
            { style: { margin: 0, color: "var(--muted)", fontSize: "13px" } },
            t(
              "输入本地项目根目录路径（若尚未包含 .claude/skills 将自动初始化 .claude/skills 与 skills-disabled）。",
              "Enter a local project root directory (initializes .claude/skills and skills-disabled if missing)."
            )
          ),
          h(
            "div",
            { className: "sw-field" },
            h("label", null, t("项目目录路径", "Project Directory Path")),
            h("input", {
              type: "text",
              className: "sw-input",
              value: manualPath,
              placeholder: t("例如：~/aiprojects/my-repo", "e.g. ~/projects/my-repo"),
              onInput: (e) => {
                manualPath = e.target.value;
                renderFooter();
              },
            })
          )
        );
      }

      // Tab 2: Scan
      if (panels.scan) {
        const inner = panels.scan.firstChild;
        const scanInput = h("input", {
          type: "text",
          className: "sw-input",
          style: { flex: 1 },
          value: scanRoot,
          placeholder: t("输入要递归扫描的项目根目录（最多 4 层）…", "Root directory to scan (up to depth 4)..."),
          onInput: (e) => {
            scanRoot = e.target.value;
          },
          onKeydown: (e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              doScan();
            }
          },
        });
        async function doScan() {
          if (!scanRoot.trim() || busy) return;
          busy = true;
          renderTabContent();
          try {
            const resp = await apiJSON("/api/skills/projects/scan", {
              method: "POST",
              body: { root: scanRoot.trim() },
            });
            scanResults = resp?.projects || [];
            scanPerformed = true;
            scanSelected = new Set(scanResults.filter((x) => !x.already_added).map((x) => x.path));
          } catch (err) {
            notify(err.message, "err");
          } finally {
            busy = false;
            renderTabContent();
            renderFooter();
          }
        }

        const pieces = [
          h(
            "div",
            { className: "sw-row" },
            scanInput,
            h(
              "button",
              { type: "button", className: "text primary", disabled: busy, onClick: doScan },
              busy ? t("扫描中…", "Scanning...") : t("开始扫描", "Scan")
            )
          ),
        ];
        if (scanPerformed && !scanResults.length) {
          pieces.push(h("div", { className: "sw-empty" }, t("未发现包含 Agent skills 目录的项目", "No projects with agent skills directories found.")));
        } else if (scanResults.length) {
          const selectable = scanResults.filter((x) => !x.already_added);
          pieces.push(
            h(
              "div",
              { className: "sw-toolbar" },
              h("span", { style: { fontSize: "12px", color: "var(--muted)" } }, t(`发现 ${scanResults.length} 个目录`, `Found ${scanResults.length} directory(ies)`)),
              selectable.length > 0 &&
                h(
                  "button",
                  {
                    type: "button",
                    className: "text",
                    onClick: () => {
                      if (scanSelected.size === selectable.length) scanSelected.clear();
                      else scanSelected = new Set(selectable.map((x) => x.path));
                      renderTabContent();
                      renderFooter();
                    },
                  },
                  scanSelected.size === selectable.length ? t("取消全选", "Deselect All") : t("全选未添加", "Select All New")
                )
            ),
            h(
              "div",
              { className: "sw-list" },
              scanResults.map((item) => {
                const checked = item.already_added || scanSelected.has(item.path);
                const cb = h("input", {
                  type: "checkbox",
                  checked,
                  disabled: item.already_added,
                  onChange: () => {
                    if (item.already_added) return;
                    if (cb.checked) scanSelected.add(item.path);
                    else scanSelected.delete(item.path);
                    renderTabContent();
                    renderFooter();
                  },
                });
                return h(
                  "label",
                  { className: `sw-pick-row${checked && !item.already_added ? " is-checked" : ""}${item.already_added ? " is-disabled" : ""}` },
                  h("div", { className: "sw-row" }, cb, h("span", { className: "sw-path-chip" }, item.display_path)),
                  item.already_added && h("span", { className: "sw-badge in-sync" }, t("已添加", "Added"))
                );
              })
            )
          );
        }
        withPreservedFocus(inner, () => inner.replaceChildren(...pieces));
      }

      // Tab 3: Linked
      if (panels.linked) {
        const inner = panels.linked.firstChild;
        inner.replaceChildren(
          h(
            "p",
            { style: { margin: 0, color: "var(--muted)", fontSize: "13px" } },
            t(
              "关联任意外部技能目录（自动扫描并支持递归子目录；可指定或自动创建同级 -disabled 停用目录）。",
              "Link any standalone skills directory directly (recursively scanned; optional companion -disabled folder)."
            )
          ),
          h(
            "div",
            { className: "sw-field" },
            h("label", null, t("工作区名称", "Workspace Name")),
            h("input", {
              type: "text",
              className: "sw-input",
              value: linkedName,
              placeholder: t("例如：Team Shared Skills", "e.g. Team Shared Skills"),
              onInput: (e) => {
                linkedName = e.target.value;
                renderFooter();
              },
            })
          ),
          h(
            "div",
            { className: "sw-field" },
            h("label", null, t("启用技能目录路径", "Enabled Skills Directory")),
            h("input", {
              type: "text",
              className: "sw-input",
              value: linkedPath,
              placeholder: t("例如：~/shared-skills", "e.g. ~/shared-skills"),
              onInput: (e) => {
                linkedPath = e.target.value;
                renderFooter();
              },
            })
          ),
          h(
            "div",
            { className: "sw-field" },
            h("label", null, t("停用技能目录路径（可选，留空自动推导 <dir>-disabled）", "Disabled Skills Directory (optional)")),
            h("input", {
              type: "text",
              className: "sw-input",
              value: linkedDisabledPath,
              placeholder: t("可选：~/shared-skills-disabled", "Optional: ~/shared-skills-disabled"),
              onInput: (e) => {
                linkedDisabledPath = e.target.value;
              },
            })
          )
        );
      }

      renderFooter();
    }

    function renderFooter() {
      let actionLabel = t("添加项目", "Add Project");
      let disabled = busy || !manualPath.trim();
      if (activeTab === "scan") {
        actionLabel = t(`添加选中项目 (${scanSelected.size})`, `Add Selected (${scanSelected.size})`);
        disabled = busy || scanSelected.size === 0;
      } else if (activeTab === "linked") {
        actionLabel = t("添加关联工作区", "Add Linked Workspace");
        disabled = busy || !linkedName.trim() || !linkedPath.trim();
      }

      footerMount.replaceChildren(
        h("span"),
        h(
          "div",
          { className: "sw-actions" },
          h("form", { method: "dialog", style: { display: "inline" } }, h("button", { type: "submit", className: "text" }, t("取消", "Cancel"))),
          h(
            "button",
            {
              type: "button",
              className: "text primary",
              disabled,
              onClick: async () => {
                busy = true;
                renderFooter();
                try {
                  if (activeTab === "manual") {
                    const p = await apiJSON("/api/skills/projects", {
                      method: "POST",
                      body: { path: manualPath.trim() },
                    });
                    notify(t(`已添加项目: ${p.name}`, `Added project: ${p.name}`), "ok");
                    dlg.close("added");
                    await loadAll(false);
                    navigateTo(`#/project/${encodeURIComponent(p.id)}`);
                  } else if (activeTab === "scan") {
                    let added = 0;
                    for (const pPath of scanSelected) {
                      try {
                        await apiJSON("/api/skills/projects", { method: "POST", body: { path: pPath } });
                        added++;
                      } catch {
                        // skip duplicates
                      }
                    }
                    notify(t(`已批量添加 ${added} 个项目`, `Added ${added} project(s)`), "ok");
                    dlg.close("added");
                    await loadAll(false);
                  } else {
                    const p = await apiJSON("/api/skills/projects/linked", {
                      method: "POST",
                      body: {
                        name: linkedName.trim(),
                        path: linkedPath.trim(),
                        disabled_path: linkedDisabledPath.trim(),
                      },
                    });
                    notify(t(`已添加关联工作区: ${p.name}`, `Added linked workspace: ${p.name}`), "ok");
                    dlg.close("added");
                    await loadAll(false);
                    navigateTo(`#/project/${encodeURIComponent(p.id)}`);
                  }
                } catch (err) {
                  notify(err.message, "err");
                } finally {
                  busy = false;
                  if (dlg.open) renderFooter();
                }
              },
            },
            actionLabel
          )
        )
      );
    }

    const header = h(
      "div",
      { className: "sw-dialog-header" },
      h("h2", { id: "skillDialogTitle", className: "sw-dialog-title" }, t("添加项目 / 关联工作区", "Add Project / Linked Workspace")),
      h("form", { method: "dialog" }, h("button", { type: "submit", className: "text", "aria-label": t("关闭", "Close") }, "✕"))
    );

    const specs = [
      { id: "manual", label: t("手动输入路径", "Manual Path") },
      { id: "scan", label: t("扫描目录发现", "Scan Folder") },
      { id: "linked", label: t("关联目录", "Linked Directory") },
    ];
    const tablist = h("div", {
      className: "sw-tablist",
      role: "tablist",
      focusgroup: "tablist nomemory",
      "aria-label": t("添加工作区方式", "Add Workspace Mode"),
    });
    specs.forEach((spec, idx) => {
      const selected = spec.id === activeTab;
      const btn = h(
        "button",
        {
          type: "button",
          className: "sw-tab",
          role: "tab",
          id: `sw-proj-tab-${spec.id}`,
          "aria-selected": selected ? "true" : "false",
          "aria-controls": `sw-proj-panel-${spec.id}`,
          focusgroupstart: idx === 0 ? true : false,
          tabindex: !("focusGroup" in HTMLElement.prototype) && !selected ? "-1" : false,
          onClick: () => setTab(spec.id),
        },
        spec.label
      );
      tabButtons[spec.id] = btn;
      tablist.appendChild(btn);

      const panel = h(
        "div",
        {
          id: `sw-proj-panel-${spec.id}`,
          role: "tabpanel",
          "aria-labelledby": `sw-proj-tab-${spec.id}`,
          hidden: selected ? false : "until-found",
        },
        h("div", { className: "sw-tabpanel-inner" })
      );
      panel.addEventListener("beforematch", () => setTab(spec.id));
      panels[spec.id] = panel;
      bodyMount.appendChild(panel);
    });
    if (!("focusGroup" in HTMLElement.prototype)) {
      tablist.addEventListener("keydown", (e) => {
        const list = Object.values(tabButtons);
        const cur = list.indexOf(document.activeElement);
        if (cur < 0) return;
        let next = -1;
        if (e.key === "ArrowRight") next = (cur + 1) % list.length;
        else if (e.key === "ArrowLeft") next = (cur - 1 + list.length) % list.length;
        else if (e.key === "Home") next = 0;
        else if (e.key === "End") next = list.length - 1;
        if (next >= 0) {
          e.preventDefault();
          list[next].focus();
          setTab(specs[next].id);
        }
      });
    }

    dlg.replaceChildren(header, tablist, bodyMount, footerMount);
    setTab("manual");
    dlg.showModal();
  }

  function defaultProjectTargetAgents(project, targets) {
    const avail = targets.filter((tg) => tg.enabled && tg.installed);
    const availKeys = new Set(avail.map((tg) => tg.key));
    try {
      const saved = JSON.parse(localStorage.getItem(`ctxview.projectLastAgents:${project.id}`) || "[]");
      if (Array.isArray(saved)) {
        const filtered = saved.filter((k) => availKeys.has(k));
        if (filtered.length) return filtered;
      }
    } catch {}
    if (project.workspace_type === "linked" && targets.length) return [targets[0].key];
    if (availKeys.has("claude_code")) return ["claude_code"];
    return avail.length ? [avail[0].key] : [];
  }

  function openAddSkillsToProjectDialog(project) {
    const dlg = document.getElementById("skillDialog");
    if (!dlg) return;
    setupNativeDialog(dlg);

    const targets = state.projectTargets || [];
    const chosenAgents = new Set(defaultProjectTargetAgents(project, targets));
    const selectedIDs = new Set();
    let searchText = "";
    let tagFilter = "all";
    let onlyAvailable = false;
    let lastClickedIndex = null;
    let importPathVal = "";
    let conflictBox = null;

    const deployedByAgent = new Map();
    for (const sk of state.projectSkills || []) {
      if (!sk.center_skill_id) continue;
      if (!deployedByAgent.has(sk.agent)) deployedByAgent.set(sk.agent, new Set());
      deployedByAgent.get(sk.agent).add(sk.center_skill_id);
    }

    function getAlreadyDeployedSet() {
      const set = new Set();
      if (!chosenAgents.size) return set;
      for (const sk of state.centerSkills || []) {
        let onAll = true;
        for (const ak of chosenAgents) {
          if (!deployedByAgent.get(ak)?.has(sk.id)) {
            onAll = false;
            break;
          }
        }
        if (onAll) set.add(sk.id);
      }
      return set;
    }

    const bodyMount = h("div", { className: "sw-dialog-body" });
    const footerMount = h("div", { className: "sw-dialog-footer" });

    function renderDialogBody() {
      const deployedSet = getAlreadyDeployedSet();
      const q = searchText.trim().toLowerCase();
      const visible = (state.centerSkills || []).filter((sk) => {
        if (onlyAvailable && deployedSet.has(sk.id)) return false;
        if (tagFilter !== "all" && !(sk.tags || []).includes(tagFilter)) return false;
        if (!q) return true;
        return (
          sk.name.toLowerCase().includes(q) ||
          (sk.description || "").toLowerCase().includes(q) ||
          (sk.tags || []).some((tg) => tg.toLowerCase().includes(q))
        );
      });
      const allTags = Array.from(new Set((state.centerSkills || []).flatMap((s) => s.tags || []))).sort();

      const pieces = [];
      if (conflictBox?.length) {
        pieces.push(
          h(
            "div",
            { className: "sw-conflict-box" },
            h("strong", null, t("发现目标目录冲突：", "Target Conflict Detected:")),
            conflictBox.map((c) => h("div", null, `${c.path}: ${c.reason}`))
          )
        );
      }

      if (project.workspace_type !== "linked") {
        const agChips = targets.filter((tg) => tg.installed).map((tg) => {
          const usable = tg.enabled && tg.installed;
          return h(
            "button",
            {
              type: "button",
              className: "sw-chip",
              disabled: !usable,
              title: tg.relative_skills_dir || "",
              "aria-pressed": chosenAgents.has(tg.key) ? "true" : "false",
              onClick: () => {
                if (!usable) return;
                if (chosenAgents.has(tg.key)) chosenAgents.delete(tg.key);
                else chosenAgents.add(tg.key);
                renderDialogBody();
              },
            },
            `${tg.display_name}${tg.relative_skills_dir ? ` (${tg.relative_skills_dir})` : ""}`
          );
        });
        pieces.push(
          h(
            "div",
            { className: "sw-field" },
            h("label", null, t("部署到项目 Agent 目录（已按共享目录去重）", "Target Project Agent Directories")),
            h("div", { className: "sw-chips" }, agChips)
          )
        );
      }

      const importInput = h("input", {
        type: "text",
        className: "sw-input",
        style: { flex: 1 },
        value: importPathVal,
        placeholder: t("导入本地 Skill 目录到中央仓库并勾选…", "Import local skill dir into Central Library..."),
        onInput: (e) => {
          importPathVal = e.target.value;
        },
      });
      pieces.push(
        h(
          "div",
          { className: "sw-row" },
          importInput,
          h(
            "button",
            {
              type: "button",
              className: "text",
              onClick: async () => {
                if (!importPathVal.trim()) return;
                try {
                  const sk = await apiJSON("/api/skills/center/import-path", {
                    method: "POST",
                    body: { path: importPathVal.trim() },
                  });
                  importPathVal = "";
                  selectedIDs.add(sk.id);
                  const centerResp = await apiJSON("/api/skills/center");
                  state.centerSkills = centerResp?.skills || [];
                  notify(t(`已导入并选中: ${sk.name}`, `Imported & selected: ${sk.name}`), "ok");
                  renderDialogBody();
                } catch (err) {
                  notify(err.message, "err");
                }
              },
            },
            t("+ 导入路径", "+ Import Path")
          )
        )
      );

      pieces.push(
        h(
          "div",
          { className: "sw-toolbar" },
          makeSearchInput({
            focusKey: "add-proj-skills-search",
            value: searchText,
            placeholder: t("搜索中央仓库 Skills…", "Search Central Library..."),
            onUpdate: (val, shouldRender) => {
              searchText = val;
              lastClickedIndex = null;
              if (shouldRender) renderDialogBody();
            },
          }),
          h(
            "div",
            { className: "sw-actions" },
            h(
              "button",
              {
                type: "button",
                className: "sw-chip",
                "aria-pressed": onlyAvailable ? "true" : "false",
                onClick: () => {
                  onlyAvailable = !onlyAvailable;
                  renderDialogBody();
                },
              },
              t("仅看未部署", "Available Only")
            ),
            h(
              "button",
              {
                type: "button",
                className: "text",
                onClick: () => {
                  visible.forEach((sk) => {
                    if (!deployedSet.has(sk.id)) selectedIDs.add(sk.id);
                  });
                  renderDialogBody();
                },
              },
              t("全选可用项", "Select All Available")
            ),
            h(
              "button",
              {
                type: "button",
                className: "text",
                onClick: () => {
                  selectedIDs.clear();
                  renderDialogBody();
                },
              },
              t("清空", "Clear")
            )
          )
        )
      );

      if (allTags.length) {
        pieces.push(
          h(
            "div",
            { className: "sw-chips" },
            h(
              "button",
              {
                type: "button",
                className: "sw-chip",
                "aria-pressed": tagFilter === "all" ? "true" : "false",
                onClick: () => {
                  tagFilter = "all";
                  renderDialogBody();
                },
              },
              t("全部标签", "All Tags")
            ),
            allTags.map((tg) =>
              h(
                "button",
                {
                  type: "button",
                  className: "sw-chip",
                  "aria-pressed": tagFilter === tg ? "true" : "false",
                  onClick: () => {
                    tagFilter = tg;
                    renderDialogBody();
                  },
                },
                tg
              )
            )
          )
        );
      }

      if (!visible.length) {
        pieces.push(h("div", { className: "sw-empty" }, t("没有可匹配的中央仓库技能", "No matching central skills.")));
      } else {
        const listBox = h("div", { className: "sw-list" });
        visible.forEach((sk, idx) => {
          const isDeployed = deployedSet.has(sk.id);
          const isChecked = isDeployed || selectedIDs.has(sk.id);
          const cb = h("input", {
            type: "checkbox",
            checked: isChecked,
            disabled: isDeployed,
            onClick: (e) => {
              e.stopPropagation();
              if (isDeployed) return;
              const nextVal = cb.checked;
              if (e.shiftKey && lastClickedIndex !== null) {
                const lo = Math.min(lastClickedIndex, idx);
                const hi = Math.max(lastClickedIndex, idx);
                for (let k = lo; k <= hi; k++) {
                  const cand = visible[k];
                  if (!deployedSet.has(cand.id)) {
                    if (nextVal) selectedIDs.add(cand.id);
                    else selectedIDs.delete(cand.id);
                  }
                }
              } else {
                if (nextVal) selectedIDs.add(sk.id);
                else selectedIDs.delete(sk.id);
              }
              lastClickedIndex = idx;
              renderDialogBody();
            },
          });
          listBox.appendChild(
            h(
              "label",
              { className: `sw-pick-row${isChecked && !isDeployed ? " is-checked" : ""}${isDeployed ? " is-disabled" : ""}` },
              h(
                "div",
                { className: "sw-row", style: { minWidth: 0, flex: 1 } },
                cb,
                h(
                  "div",
                  { style: { minWidth: 0 } },
                  h("div", { style: { fontWeight: 600, fontSize: "13px" } }, sk.name),
                  sk.description && h("div", { style: { fontSize: "12px", color: "var(--muted)" } }, sk.description)
                )
              ),
              h(
                "div",
                { className: "sw-chips" },
                (sk.tags || []).map((tg) => h("span", { className: "sw-count-pill" }, tg)),
                isDeployed && h("span", { className: "sw-badge in-sync" }, t("已部署", "Deployed"))
              )
            )
          );
        });
        pieces.push(listBox);
      }

      withPreservedFocus(dlg, () => bodyMount.replaceChildren(...pieces));

      footerMount.replaceChildren(
        h("span", { style: { fontSize: "12px", color: "var(--muted)" } }, t(`已选 ${selectedIDs.size} 项 · 支持 Shift 连选`, `${selectedIDs.size} selected · Shift+click range`)),
        h(
          "div",
          { className: "sw-actions" },
          h("form", { method: "dialog", style: { display: "inline" } }, h("button", { type: "submit", className: "text" }, t("取消", "Cancel"))),
          h(
            "button",
            {
              type: "button",
              className: "text primary",
              disabled: !selectedIDs.size || !chosenAgents.size,
              onClick: async () => {
                conflictBox = null;
                const agentArr = Array.from(chosenAgents);
                try {
                  localStorage.setItem(`ctxview.projectLastAgents:${project.id}`, JSON.stringify(agentArr));
                } catch {}
                try {
                  const res = await apiJSON(`/api/skills/projects/${encodeURIComponent(project.id)}/export`, {
                    method: "POST",
                    body: { skill_ids: Array.from(selectedIDs), agents: agentArr },
                  });
                  if (!res.ok && res.conflicts?.length) {
                    conflictBox = res.conflicts;
                    renderDialogBody();
                    await loadAll(false);
                    return;
                  }
                  notify(t(`已导出 ${res.applied} 项技能到项目（跳过 ${res.skipped} 项）`, `Exported ${res.applied} (skipped ${res.skipped})`), "ok");
                  dlg.close("exported");
                  await loadAll(false);
                } catch (err) {
                  if (err.conflicts?.length) {
                    conflictBox = err.conflicts;
                    renderDialogBody();
                  }
                  notify(err.message, "err");
                }
              },
            },
            t(`部署到项目 (${selectedIDs.size})`, `Deploy to Project (${selectedIDs.size})`)
          )
        )
      );
    }

    const header = h(
      "div",
      { className: "sw-dialog-header" },
      h(
        "div",
        null,
        h("h2", { id: "skillDialogTitle", className: "sw-dialog-title" }, t(`向项目 "${project.name}" 添加 Skills`, `Add Skills to "${project.name}"`)),
        h("div", { className: "sw-dialog-sub" }, h("span", { className: "sw-path-chip" }, project.display_path))
      ),
      h("form", { method: "dialog" }, h("button", { type: "submit", className: "text", "aria-label": t("关闭", "Close") }, "✕"))
    );

    dlg.replaceChildren(header, bodyMount, footerMount);
    renderDialogBody();
    dlg.showModal();
  }

  function openBatchProjectTagsDialog(centerSkills) {
    const dlg = document.getElementById("skillDialog");
    if (!dlg || !centerSkills.length) return;
    setupNativeDialog(dlg);

    let addStr = "";
    let removeSet = new Set();
    const existingTags = Array.from(new Set(centerSkills.flatMap((s) => s.tags || []))).sort();

    const bodyMount = h("div", { className: "sw-dialog-body" });
    const footerMount = h("div", { className: "sw-dialog-footer" });

    function renderForm() {
      bodyMount.replaceChildren(
        h("div", { style: { fontSize: "12.5px", color: "var(--muted)" } }, t(`将修改 ${centerSkills.length} 个关联中央技能的标签`, `Updating tags on ${centerSkills.length} linked central skill(s)`)),
        h(
          "div",
          { className: "sw-field" },
          h("label", null, t("新增标签（逗号分隔）", "Add Tags (comma-separated)")),
          h("input", {
            type: "text",
            className: "sw-input",
            value: addStr,
            placeholder: t("例如：frontend, ui", "e.g. frontend, ui"),
            onInput: (e) => {
              addStr = e.target.value;
            },
          })
        ),
        existingTags.length > 0 &&
          h(
            "div",
            { className: "sw-field" },
            h("label", null, t("点击选择要移除的现有标签", "Toggle existing tags to remove")),
            h(
              "div",
              { className: "sw-chips" },
              existingTags.map((tg) =>
                h(
                  "button",
                  {
                    type: "button",
                    className: "sw-chip",
                    "aria-pressed": removeSet.has(tg) ? "true" : "false",
                    onClick: () => {
                      if (removeSet.has(tg)) removeSet.delete(tg);
                      else removeSet.add(tg);
                      renderForm();
                    },
                  },
                  removeSet.has(tg) ? `✕ ${tg}` : tg
                )
              )
            )
          )
      );
    }

    footerMount.replaceChildren(
      h("span"),
      h(
        "div",
        { className: "sw-actions" },
        h("form", { method: "dialog", style: { display: "inline" } }, h("button", { type: "submit", className: "text" }, t("取消", "Cancel"))),
        h(
          "button",
          {
            type: "button",
            className: "text primary",
            onClick: async () => {
              const adds = addStr.split(/[,，]/).map((x) => x.trim()).filter(Boolean);
              let updated = 0;
              for (const cs of centerSkills) {
                const next = (cs.tags || []).filter((tg) => !removeSet.has(tg));
                for (const tg of adds) {
                  if (!next.includes(tg)) next.push(tg);
                }
                try {
                  await apiJSON(`/api/skills/center/${encodeURIComponent(cs.id)}/tags`, {
                    method: "POST",
                    body: { tags: next },
                  });
                  updated++;
                } catch {}
              }
              notify(t(`已更新 ${updated} 个技能标签`, `Updated tags on ${updated} skill(s)`), "ok");
              dlg.close("saved");
              await loadAll(false);
            },
          },
          t("应用标签变更", "Apply Tags")
        )
      )
    );

    dlg.replaceChildren(
      h(
        "div",
        { className: "sw-dialog-header" },
        h("h2", { id: "skillDialogTitle", className: "sw-dialog-title" }, t("批量编辑标签", "Batch Edit Tags")),
        h("form", { method: "dialog" }, h("button", { type: "submit", className: "text", "aria-label": t("关闭", "Close") }, "✕"))
      ),
      bodyMount,
      footerMount
    );
    renderForm();
    dlg.showModal();
  }

  function openProjectSkillDetailDialog(project, group) {
    const dlg = document.getElementById("skillDialog");
    if (!dlg) return;
    setupNativeDialog(dlg);

    const centerId = group.centerSkillIds[0] || null;
    const hasCenter = Boolean(centerId);
    let currentGroup = group;
    let activeTab = "local";
    let rawMode = false;
    let localDoc = null;
    let centerDoc = null;
    let localErr = "";
    let centerErr = "";
    let busyAgent = null;

    const agentGridMount = h("div", { style: { padding: "12px 18px", borderBottom: "1px solid var(--line)" } });
    const bodyMount = h("div", { className: "sw-dialog-body", style: { padding: 0 } });
    const footerMount = h("div", { className: "sw-dialog-footer" });
    const tabButtons = {};
    const panels = {};

    function setTab(nextTab) {
      activeTab = nextTab;
      for (const [k, btn] of Object.entries(tabButtons)) {
        const selected = k === activeTab;
        btn.setAttribute("aria-selected", selected ? "true" : "false");
        if (!("focusGroup" in HTMLElement.prototype)) btn.setAttribute("tabindex", selected ? "0" : "-1");
      }
      for (const [k, panel] of Object.entries(panels)) {
        if (k === activeTab) panel.removeAttribute("hidden");
        else panel.setAttribute("hidden", "until-found");
      }
      renderTabContents();
    }

    function renderDocPanel(targetInner, doc, errMsg, fallbackName) {
      if (errMsg) return targetInner.replaceChildren(h("div", { className: "sw-empty" }, errMsg));
      if (!doc) return targetInner.replaceChildren(h("div", { className: "sw-empty" }, t("正在加载文档…", "Loading document...")));
      const bar = h(
        "div",
        { className: "sw-row", style: { justifyContent: "space-between" } },
        h("span", { className: "sw-path-chip" }, doc.filename || fallbackName || "SKILL.md"),
        h(
          "button",
          {
            type: "button",
            className: rawMode ? "text on" : "text",
            onClick: () => {
              rawMode = !rawMode;
              renderTabContents();
            },
          },
          rawMode ? t("切换到渲染视图", "Rendered View") : t("查看源码", "Raw Source")
        )
      );
      const box = h("div");
      if (rawMode) box.replaceChildren(h("pre", { className: "sw-raw-pre" }, doc.content || ""));
      else renderMarkdownDocument(box, doc.content || "");
      targetInner.replaceChildren(bar, box);
    }

    function renderTabContents() {
      if (panels.local) renderDocPanel(panels.local.firstChild, localDoc, localErr, "Local SKILL.md");
      if (panels.diff) {
        const inner = panels.diff.firstChild;
        if (localErr || centerErr) inner.replaceChildren(h("div", { className: "sw-empty" }, localErr || centerErr));
        else if (!localDoc || !centerDoc) inner.replaceChildren(h("div", { className: "sw-empty" }, t("正在加载对比内容…", "Loading comparison...")));
        else if ((localDoc.content || "") === (centerDoc.content || "")) {
          inner.replaceChildren(h("div", { className: "sw-empty" }, t("项目版本与中央仓库内容完全一致。", "Project and Central SKILL.md are identical.")));
        } else {
          const rows = computeLineDiff(localDoc.content, centerDoc.content);
          inner.replaceChildren(
            h("div", { className: "sw-row", style: { fontSize: "12px", color: "var(--muted)" } }, t("红色 = 仅项目 / 绿色 = 仅中央仓库", "Red = Project / Green = Center")),
            h(
              "div",
              { className: "sw-diff" },
              rows.map((r) =>
                h(
                  "div",
                  { className: `sw-diff-line ${r.kind}` },
                  h("span", { className: "sw-diff-ln" }, r.la),
                  h("span", { className: "sw-diff-ln" }, r.lb),
                  h("span", null, r.kind === "add" ? "+" : r.kind === "del" ? "-" : " "),
                  h("span", null, r.text)
                )
              )
            )
          );
        }
      }
      if (panels.center) renderDocPanel(panels.center.firstChild, centerDoc, centerErr, "Center SKILL.md");
    }

    function renderAgentToggles() {
      const targets = (state.projectTargets || []).filter(
        (tg) => tg.installed || currentGroup.variants.some((v) => v.agent === tg.key)
      );
      const cards = targets.map((tg) => {
        const variant = currentGroup.variants.find((v) => v.agent === tg.key);
        const deployed = Boolean(variant);
        const usable = tg.installed && tg.enabled;
        const disabled = busyAgent !== null || (!deployed && !usable);
        const badge = !tg.installed
          ? t("未安装", "Not installed")
          : !tg.enabled
          ? t("全局禁用", "Disabled")
          : variant && !variant.enabled
          ? t("已停用", "Disabled")
          : null;

        return h(
          "div",
          { className: `sw-agent-toggle-card${!usable && !deployed ? " is-unavailable" : ""}` },
          h(
            "div",
            { style: { minWidth: 0 } },
            h("div", { style: { fontWeight: 600 } }, tg.display_name),
            h("div", { style: { fontSize: "11px", color: "var(--muted)" } }, badge || tg.relative_skills_dir || tg.key)
          ),
          h("button", {
            type: "button",
            role: "switch",
            className: "sw-toggle",
            "aria-checked": deployed ? "true" : "false",
            "aria-label": `${tg.display_name} toggle`,
            disabled,
            onClick: async () => {
              busyAgent = tg.key;
              renderAgentToggles();
              try {
                if (!deployed) {
                  if (!centerId) {
                    notify(t(`需要先把 "${currentGroup.name}" 导入中央仓库，才能部署到 ${tg.display_name}`, `Import "${currentGroup.name}" to Central Library before deploying to ${tg.display_name}`), "warn");
                    return;
                  }
                  await apiJSON(`/api/skills/projects/${encodeURIComponent(project.id)}/export`, {
                    method: "POST",
                    body: { skill_ids: [centerId], agents: [tg.key] },
                  });
                  notify(t(`已添加到 ${tg.display_name}`, `Added to ${tg.display_name}`), "ok");
                } else {
                  const confirmed = await confirmUnsyncedRemoval(project.id, [variant]);
                  if (!confirmed) return;
                  await apiJSON(`/api/skills/projects/${encodeURIComponent(project.id)}/delete-skill`, {
                    method: "POST",
                    body: { rel_path: variant.relative_path, agent: tg.key },
                  });
                  notify(t(`已从 ${tg.display_name} 移出`, `Removed from ${tg.display_name}`), "ok");
                }
                await loadAll(false);
                const updated = buildProjectSkillGroups(state.projectSkills).find((g) => g.id === currentGroup.id);
                if (!updated) {
                  dlg.close();
                  return;
                }
                currentGroup = updated;
                renderAgentToggles();
              } catch (err) {
                notify(err.message, "err");
              } finally {
                busyAgent = null;
                if (dlg.open) renderAgentToggles();
              }
            },
          })
        );
      });

      agentGridMount.replaceChildren(
        h("div", { style: { fontSize: "12px", fontWeight: 600, color: "var(--muted)", marginBottom: "8px" } }, t("各 Agent 目录部署状态", "Project Agent Targets")),
        h("div", { className: "sw-agent-toggle-grid" }, cards)
      );
    }

    const sm = statusMeta(group.status);
    const header = h(
      "div",
      { className: "sw-dialog-header" },
      h(
        "div",
        { style: { minWidth: 0 } },
        h("h2", { id: "skillDialogTitle", className: "sw-dialog-title" }, group.name),
        h(
          "div",
          { className: "sw-dialog-sub" },
          h("span", { className: `sw-badge ${sm.cls}` }, sm.label),
          h("span", { className: "sw-path-chip", title: group.primaryVariant.path }, group.primaryVariant.path),
          group.files?.length ? h("span", { className: "sw-count-pill" }, group.files.join(", ")) : null
        )
      ),
      h("form", { method: "dialog" }, h("button", { type: "submit", className: "text", "aria-label": t("关闭", "Close") }, "✕"))
    );

    const tabsSpec = [{ id: "local", label: t("项目文档", "Local Doc") }];
    if (hasCenter) {
      tabsSpec.push({ id: "diff", label: t("版本对比", "Diff") });
      tabsSpec.push({ id: "center", label: t("中央文档", "Center Doc") });
    }
    const tablist = h("div", {
      className: "sw-tablist",
      role: "tablist",
      focusgroup: "tablist nomemory",
      "aria-label": t("技能文档视图", "Skill Document Views"),
    });
    tabsSpec.forEach((spec, idx) => {
      const selected = spec.id === activeTab;
      const btn = h(
        "button",
        {
          type: "button",
          className: "sw-tab",
          role: "tab",
          id: `sw-ptab-${spec.id}`,
          "aria-selected": selected ? "true" : "false",
          "aria-controls": `sw-ppanel-${spec.id}`,
          focusgroupstart: idx === 0 ? true : false,
          tabindex: !("focusGroup" in HTMLElement.prototype) && !selected ? "-1" : false,
          onClick: () => setTab(spec.id),
        },
        spec.label
      );
      tabButtons[spec.id] = btn;
      tablist.appendChild(btn);

      const panel = h(
        "div",
        {
          id: `sw-ppanel-${spec.id}`,
          role: "tabpanel",
          "aria-labelledby": `sw-ptab-${spec.id}`,
          hidden: selected ? false : "until-found",
        },
        h("div", { className: "sw-tabpanel-inner" })
      );
      panel.addEventListener("beforematch", () => setTab(spec.id));
      panels[spec.id] = panel;
      bodyMount.appendChild(panel);
    });
    if (!("focusGroup" in HTMLElement.prototype)) {
      tablist.addEventListener("keydown", (e) => {
        const list = Object.values(tabButtons);
        const cur = list.indexOf(document.activeElement);
        if (cur < 0) return;
        let next = -1;
        if (e.key === "ArrowRight") next = (cur + 1) % list.length;
        else if (e.key === "ArrowLeft") next = (cur - 1 + list.length) % list.length;
        else if (e.key === "Home") next = 0;
        else if (e.key === "End") next = list.length - 1;
        if (next >= 0) {
          e.preventDefault();
          list[next].focus();
          setTab(tabsSpec[next].id);
        }
      });
    }

    footerMount.replaceChildren(
      h("button", { type: "button", className: "text", onClick: () => copyText(group.primaryVariant.path) }, t("复制路径", "Copy Path")),
      h(
        "div",
        { className: "sw-actions" },
        h("form", { method: "dialog", style: { display: "inline" } }, h("button", { type: "submit", className: "text" }, t("关闭", "Close")))
      )
    );

    dlg.replaceChildren(header, agentGridMount, tablist, bodyMount, footerMount);
    renderAgentToggles();
    setTab("local");
    dlg.showModal();

    apiJSON(
      `/api/skills/projects/${encodeURIComponent(project.id)}/document?rel_path=${encodeURIComponent(
        group.primaryVariant.relative_path
      )}&agent=${encodeURIComponent(group.primaryVariant.agent)}`
    )
      .then((d) => {
        localDoc = d;
        renderTabContents();
      })
      .catch((err) => {
        localErr = err.message;
        renderTabContents();
      });

    if (centerId) {
      apiJSON(`/api/skills/center/${encodeURIComponent(centerId)}/document`)
        .then((d) => {
          centerDoc = d;
          renderTabContents();
        })
        .catch((err) => {
          centerErr = err.message;
          renderTabContents();
        });
    }
  }

  function openSettingsDialog() {
    const dlg = document.getElementById("skillDialog");
    if (!dlg) return;
    setupNativeDialog(dlg);

    let agentSearch = "";
    let customKey = "";
    let customName = "";
    let customDir = "";
    let customCat = "coding";
    let customRecursive = false;
    const pathDrafts = new Map();

    const bodyMount = h("div", { className: "sw-dialog-body" });
    const footerMount = h("div", { className: "sw-dialog-footer" });

    async function callSettings(payload, okMsg) {
      try {
        await apiJSON("/api/skills/settings", { method: "POST", body: payload });
        notify(okMsg, "ok");
        await loadAll(true);
        renderSettingsBody();
      } catch (err) {
        notify(err.message, "err");
      }
    }

    function renderSettingsBody() {
      const mode = state.overview.sync_mode || "symlink";
      const syncModeCard = h(
        "div",
        { className: "sw-card", style: { padding: "12px" } },
        h(
          "div",
          { className: "sw-toolbar" },
          h(
            "div",
            null,
            h("div", { style: { fontWeight: 700, fontSize: "13.5px" } }, t("同步方式 (Sync Mode)", "Default Sync Mode")),
            h(
              "div",
              { style: { fontSize: "12px", color: "var(--muted)" } },
              t("Symlink（软链共享中央仓库）/ Copy（独立目录复制）", "Symlink shares central directory / Copy duplicates files per agent")
            )
          ),
          h(
            "div",
            { className: "sw-actions" },
            h(
              "button",
              {
                type: "button",
                className: mode === "symlink" ? "text primary" : "text",
                onClick: () => callSettings({ action: "set_sync_mode", sync_mode: "symlink" }, t("已切换为 Symlink 模式", "Sync mode: symlink")),
              },
              "Symlink"
            ),
            h(
              "button",
              {
                type: "button",
                className: mode === "copy" ? "text primary" : "text",
                onClick: () => callSettings({ action: "set_sync_mode", sync_mode: "copy" }, t("已切换为 Copy 模式", "Sync mode: copy")),
              },
              "Copy"
            )
          )
        )
      );

      // Add custom agent section
      const addCustomBox = h(
        "div",
        { className: "sw-card", style: { padding: "12px" } },
        h("div", { style: { fontWeight: 700, fontSize: "13.5px", marginBottom: "8px" } }, t("新增自定义 Agent", "Add Custom Agent")),
        h(
          "div",
          { className: "sw-row" },
          h("input", {
            type: "text",
            className: "sw-input",
            style: { width: "120px" },
            value: customKey,
            placeholder: t("标识 key", "key (e.g. my-agent)"),
            onInput: (e) => {
              customKey = e.target.value;
            },
          }),
          h("input", {
            type: "text",
            className: "sw-input",
            style: { width: "140px" },
            value: customName,
            placeholder: t("显示名称", "Display name"),
            onInput: (e) => {
              customName = e.target.value;
            },
          }),
          h("input", {
            type: "text",
            className: "sw-input",
            style: { flex: 1, minWidth: "170px" },
            value: customDir,
            placeholder: t("Skills 目录 (如 ~/.myagent/skills)", "Skills directory"),
            onInput: (e) => {
              customDir = e.target.value;
            },
          }),
          h(
            "select",
            {
              className: "sw-select",
              value: customCat,
              onChange: (e) => {
                customCat = e.target.value;
              },
            },
            h("option", { value: "coding" }, "Coding"),
            h("option", { value: "lobster" }, "Lobster")
          ),
          h(
            "label",
            { className: "sw-row", style: { fontSize: "12px" } },
            h("input", {
              type: "checkbox",
              checked: customRecursive,
              onChange: (e) => {
                customRecursive = e.target.checked;
              },
            }),
            t("递归扫描", "Recursive")
          ),
          h(
            "button",
            {
              type: "button",
              className: "text primary",
              onClick: async () => {
                await callSettings(
                  {
                    action: "add_custom_tool",
                    key: customKey.trim(),
                    display_name: customName.trim(),
                    skills_dir: customDir.trim(),
                    category: customCat,
                    recursive_scan: customRecursive,
                  },
                  t("已添加自定义 Agent", "Custom agent added")
                );
                customKey = "";
                customName = "";
                customDir = "";
              },
            },
            t("添加", "Add")
          )
        )
      );

      // List locally detected agents by default; search across all adapters when query is non-empty
      const q = agentSearch.trim().toLowerCase();
      const filteredAgents = (state.overview.agents || []).filter((a) => {
        if (!q) return a.installed;
        return (
          a.key.toLowerCase().includes(q) ||
          a.display_name.toLowerCase().includes(q) ||
          (a.display_skills_dir || "").toLowerCase().includes(q)
        );
      });

      const agentRows = filteredAgents.map((a) => {
        const draftPath = pathDrafts.has(a.key) ? pathDrafts.get(a.key) : a.skills_dir;
        const pathInput = h("input", {
          type: "text",
          className: "sw-input",
          style: { flex: 1, minWidth: "180px", fontFamily: "var(--code)", fontSize: "12px" },
          value: draftPath || "",
          onInput: (e) => {
            pathDrafts.set(a.key, e.target.value);
          },
        });
        return h(
          "div",
          { className: "sw-card", style: { padding: "10px 12px" } },
          h(
            "div",
            { className: "sw-toolbar" },
            h(
              "div",
              { className: "sw-row" },
              renderIcon(a.icon, a.display_name),
              h("strong", null, a.display_name),
              h("span", { className: "sw-count-pill" }, a.key),
              h("span", { className: `sw-badge ${a.installed ? "ok" : "muted"}` }, a.installed ? t("已检测到", "Installed") : t("未安装", "Not Detected")),
              a.category === "lobster" && h("span", { className: "sw-badge no-dot accent" }, "Lobster"),
              a.is_custom && h("span", { className: "sw-badge no-dot accent" }, t("自定义", "Custom"))
            ),
            h(
              "div",
              { className: "sw-actions" },
              h(
                "button",
                {
                  type: "button",
                  className: a.enabled ? "text on" : "text",
                  onClick: () =>
                    callSettings(
                      { action: "set_tool_enabled", key: a.key, enabled: !a.enabled },
                      a.enabled ? t(`已禁用 ${a.display_name}`, `Disabled ${a.display_name}`) : t(`已启用 ${a.display_name}`, `Enabled ${a.display_name}`)
                    ),
                },
                a.enabled ? t("已启用", "Enabled") : t("已禁用", "Disabled")
              ),
              a.is_custom &&
                h(
                  "button",
                  {
                    type: "button",
                    className: "text sw-btn-danger",
                    onClick: () => callSettings({ action: "remove_custom_tool", key: a.key }, t("已移除自定义 Agent", "Custom agent removed")),
                  },
                  t("移除", "Remove")
                )
            )
          ),
          h(
            "div",
            { className: "sw-row", style: { marginTop: "8px" } },
            pathInput,
            h(
              "button",
              {
                type: "button",
                className: "text",
                onClick: () => {
                  const val = pathDrafts.has(a.key) ? pathDrafts.get(a.key) : a.skills_dir;
                  callSettings({ action: "set_tool_path", key: a.key, skills_dir: val }, t("路径已覆盖更新", "Path updated"));
                },
              },
              t("保存路径", "Save Path")
            ),
            a.has_path_override &&
              h(
                "button",
                {
                  type: "button",
                  className: "text",
                  onClick: () => {
                    pathDrafts.delete(a.key);
                    callSettings({ action: "reset_tool_path", key: a.key }, t("已恢复默认路径", "Path reset to default"));
                  },
                },
                t("恢复默认", "Reset")
              )
          )
        );
      });

      const listHeader = h(
        "div",
        null,
        h(
          "div",
          { className: "sw-toolbar" },
          h("strong", null, t(`Agent 适配器注册表 (${filteredAgents.length})`, `Agents (${filteredAgents.length})`)),
          makeSearchInput({
            focusKey: "settings-agent-search",
            value: agentSearch,
            placeholder: t("按名称 / key / 路径搜索…", "Search agents..."),
            onUpdate: (val, shouldRender) => {
              agentSearch = val;
              if (shouldRender) renderSettingsBody();
            },
          })
        ),
        h(
          "div",
          { style: { fontSize: "12px", color: "var(--muted)", marginTop: "4px" } },
          t("搜索可查找未检测到的 Agent 并配置自定义路径", "Search to find undetected agents and configure custom paths")
        )
      );

      withPreservedFocus(dlg, () => {
        bodyMount.replaceChildren(syncModeCard, addCustomBox, listHeader, h("div", { className: "sw-list" }, agentRows));
      });
    }

    const header = h(
      "div",
      { className: "sw-dialog-header" },
      h(
        "div",
        null,
        h("h2", { id: "skillDialogTitle", className: "sw-dialog-title" }, t("全局 Agent 与同步设置", "Agent & Sync Settings")),
        h("div", { className: "sw-dialog-sub" }, h("span", { className: "sw-path-chip" }, state.overview.central_dir || ""))
      ),
      h(
        "form",
        { method: "dialog" },
        h("button", { type: "submit", className: "text", "aria-label": t("关闭", "Close") }, "✕")
      )
    );

    footerMount.replaceChildren(
      h("span", { style: { fontSize: "12px", color: "var(--muted)" } }, t("禁用内置 Agent 会清理该 Agent 的受管部署记录但保留中央副本", "Disabling an agent cleans up managed deployments without deleting central copies")),
      h(
        "form",
        { method: "dialog" },
        h("button", { type: "submit", className: "text primary" }, t("完成", "Done"))
      )
    );

    dlg.replaceChildren(header, bodyMount, footerMount);
    renderSettingsBody();
    dlg.showModal();
  }

  // -------------------- Rendering Sections --------------------
  function renderSidebar() {
    const codingAll = orderedAgents("coding").filter((a) => a.enabled);
    const lobsterAll = orderedAgents("lobster").filter((a) => a.enabled);
    const codingInstalled = codingAll.filter((a) => a.installed);
    const lobsterInstalled = lobsterAll.filter((a) => a.installed);

    const codingVisible = codingInstalled;
    const lobsterVisible = lobsterInstalled;

    const renderAgentBtn = (a) => {
      const prefix = a.category === "lobster" ? "#/lobster-workspace/" : "#/global-workspace/";
      const active = state.selectedAgentKey === a.key && state.routeKind !== "library";
      return h(
        "button",
        {
          type: "button",
          className: `sw-agent-item${a.installed ? "" : " is-uninstalled"}`,
          "aria-current": active ? "page" : undefined,
          onClick: () => navigateTo(prefix + encodeURIComponent(a.key)),
        },
        h(
          "span",
          { className: "sw-agent-item-left" },
          renderIcon(a.icon, a.display_name),
          h("span", { className: "sw-agent-item-name" }, a.display_name)
        ),
        h(
          "span",
          { className: "sw-count-pill" },
          a.local_count_scanned ? String(a.local_count) : a.installed ? "0" : "—"
        )
      );
    };

    const codingSection = h(
      "div",
      { className: "sw-nav-section" },
      h(
        "div",
        { className: "sw-nav-header" },
        h(
          "button",
          {
            type: "button",
            className: "sw-nav-root",
            "aria-current": state.routeKind === "global" && !state.selectedAgentKey ? "page" : undefined,
            onClick: () => navigateTo("#/global-workspace"),
          },
          h("span", null, t("全局工作区", "Global Workspace")),
          h("span", { className: "sw-count-pill" }, `${codingInstalled.length}`)
        ),
        h(
          "button",
          {
            type: "button",
            className: "sw-nav-collapse",
            "aria-expanded": String(!state.collapseCoding),
            title: t("展开/折叠", "Toggle list"),
            onClick: () => {
              state.collapseCoding = !state.collapseCoding;
              render();
            },
          },
          state.collapseCoding ? "▸" : "▾"
        )
      ),
      h(
        "div",
        { className: "sw-nav-list", hidden: state.collapseCoding },
        codingVisible.length
          ? codingVisible.map(renderAgentBtn)
          : h("div", { className: "sw-empty-hint" }, t("本地未检测到 Agent", "No agents detected"))
      )
    );

    const lobsterSection = h(
      "div",
      { className: "sw-nav-section" },
      h(
        "div",
        { className: "sw-nav-header" },
        h(
          "button",
          {
            type: "button",
            className: "sw-nav-root",
            "aria-current": state.routeKind === "lobster" && !state.selectedAgentKey ? "page" : undefined,
            onClick: () => navigateTo("#/lobster-workspace"),
          },
          h("span", null, t("Lobster Agent 矩阵", "Lobster Agents")),
          h("span", { className: "sw-count-pill" }, `${lobsterInstalled.length}`)
        ),
        h(
          "button",
          {
            type: "button",
            className: "sw-nav-collapse",
            "aria-expanded": String(!state.collapseLobster),
            title: t("展开/折叠", "Toggle list"),
            onClick: () => {
              state.collapseLobster = !state.collapseLobster;
              render();
            },
          },
          state.collapseLobster ? "▸" : "▾"
        )
      ),
      h(
        "div",
        { className: "sw-nav-list", hidden: state.collapseLobster },
        lobsterVisible.length
          ? lobsterVisible.map(renderAgentBtn)
          : h("div", { className: "sw-empty-hint" }, t("本地未检测到 Agent", "No agents detected"))
      )
    );

    const projects = state.projects || [];
    const projectsSection = h(
      "div",
      { className: "sw-nav-section" },
      h(
        "div",
        { className: "sw-nav-header" },
        h(
          "button",
          {
            type: "button",
            className: "sw-nav-root",
            "aria-current": state.routeKind === "projects" && !state.selectedProjectId ? "page" : undefined,
            onClick: () => navigateTo("#/projects"),
          },
          h("span", null, t("项目工作区", "Projects")),
          h("span", { className: "sw-count-pill" }, String(projects.length))
        ),
        h(
          "button",
          {
            type: "button",
            className: "sw-nav-collapse",
            "aria-expanded": String(!state.collapseProjects),
            title: t("展开/折叠", "Toggle list"),
            onClick: () => {
              state.collapseProjects = !state.collapseProjects;
              render();
            },
          },
          state.collapseProjects ? "▸" : "▾"
        )
      ),
      h(
        "div",
        { className: "sw-nav-list", hidden: state.collapseProjects },
        projects.map((p, idx) => {
          const active = state.routeKind === "project" && state.selectedProjectId === p.id;
          const isLinked = p.workspace_type === "linked";
          return h(
            "div",
            { className: "sw-project-row" },
            h(
              "button",
              {
                type: "button",
                className: "sw-agent-item",
                "aria-current": active ? "page" : undefined,
                title: p.display_path,
                onClick: () => navigateTo("#/project/" + encodeURIComponent(p.id)),
              },
              h(
                "span",
                { className: "sw-agent-item-left" },
                renderIcon("", isLinked ? "L" : "P"),
                h("span", { className: "sw-agent-item-name" }, p.name)
              ),
              h(
                "span",
                { className: "sw-project-metrics" },
                h("span", { className: `sw-health-dot ${projectHealthDotClass(p)}`, title: p.display_path }),
                p.skill_count > 0 && h("span", { className: "sw-count-pill" }, String(p.skill_count))
              )
            ),
            h(
              "div",
              { className: "sw-project-acts" },
              idx > 0 &&
                h(
                  "button",
                  {
                    type: "button",
                    className: "sw-preset-icon-btn",
                    title: t("上移", "Move Up"),
                    "aria-label": t(`上移 ${p.name}`, `Move ${p.name} up`),
                    onClick: (e) => {
                      e.stopPropagation();
                      handleReorderProject(idx, -1);
                    },
                  },
                  "↑"
                ),
              idx < projects.length - 1 &&
                h(
                  "button",
                  {
                    type: "button",
                    className: "sw-preset-icon-btn",
                    title: t("下移", "Move Down"),
                    "aria-label": t(`下移 ${p.name}`, `Move ${p.name} down`),
                    onClick: (e) => {
                      e.stopPropagation();
                      handleReorderProject(idx, 1);
                    },
                  },
                  "↓"
                ),
              h(
                "button",
                {
                  type: "button",
                  className: "sw-preset-icon-btn",
                  title: t("移除工作区", "Remove Workspace"),
                  "aria-label": t(`移除 ${p.name}`, `Remove ${p.name}`),
                  onClick: (e) => {
                    e.stopPropagation();
                    handleRemoveProject(p);
                  },
                },
                "✕"
              )
            )
          );
        }),
        h(
          "button",
          {
            type: "button",
            className: "sw-agent-toggle-all",
            onClick: () => openAddProjectDialog(),
          },
          t("+ 添加项目 / 关联目录", "+ Add Project / Linked")
        )
      )
    );

    const librarySection = h(
      "div",
      { className: "sw-nav-section" },
      h(
        "button",
        {
          type: "button",
          className: "sw-nav-root",
          "aria-current": state.routeKind === "library" ? "page" : undefined,
          onClick: () => navigateTo("#/skills-library"),
        },
        h("span", null, t("中央 Skills 仓库", "Central Library")),
        h("span", { className: "sw-count-pill" }, String((state.centerSkills || []).length))
      )
    );

    const footer = h(
      "div",
      { className: "sw-sidebar-footer" },
      h(
        "button",
        {
          type: "button",
          className: "text",
          style: { width: "100%", textAlign: "left" },
          onClick: () => openSettingsDialog(),
        },
        t("⚙ Agent 与同步设置", "⚙ Agent & Sync Settings")
      ),
      state.overview.central_dir &&
        h(
          "div",
          {
            className: "sw-path-chip",
            title: state.overview.central_dir,
            style: { cursor: "pointer" },
            onClick: () => copyText(state.overview.central_dir),
          },
          state.overview.central_dir
        )
    );

    return h("aside", { className: "sw-sidebar", "aria-label": t("全局工作区导航", "Workspace Navigation") }, codingSection, lobsterSection, projectsSection, librarySection, footer);
  }

  function renderPresetBar(scopedAgentKey = null) {
    const deployedSet = scopedAgentKey
      ? new Set(state.agentSkills.filter((s) => s.managed && s.center_skill_id).map((s) => s.center_skill_id))
      : null;

    const pills = (state.presets || []).map((preset) => {
      const total = (preset.skill_ids || []).length;
      let statusCls = "";
      let countBadge = `${total}`;
      if (deployedSet && total > 0) {
        const applied = preset.skill_ids.filter((id) => deployedSet.has(id)).length;
        if (applied === total) statusCls = " is-full";
        else if (applied > 0) statusCls = " is-partial";
        countBadge = `${applied}/${total}`;
      }
      return h(
        "div",
        { className: `sw-preset-pill${statusCls}` },
        h(
          "button",
          {
            type: "button",
            className: "sw-preset-apply",
            title: scopedAgentKey
              ? t("点击应用或移出该 Preset", "Click to apply or remove preset")
              : t("点击一键应用到当前分类已启用 Agent", "Apply preset to active agents"),
            onClick: () => handlePresetClick(preset, scopedAgentKey),
          },
          h("span", null, preset.name),
          h("span", { className: "sw-count-pill" }, countBadge)
        ),
        h(
          "button",
          {
            type: "button",
            className: "sw-preset-icon-btn",
            title: t("编辑 Preset", "Edit Preset"),
            "aria-label": t(`编辑 ${preset.name}`, `Edit ${preset.name}`),
            onClick: () => openPresetDialog(preset),
          },
          "✎"
        ),
        h(
          "button",
          {
            type: "button",
            className: "sw-preset-icon-btn",
            title: t("删除 Preset", "Delete Preset"),
            "aria-label": t(`删除 ${preset.name}`, `Delete ${preset.name}`),
            onClick: async () => {
              const ok = await showConfirm({
                title: t(`删除 Preset "${preset.name}"？`, `Delete preset "${preset.name}"?`),
                message: t("删除 Preset 不会影响已同步部署的技能。", "Deleting a preset keeps existing skill deployments intact."),
                confirmLabel: t("删除", "Delete"),
                danger: true,
              });
              if (!ok) return;
              try {
                await apiJSON(`/api/skills/presets/${encodeURIComponent(preset.id)}`, { method: "DELETE" });
                notify(t("Preset 已删除", "Preset deleted"), "ok");
                await loadAll(true);
              } catch (err) {
                notify(err.message, "err");
              }
            },
          },
          "✕"
        )
      );
    });

    const seedIDs = scopedAgentKey
      ? Array.from(deployedSet || [])
      : null;

    return h(
      "div",
      { className: "sw-preset-bar" },
      h("span", { className: "sw-preset-label" }, t("Presets 预设组合:", "Presets:")),
      ...pills,
      h(
        "button",
        {
          type: "button",
          className: "text",
          onClick: () => openPresetDialog(null, seedIDs),
        },
        t("+ 新建 Preset", "+ New Preset")
      )
    );
  }

  function renderConflictNotice() {
    if (!state.lastConflict || (!state.lastConflict.conflicts?.length && !state.lastConflict.failures?.length)) {
      return null;
    }
    return h(
      "div",
      { className: "sw-conflict-box" },
      h(
        "div",
        { className: "sw-toolbar" },
        h("strong", null, t("同步保护拦截了目标冲突或错误：", "Sync Conflicts / Errors:")),
        h(
          "button",
          {
            type: "button",
            className: "text",
            onClick: () => {
              state.lastConflict = null;
              render();
            },
          },
          t("关闭提示", "Dismiss")
        )
      ),
      (state.lastConflict.conflicts || []).map((c) => h("div", null, `${c.path}: ${c.reason}`)),
      (state.lastConflict.failures || []).map((f) => h("div", null, `${f.agent || ""} ${f.skill_id || ""}: ${f.error}`))
    );
  }

  function renderOverviewMain(category) {
    const allCatAgents = orderedAgents(category);
    const enabledAgents = allCatAgents.filter((a) => a.enabled);
    const installedAgents = enabledAgents.filter((a) => a.installed);
    const installedCount = installedAgents.length;
    const totalLocalSkills = installedAgents.reduce((acc, a) => acc + (a.local_count || 0), 0);
    const totalManagedSkills = installedAgents.reduce((acc, a) => acc + (a.managed_count || 0), 0);

    const q = state.searchAgent.trim().toLowerCase();
    const visibleAgents = enabledAgents.filter((a) => {
      if (!a.installed) return false;
      if (!q) return true;
      return (
        a.display_name.toLowerCase().includes(q) ||
        a.key.toLowerCase().includes(q) ||
        (a.display_skills_dir || "").toLowerCase().includes(q)
      );
    });

    const headerCard = h(
      "div",
      { className: "sw-card" },
      h(
        "div",
        { className: "sw-header" },
        h(
          "div",
          { className: "sw-header-title-wrap" },
          h(
            "div",
            null,
            h(
              "h2",
              { className: "sw-header-title" },
              category === "lobster"
                ? t("Lobster Agents 全局工作区总览", "Lobster Agents Workspace Overview")
                : t("全局 Agent Skills 总览", "Global Coding Agents Overview")
            ),
            h(
              "div",
              { className: "sw-header-meta" },
              h("span", null, t(`本地已检测: ${installedCount}`, `Detected: ${installedCount}`)),
              h("span", null, "·"),
              h("span", null, t(`本地技能总数: ${totalLocalSkills}`, `Local skills: ${totalLocalSkills}`)),
              h("span", null, "·"),
              h("span", null, t(`中央纳管部署: ${totalManagedSkills}`, `Managed links: ${totalManagedSkills}`)),
              h("span", null, "·"),
              h("span", { className: "sw-badge no-dot" }, `${t("模式", "Mode")}: ${state.overview.sync_mode || "symlink"}`)
            )
          )
        ),
        h(
          "div",
          { className: "sw-actions" },
          h(
            "button",
            {
              type: "button",
              className: "text primary",
              onClick: () => openAddSkillsDialog(null),
            },
            t("+ 批量同步 Skills", "+ Add / Deploy Skills")
          ),
          h(
            "button",
            {
              type: "button",
              className: "text",
              onClick: () => loadAll(true),
            },
            t("刷新扫描", "Refresh")
          )
        )
      )
    );

    const filterBar = h(
      "div",
      { className: "sw-toolbar" },
      makeSearchInput({
        focusKey: "overview-agent-search",
        value: state.searchAgent,
        placeholder: t("搜索 Agent 名称、key 或目录路径…", "Search agents by name, key, or directory..."),
        onUpdate: (val, shouldRender) => {
          state.searchAgent = val;
          if (shouldRender) render();
        },
      }),
      h(
        "div",
        { className: "sw-actions" },
        h(
          "button",
          {
            type: "button",
            className: "text",
            onClick: () => openSettingsDialog(),
          },
          t("配置 Agent 与路径", "Configure Agents")
        )
      )
    );

    const hashBase = category === "lobster" ? "#/lobster-workspace/" : "#/global-workspace/";
    const cards = visibleAgents.map((a) =>
      h(
        "div",
        { className: `sw-agent-card${a.installed ? "" : " is-uninstalled"}` },
        h(
          "div",
          { className: "sw-agent-card-top" },
          h(
            "div",
            { className: "sw-row", style: { minWidth: 0 } },
            renderIcon(a.icon, a.display_name, true),
            h(
              "div",
              { style: { minWidth: 0 } },
              h("div", { style: { fontWeight: 700, fontSize: "14.5px" } }, a.display_name),
              h("div", { style: { fontSize: "11.5px", color: "var(--muted)" } }, a.key)
            )
          ),
          h(
            "div",
            { className: "sw-chips" },
            a.recursive_scan && h("span", { className: "sw-badge no-dot accent" }, t("递归", "Recursive")),
            a.has_path_override && h("span", { className: "sw-badge no-dot warn" }, t("自定义路径", "Custom Path")),
            h("span", { className: `sw-badge ${a.installed ? "ok" : "muted"}` }, a.installed ? t("已安装", "Installed") : t("未检测到", "Not Installed"))
          )
        ),
        h("div", { className: "sw-path-chip", title: a.display_skills_dir }, a.display_skills_dir || "—"),
        h(
          "div",
          { className: "sw-agent-card-metrics" },
          h("span", null, t(`本地技能: ${a.local_count}`, `Local: ${a.local_count}`)),
          h("span", null, t(`中央纳管: ${a.managed_count}`, `Managed: ${a.managed_count}`))
        ),
        h(
          "div",
          { className: "sw-skill-actions" },
          h(
            "button",
            {
              type: "button",
              className: "text primary",
              onClick: () => navigateTo(hashBase + encodeURIComponent(a.key)),
            },
            t("进入工作区", "Open Workspace")
          ),
          a.installed &&
            h(
              "button",
              {
                type: "button",
                className: "text",
                onClick: () => openAddSkillsDialog(a.key),
              },
              t("+ 添加 Skills", "+ Add Skills")
            )
        )
      )
    );

    const gridEl = visibleAgents.length
      ? h("div", { className: "sw-grid" }, cards)
      : h(
          "div",
          { className: "sw-empty" },
          q
            ? t("没有匹配的 Agent", "No matching agents")
            : t("本地未检测到该分类的 Agent", "No agents detected locally")
        );

    return h("section", { className: "sw-main" }, headerCard, renderPresetBar(null), renderConflictNotice(), filterBar, gridEl);
  }

  function renderAgentDetailMain(agent) {
    const backHash = agent.category === "lobster" ? "#/lobster-workspace" : "#/global-workspace";
    const skills = state.agentSkills || [];

    const counts = {
      all: skills.length,
      in_sync: 0,
      center_newer: 0,
      project_newer: 0,
      diverged: 0,
      project_only: 0,
    };
    const tagSet = new Set();
    for (const s of skills) {
      if (counts[s.sync_status] !== undefined) counts[s.sync_status]++;
      else counts.project_only++;
      (s.tags || []).forEach((tg) => tagSet.add(tg));
    }

    const q = state.searchSkill.trim().toLowerCase();
    const visibleSkills = skills.filter((s) => {
      if (state.statusFilter !== "all" && s.sync_status !== state.statusFilter) return false;
      if (state.tagFilter !== "all" && !(s.tags || []).includes(state.tagFilter)) return false;
      if (!q) return true;
      return (
        s.name.toLowerCase().includes(q) ||
        (s.description || "").toLowerCase().includes(q) ||
        (s.relative_path || "").toLowerCase().includes(q) ||
        (s.tags || []).some((tg) => tg.toLowerCase().includes(q))
      );
    });

    const headerCard = h(
      "div",
      { className: "sw-card" },
      h(
        "div",
        { className: "sw-header" },
        h(
          "div",
          { className: "sw-header-title-wrap" },
          renderIcon(agent.icon, agent.display_name, true),
          h(
            "div",
            { style: { minWidth: 0 } },
            h(
              "div",
              { className: "sw-row" },
              h(
                "button",
                {
                  type: "button",
                  className: "text",
                  onClick: () => navigateTo(backHash),
                },
                t("← 总览", "← All Agents")
              ),
              h("h2", { className: "sw-header-title" }, agent.display_name),
              h("span", { className: `sw-badge ${agent.installed ? "ok" : "muted"}` }, agent.installed ? t("已安装", "Installed") : t("未检测到", "Not Installed")),
              agent.recursive_scan && h("span", { className: "sw-badge no-dot accent" }, t("递归子目录扫描", "Recursive Scan"))
            ),
            h(
              "div",
              { className: "sw-header-meta" },
              h(
                "span",
                {
                  className: "sw-path-chip",
                  title: t("点击复制目录路径", "Click to copy"),
                  style: { cursor: "pointer" },
                  onClick: () => copyText(agent.display_skills_dir),
                },
                agent.display_skills_dir
              ),
              h("span", null, t(`共 ${skills.length} 个全局技能`, `${skills.length} skill(s)`))
            )
          )
        ),
        h(
          "div",
          { className: "sw-actions" },
          h(
            "button",
            {
              type: "button",
              className: "text primary",
              onClick: () => openAddSkillsDialog(agent.key),
            },
            t("+ 添加中央 Skills", "+ Add Skills")
          ),
          h(
            "button",
            {
              type: "button",
              className: "text",
              onClick: () => loadAll(true),
            },
            t("重新扫描", "Refresh")
          )
        )
      )
    );

    // Status filter chips
    const statusChipsData = [
      { key: "all", label: t("全部", "All"), n: counts.all },
      { key: "in_sync", label: t("已同步", "In Sync"), n: counts.in_sync },
      { key: "center_newer", label: t("中心有更新", "Center Newer"), n: counts.center_newer },
      { key: "project_newer", label: t("本地有更新", "Local Newer"), n: counts.project_newer },
      { key: "diverged", label: t("已分歧", "Diverged"), n: counts.diverged },
      { key: "project_only", label: t("仅本地", "Local Only"), n: counts.project_only },
    ];

    const filterToolbar = h(
      "div",
      { className: "sw-toolbar" },
      h(
        "div",
        { className: "sw-chips" },
        statusChipsData.map((st) =>
          h(
            "button",
            {
              type: "button",
              className: "sw-chip",
              "aria-pressed": state.statusFilter === st.key ? "true" : "false",
              onClick: () => {
                state.statusFilter = st.key;
                render();
              },
            },
            `${st.label} (${st.n})`
          )
        )
      ),
      h(
        "div",
        { className: "sw-actions" },
        makeSearchInput({
          focusKey: "detail-skill-search",
          value: state.searchSkill,
          placeholder: t("按技能名 / 路径 / 描述筛选…", "Filter skills..."),
          onUpdate: (val, shouldRender) => {
            state.searchSkill = val;
            if (shouldRender) render();
          },
        }),
        h(
          "button",
          {
            type: "button",
            className: "text",
            onClick: () => {
              state.viewMode = state.viewMode === "grid" ? "list" : "grid";
              localStorage.setItem("ctxview.skills.viewMode", state.viewMode);
              render();
            },
          },
          state.viewMode === "grid" ? t("列表视图", "List View") : t("网格视图", "Grid View")
        )
      )
    );

    const tagsRow = tagSet.size
      ? h(
          "div",
          { className: "sw-chips" },
          h(
            "button",
            {
              type: "button",
              className: "sw-chip",
              "aria-pressed": state.tagFilter === "all" ? "true" : "false",
              onClick: () => {
                state.tagFilter = "all";
                render();
              },
            },
            t("全部标签", "All Tags")
          ),
          Array.from(tagSet)
            .sort()
            .map((tg) =>
              h(
                "button",
                {
                  type: "button",
                  className: "sw-chip",
                  "aria-pressed": state.tagFilter === tg ? "true" : "false",
                  onClick: () => {
                    state.tagFilter = tg;
                    render();
                  },
                },
                tg
              )
            )
        )
      : null;

    // Multi-select bar splitting Managed Unsync vs Unmanaged Delete Local
    const selectedSkills = skills.filter((s) => state.selectedSkillRelPaths.has(s.relative_path));
    const managedSelected = selectedSkills.filter((s) => s.managed && s.center_skill_id);
    const unmanagedSelected = selectedSkills.filter((s) => !s.managed && !s.in_center);

    const batchBar = h(
      "div",
      { className: selectedSkills.length ? "sw-batch-bar" : "sw-toolbar" },
      h(
        "div",
        { className: "sw-row" },
        h(
          "button",
          {
            type: "button",
            className: "text",
            onClick: () => {
              const allVisibleSelected =
                visibleSkills.length > 0 && visibleSkills.every((s) => state.selectedSkillRelPaths.has(s.relative_path));
              if (allVisibleSelected) {
                visibleSkills.forEach((s) => state.selectedSkillRelPaths.delete(s.relative_path));
              } else {
                visibleSkills.forEach((s) => state.selectedSkillRelPaths.add(s.relative_path));
              }
              render();
            },
          },
          visibleSkills.length > 0 && visibleSkills.every((s) => state.selectedSkillRelPaths.has(s.relative_path))
            ? t("取消全选当前列表", "Deselect Visible")
            : t(`全选当前列表 (${visibleSkills.length})`, `Select Visible (${visibleSkills.length})`)
        ),
        selectedSkills.length > 0 &&
          h("span", { style: { fontWeight: 600, fontSize: "12.5px" } }, t(`已选 ${selectedSkills.length} 项`, `${selectedSkills.length} selected`))
      ),
      selectedSkills.length > 0 &&
        h(
          "div",
          { className: "sw-actions" },
          managedSelected.length > 0 &&
            h(
              "button",
              {
                type: "button",
                className: "text",
                onClick: () =>
                  handleUnsyncSkills(
                    managedSelected.map((s) => s.center_skill_id),
                    [agent.key]
                  ),
              },
              t(`移出受管技能 (${managedSelected.length})`, `Unsync Managed (${managedSelected.length})`)
            ),
          unmanagedSelected.length > 0 &&
            h(
              "button",
              {
                type: "button",
                className: "text primary",
                onClick: async () => {
                  let count = 0;
                  for (const sk of unmanagedSelected) {
                    try {
                      await apiJSON(`/api/skills/agents/${encodeURIComponent(agent.key)}/import`, {
                        method: "POST",
                        body: { rel_path: sk.relative_path },
                      });
                      count++;
                    } catch (err) {
                      notify(err.message, "err");
                    }
                  }
                  if (count) notify(t(`已导入 ${count} 个本地技能`, `Imported ${count} skill(s)`), "ok");
                  await loadAll(false);
                },
              },
              t(`批量导入中央 (${unmanagedSelected.length})`, `Import Selected (${unmanagedSelected.length})`)
            ),
          unmanagedSelected.length > 0 &&
            h(
              "button",
              {
                type: "button",
                className: "text sw-btn-danger",
                onClick: () =>
                  handleDeleteLocalSkills(
                    agent.key,
                    unmanagedSelected.map((s) => s.relative_path)
                  ),
              },
              t(`批量删除本地 (${unmanagedSelected.length})`, `Delete Unmanaged (${unmanagedSelected.length})`)
            ),
          h(
            "button",
            {
              type: "button",
              className: "text",
              onClick: () => {
                state.selectedSkillRelPaths.clear();
                render();
              },
            },
            t("清空选择", "Clear")
          )
        )
    );

    const skillCards = visibleSkills.map((sk) => {
      const checked = state.selectedSkillRelPaths.has(sk.relative_path);
      const sm = statusMeta(sk.sync_status);
      const cb = h("input", {
        type: "checkbox",
        checked,
        "aria-label": sk.name,
        onChange: () => {
          if (cb.checked) state.selectedSkillRelPaths.add(sk.relative_path);
          else state.selectedSkillRelPaths.delete(sk.relative_path);
          render();
        },
      });

      const actionButtons = [
        h(
          "button",
          {
            type: "button",
            className: "text",
            onClick: () => openSkillDetailDialog({ agentKey: agent.key, skill: sk }),
          },
          t("详情 / 文档", "Inspect")
        ),
      ];
      if (sk.sync_status === "project_only") {
        actionButtons.push(
          h(
            "button",
            {
              type: "button",
              className: "text primary",
              onClick: () => handleImportLocalSkill(agent.key, sk.relative_path),
            },
            t("导入纳管", "Import")
          )
        );
        actionButtons.push(
          h(
            "button",
            {
              type: "button",
              className: "text sw-btn-danger",
              onClick: () => handleDeleteLocalSkills(agent.key, [sk.relative_path], sk.name),
            },
            t("删除本地", "Delete")
          )
        );
      } else {
        if (sk.sync_status === "project_newer" || sk.sync_status === "diverged") {
          actionButtons.push(
            h(
              "button",
              {
                type: "button",
                className: "text primary",
                onClick: () => handleImportLocalSkill(agent.key, sk.relative_path),
              },
              t("推送到中央", "Push")
            )
          );
        }
        if (sk.sync_status === "center_newer" || sk.sync_status === "diverged") {
          actionButtons.push(
            h(
              "button",
              {
                type: "button",
                className: "text",
                onClick: () => handlePullLocalSkill(agent.key, sk),
              },
              t("从中央更新", "Pull")
            )
          );
        }
        if (sk.managed && sk.center_skill_id) {
          actionButtons.push(
            h(
              "button",
              {
                type: "button",
                className: "text",
                onClick: () => handleUnsyncSkills([sk.center_skill_id], [agent.key]),
              },
              t("移出", "Unsync")
            )
          );
        }
      }

      return h(
        "div",
        { className: `sw-skill-card${checked ? " is-selected" : ""}` },
        h(
          "div",
          { className: "sw-skill-card-body" },
          h(
            "div",
            { className: "sw-skill-card-head" },
            h(
              "div",
              { className: "sw-skill-title-row" },
              cb,
              h(
                "button",
                {
                  type: "button",
                  className: "sw-skill-name",
                  onClick: () => openSkillDetailDialog({ agentKey: agent.key, skill: sk }),
                },
                sk.name
              )
            ),
            h("span", { className: `sw-badge ${sm.cls}` }, sm.label)
          ),
          sk.description && h("p", { className: "sw-skill-desc", style: { marginTop: "6px" } }, sk.description),
          h(
            "div",
            { className: "sw-skill-meta", style: { marginTop: "8px" } },
            sk.relative_path !== sk.name && h("span", { className: "sw-path-chip" }, sk.relative_path),
            sk.is_symlink
              ? h("span", { className: "sw-badge no-dot accent", title: sk.link_target || "" }, t("Symlink 软链", "Symlink"))
              : h("span", { className: "sw-badge no-dot muted" }, t("本地目录", "Directory")),
            sk.files?.length ? h("span", null, `${sk.files.length} ${t("文件", "files")}`) : null,
            sk.last_modified_ms ? h("span", null, formatRelTime(sk.last_modified_ms)) : null,
            (sk.tags || []).map((tg) => h("span", { className: "sw-count-pill" }, tg))
          )
        ),
        h("div", { className: "sw-skill-actions" }, actionButtons)
      );
    });

    const skillsEl = state.agentLoading
      ? h("div", { className: "sw-empty" }, t("正在扫描 Agent 技能目录…", "Scanning agent skills directory..."))
      : visibleSkills.length
      ? h("div", { className: state.viewMode === "list" ? "sw-list" : "sw-grid" }, skillCards)
      : h(
          "div",
          { className: "sw-empty" },
          skills.length
            ? t("当前筛选条件下没有技能", "No skills match current filters")
            : t(`目录 ${agent.display_skills_dir} 下暂无技能，可点击上方 “+ 添加中央 Skills” 同步部署`, "No skills in this directory yet. Click '+ Add Skills' above.")
        );

    return h(
      "section",
      { className: "sw-main" },
      headerCard,
      renderPresetBar(agent.key),
      renderConflictNotice(),
      filterToolbar,
      tagsRow,
      batchBar,
      skillsEl
    );
  }

  function renderLibraryMain() {
    let importInputVal = "";
    const skills = state.centerSkills || [];
    const allTags = Array.from(new Set(skills.flatMap((s) => s.tags || []))).sort();
    const q = state.librarySearch.trim().toLowerCase();
    const visible = skills.filter((s) => {
      if (state.libraryTag !== "all" && !(s.tags || []).includes(state.libraryTag)) return false;
      if (!q) return true;
      return (
        s.name.toLowerCase().includes(q) ||
        (s.description || "").toLowerCase().includes(q) ||
        (s.tags || []).some((tg) => tg.toLowerCase().includes(q))
      );
    });

    const headerCard = h(
      "div",
      { className: "sw-card" },
      h(
        "div",
        { className: "sw-header" },
        h(
          "div",
          null,
          h("h2", { className: "sw-header-title" }, t("中央 Skills 仓库", "Central Skills Library")),
          h(
            "div",
            { className: "sw-header-meta" },
            h("span", { className: "sw-path-chip", style: { cursor: "pointer" }, onClick: () => copyText(state.overview.central_dir || "") }, state.overview.central_dir || ""),
            h("span", null, t(`共 ${skills.length} 个受管技能`, `${skills.length} central skill(s)`))
          )
        ),
        h(
          "div",
          { className: "sw-actions" },
          h(
            "button",
            {
              type: "button",
              className: "text primary",
              onClick: () => openAddSkillsDialog(null),
            },
            t("批量同步到 Agents", "Batch Deploy")
          ),
          h(
            "button",
            {
              type: "button",
              className: "text",
              onClick: () => loadAll(true),
            },
            t("刷新", "Refresh")
          )
        )
      )
    );

    const importInput = h("input", {
      type: "text",
      className: "sw-input",
      style: { flex: 1, minWidth: "220px" },
      placeholder: t("导入本地 Skill 目录路径（须含 SKILL.md），如 ~/skills/my-skill", "Import local skill directory containing SKILL.md"),
      onInput: (e) => {
        importInputVal = e.target.value;
      },
    });

    const importRow = h(
      "div",
      { className: "sw-card", style: { padding: "12px" } },
      h(
        "div",
        { className: "sw-row" },
        importInput,
        h(
          "button",
          {
            type: "button",
            className: "text primary",
            onClick: async () => {
              if (!importInputVal.trim()) return;
              try {
                const sk = await apiJSON("/api/skills/center/import-path", {
                  method: "POST",
                  body: { path: importInputVal.trim() },
                });
                notify(t(`已导入中央仓库: ${sk.name}`, `Imported: ${sk.name}`), "ok");
                await loadAll(true);
              } catch (err) {
                notify(err.message, "err");
              }
            },
          },
          t("+ 导入目录", "+ Import Directory")
        )
      )
    );

    const filterRow = h(
      "div",
      { className: "sw-toolbar" },
      makeSearchInput({
        focusKey: "library-skill-search",
        value: state.librarySearch,
        placeholder: t("搜索中央仓库 Skills…", "Search library..."),
        onUpdate: (val, shouldRender) => {
          state.librarySearch = val;
          if (shouldRender) render();
        },
      }),
      allTags.length
        ? h(
            "div",
            { className: "sw-chips" },
            h(
              "button",
              {
                type: "button",
                className: "sw-chip",
                "aria-pressed": state.libraryTag === "all" ? "true" : "false",
                onClick: () => {
                  state.libraryTag = "all";
                  render();
                },
              },
              t("全部标签", "All Tags")
            ),
            allTags.map((tg) =>
              h(
                "button",
                {
                  type: "button",
                  className: "sw-chip",
                  "aria-pressed": state.libraryTag === tg ? "true" : "false",
                  onClick: () => {
                    state.libraryTag = tg;
                    render();
                  },
                },
                tg
              )
            )
          )
        : null
    );

    const cards = visible.map((sk) => {
      const agByKey = agentMap();
      const targetPills = (sk.targets || []).map((tg) => {
        const ag = agByKey.get(tg.tool);
        return h(
          "span",
          { className: "sw-badge no-dot in-sync", title: `${tg.target_path} (${tg.mode})` },
          ag?.display_name || tg.tool,
          h("span", { style: { opacity: 0.7 } }, tg.mode === "symlink" ? "⇄" : "❏"),
          h(
            "button",
            {
              type: "button",
              className: "sw-preset-icon-btn",
              "aria-label": t(`取消同步 ${ag?.display_name || tg.tool}`, `Unsync ${tg.tool}`),
              onClick: () => handleUnsyncSkills([sk.id], [tg.tool]),
            },
            "✕"
          )
        );
      });

      return h(
        "div",
        { className: "sw-skill-card" },
        h(
          "div",
          { className: "sw-skill-card-head" },
          h(
            "button",
            {
              type: "button",
              className: "sw-skill-name",
              onClick: () => openSkillDetailDialog({ centerSkill: sk }),
            },
            sk.name
          ),
          h("span", { className: "sw-count-pill" }, sk.content_hash ? sk.content_hash.slice(0, 8) : "center")
        ),
        sk.description && h("p", { className: "sw-skill-desc" }, sk.description),
        sk.source_ref && h("div", { className: "sw-path-chip", title: sk.source_ref }, `${t("来源", "Src")}: ${sk.source_ref}`),
        h(
          "div",
          { className: "sw-chips" },
          (sk.tags || []).map((tg) => h("span", { className: "sw-count-pill" }, tg)),
          targetPills
        ),
        h(
          "div",
          { className: "sw-skill-actions" },
          h(
            "button",
            {
              type: "button",
              className: "text",
              onClick: () => openSkillDetailDialog({ centerSkill: sk }),
            },
            t("查看文档", "Inspect")
          ),
          h(
            "button",
            {
              type: "button",
              className: "text",
              onClick: () => openTagsDialog(sk),
            },
            t("标签", "Tags")
          ),
          h(
            "button",
            {
              type: "button",
              className: "text sw-btn-danger",
              onClick: async () => {
                const ok = await showConfirm({
                  title: t(`从中央仓库删除 "${sk.name}"？`, `Delete "${sk.name}" from Central Library?`),
                  message: t(
                    `将同时移除 ${(sk.targets || []).length} 个 Agent 上受本技能管理的同步部署并删除中央副本，不可恢复。`,
                    `This removes ${(sk.targets || []).length} managed target(s) and deletes the central copy.`
                  ),
                  confirmLabel: t("确认删除", "Delete"),
                  danger: true,
                });
                if (!ok) return;
                try {
                  await apiJSON(`/api/skills/center/${encodeURIComponent(sk.id)}`, { method: "DELETE" });
                  notify(t(`已从中央仓库删除 ${sk.name}`, `Deleted ${sk.name}`), "ok");
                  await loadAll(true);
                } catch (err) {
                  notify(err.message, "err");
                }
              },
            },
            t("删除", "Delete")
          )
        )
      );
    });

    const listEl = visible.length
      ? h("div", { className: "sw-grid" }, cards)
      : h("div", { className: "sw-empty" }, t("暂无中央仓库技能，可通过上方路径导入或在任意 Agent 中点击「导入纳管」。", "No central skills found."));

    return h("section", { className: "sw-main" }, headerCard, importRow, renderPresetBar(null), filterRow, listEl);
  }

  function renderProjectsOverviewMain() {
    const projects = state.projects || [];
    const headerCard = h(
      "div",
      { className: "sw-card" },
      h(
        "div",
        { className: "sw-header" },
        h(
          "div",
          null,
          h("h2", { className: "sw-header-title" }, t("项目与关联工作区总览", "Projects & Linked Workspaces")),
          h(
            "div",
            { className: "sw-header-meta" },
            h("span", null, t(`已注册工作区: ${projects.length}`, `Workspaces: ${projects.length}`)),
            h("span", null, "·"),
            h("span", null, t("支持多 Agent 目录聚合、启用/停用切换、中央同步与 Preset 编排", "Per-project agent grouping, enable/disable toggle, central sync & presets"))
          )
        ),
        h(
          "div",
          { className: "sw-actions" },
          h(
            "button",
            { type: "button", className: "text primary", onClick: () => openAddProjectDialog() },
            t("+ 添加项目 / 关联目录", "+ Add Project / Linked")
          ),
          h("button", { type: "button", className: "text", onClick: () => loadAll(true) }, t("刷新", "Refresh"))
        )
      )
    );

    const cards = projects.map((p) => {
      const sh = p.sync_health || {};
      const isLinked = p.workspace_type === "linked";
      return h(
        "div",
        { className: "sw-agent-card" },
        h(
          "div",
          { className: "sw-agent-card-top" },
          h(
            "div",
            { className: "sw-row", style: { minWidth: 0 } },
            renderIcon("", isLinked ? "L" : "P", true),
            h(
              "div",
              { style: { minWidth: 0 } },
              h("div", { style: { fontWeight: 700, fontSize: "14.5px" } }, p.name),
              h("div", { style: { fontSize: "11.5px", color: "var(--muted)" } }, isLinked ? t("关联目录 (Linked)", "Linked Directory") : t("项目工作区", "Project Workspace"))
            )
          ),
          h("span", { className: `sw-badge ${projectHealthDotClass(p)}` }, `${p.skill_count} ${t("技能", "skills")}`)
        ),
        h("div", { className: "sw-path-chip", style: { cursor: "pointer" }, title: p.display_path, onClick: () => copyText(p.display_path) }, p.display_path),
        h(
          "div",
          { className: "sw-chips" },
          sh.in_sync > 0 && h("span", { className: "sw-badge in-sync" }, `${t("已同步", "In Sync")}: ${sh.in_sync}`),
          sh.center_newer > 0 && h("span", { className: "sw-badge center-newer" }, `${t("中心更新", "Center")}: ${sh.center_newer}`),
          sh.project_newer > 0 && h("span", { className: "sw-badge local-newer" }, `${t("项目更新", "Local")}: ${sh.project_newer}`),
          sh.diverged > 0 && h("span", { className: "sw-badge diverged" }, `${t("分歧", "Diverged")}: ${sh.diverged}`),
          sh.project_only > 0 && h("span", { className: "sw-badge project-only" }, `${t("仅项目", "Only")}: ${sh.project_only}`)
        ),
        h(
          "div",
          { className: "sw-skill-actions" },
          h(
            "button",
            { type: "button", className: "text primary", onClick: () => navigateTo("#/project/" + encodeURIComponent(p.id)) },
            t("进入工作区", "Open Workspace")
          ),
          h(
            "button",
            { type: "button", className: "text", onClick: () => handleRemoveProject(p) },
            t("移除", "Remove")
          )
        )
      );
    });

    const body = projects.length
      ? h("div", { className: "sw-grid" }, cards)
      : h("div", { className: "sw-empty" }, t("尚未添加项目或关联目录，点击右上角 “+ 添加项目 / 关联目录” 开始。", "No projects registered yet. Click '+ Add Project / Linked' above."));

    return h("section", { className: "sw-main" }, headerCard, body);
  }

  function renderProjectPresetBar(project) {
    const targetAgents = defaultProjectTargetAgents(project, state.projectTargets || []);
    const bySkillAgent = new Map();
    for (const sk of state.projectSkills || []) {
      if (sk.center_skill_id) bySkillAgent.set(`${sk.center_skill_id}::${sk.agent}`, sk);
    }

    const pills = (state.presets || []).map((preset) => {
      const sids = preset.skill_ids || [];
      let applied = 0;
      for (const sid of sids) {
        if (targetAgents.length && targetAgents.every((ak) => bySkillAgent.has(`${sid}::${ak}`))) applied++;
      }
      let statusCls = "";
      if (sids.length > 0 && applied === sids.length) statusCls = " is-full";
      else if (applied > 0) statusCls = " is-partial";

      return h(
        "div",
        { className: `sw-preset-pill${statusCls}` },
        h(
          "button",
          {
            type: "button",
            className: "sw-preset-apply",
            onClick: async () => {
              if (!sids.length) {
                openPresetDialog(preset);
                return;
              }
              if (!targetAgents.length) {
                notify(t("没有可用的项目 Agent 目录", "No active project agent targets"), "warn");
                return;
              }
              if (applied === sids.length) {
                const variantsToRemove = [];
                for (const sid of sids) {
                  for (const ak of targetAgents) {
                    const v = bySkillAgent.get(`${sid}::${ak}`);
                    if (v) variantsToRemove.push(v);
                  }
                }
                if (!(await confirmUnsyncedRemoval(project.id, variantsToRemove))) return;
                for (const v of variantsToRemove) {
                  try {
                    await apiJSON(`/api/skills/projects/${encodeURIComponent(project.id)}/delete-skill`, {
                      method: "POST",
                      body: { rel_path: v.relative_path, agent: v.agent },
                    });
                  } catch {}
                }
                notify(t(`已从项目移出 Preset "${preset.name}"`, `Removed preset "${preset.name}" from project`), "ok");
                await loadAll(false);
              } else {
                try {
                  const res = await apiJSON(`/api/skills/projects/${encodeURIComponent(project.id)}/export`, {
                    method: "POST",
                    body: { skill_ids: sids, agents: targetAgents },
                  });
                  if (!res.ok && res.conflicts?.length) {
                    state.lastConflict = res;
                    notify(t(`应用 Preset 完成 ${res.applied} 项，存在 ${res.conflicts.length} 项冲突`, `Applied ${res.applied}, conflicts: ${res.conflicts.length}`), "warn");
                  } else {
                    notify(t(`已部署 Preset "${preset.name}"（${res.applied} 新增 / ${res.skipped} 已有）`, `Applied preset "${preset.name}"`), "ok");
                  }
                  await loadAll(false);
                } catch (err) {
                  if (err.conflicts?.length) {
                    state.lastConflict = { ok: false, conflicts: err.conflicts };
                    render();
                  }
                  notify(err.message, "err");
                }
              }
            },
          },
          h("span", null, preset.name),
          h("span", { className: "sw-count-pill" }, `${applied}/${sids.length}`)
        ),
        h(
          "button",
          {
            type: "button",
            className: "sw-preset-icon-btn",
            title: t("编辑 Preset", "Edit Preset"),
            onClick: () => openPresetDialog(preset),
          },
          "✎"
        )
      );
    });

    const seedIDs = Array.from(new Set((state.projectSkills || []).map((s) => s.center_skill_id).filter(Boolean)));
    return h(
      "div",
      { className: "sw-preset-bar" },
      h("span", { className: "sw-preset-label" }, t("项目 Presets:", "Project Presets:")),
      ...pills,
      h("button", { type: "button", className: "text", onClick: () => openPresetDialog(null, seedIDs) }, t("+ 新建 Preset", "+ New Preset"))
    );
  }

  function renderProjectDetailMain(project) {
    const groups = buildProjectSkillGroups(state.projectSkills);
    const enabledGroupsCount = groups.filter((g) => g.enabledCount > 0).length;
    const isLinked = project.workspace_type === "linked";

    const counts = { all: groups.length, in_sync: 0, center_newer: 0, project_newer: 0, diverged: 0, project_only: 0 };
    const tagSet = new Set();
    let hasUntagged = false;
    for (const g of groups) {
      if (counts[g.status] !== undefined) counts[g.status]++;
      else counts.project_only++;
      if (!g.tags.length) hasUntagged = true;
      g.tags.forEach((tg) => tagSet.add(tg));
    }

    const q = state.projectSearch.trim().toLowerCase();
    const visible = groups.filter((g) => {
      if (state.projectFilterMode === "enabled" && g.enabledCount === 0) return false;
      if (state.projectFilterMode === "disabled" && g.enabledCount > 0) return false;
      if (state.projectStatusFilter !== "all" && g.status !== state.projectStatusFilter) return false;
      if (state.projectTagFilter === "__untagged__" && g.tags.length > 0) return false;
      else if (state.projectTagFilter !== "all" && state.projectTagFilter !== "__untagged__" && !g.tags.includes(state.projectTagFilter)) return false;
      if (!q) return true;
      return (
        g.name.toLowerCase().includes(q) ||
        (g.description || "").toLowerCase().includes(q) ||
        g.relative_path.toLowerCase().includes(q) ||
        g.tags.some((tg) => tg.toLowerCase().includes(q))
      );
    });

    const headerCard = h(
      "div",
      { className: "sw-card" },
      h(
        "div",
        { className: "sw-header" },
        h(
          "div",
          { className: "sw-header-title-wrap" },
          renderIcon("", isLinked ? "L" : "P", true),
          h(
            "div",
            { style: { minWidth: 0 } },
            h(
              "div",
              { className: "sw-row" },
              h("button", { type: "button", className: "text", onClick: () => navigateTo("#/projects") }, t("← 所有项目", "← Projects")),
              h("h2", { className: "sw-header-title" }, project.name),
              h("span", { className: `sw-badge no-dot ${isLinked ? "accent" : "ok"}` }, isLinked ? t("关联目录", "Linked") : t("项目工作区", "Project")),
              h("span", { className: "sw-count-pill" }, String(groups.length))
            ),
            h(
              "div",
              { className: "sw-header-meta" },
              h("span", { className: "sw-path-chip", style: { cursor: "pointer" }, onClick: () => copyText(project.display_path) }, project.display_path),
              project.supports_skill_toggle && h("span", null, t(`已启用 ${enabledGroupsCount} / ${groups.length}`, `${enabledGroupsCount}/${groups.length} enabled`))
            )
          )
        ),
        h(
          "div",
          { className: "sw-actions" },
          h("button", { type: "button", className: "text primary", onClick: () => openAddSkillsToProjectDialog(project) }, t("+ 添加 Skills", "+ Add Skills")),
          h("button", { type: "button", className: "text", onClick: () => loadAll(true) }, t("刷新", "Refresh"))
        )
      )
    );

    const enabledChips = [
      { key: "all", label: t("全部", "All") },
      { key: "enabled", label: t("已启用", "Enabled") },
      { key: "disabled", label: t("已停用", "Disabled") },
    ];
    const statusChipsData = [
      { key: "all", label: t("全部状态", "All Status"), n: counts.all },
      { key: "in_sync", label: t("已同步", "In Sync"), n: counts.in_sync },
      { key: "project_newer", label: t("项目更新", "Project Newer"), n: counts.project_newer },
      { key: "center_newer", label: t("中心更新", "Center Newer"), n: counts.center_newer },
      { key: "diverged", label: t("已分歧", "Diverged"), n: counts.diverged },
      { key: "project_only", label: t("仅项目", "Project Only"), n: counts.project_only },
    ];

    const filterToolbar = h(
      "div",
      { className: "sw-toolbar" },
      h(
        "div",
        { className: "sw-chips" },
        project.supports_skill_toggle &&
          enabledChips.map((em) =>
            h(
              "button",
              {
                type: "button",
                className: "sw-chip",
                "aria-pressed": state.projectFilterMode === em.key ? "true" : "false",
                onClick: () => {
                  state.projectFilterMode = em.key;
                  render();
                },
              },
              em.label
            )
          ),
        statusChipsData.map((st) =>
          h(
            "button",
            {
              type: "button",
              className: "sw-chip",
              "aria-pressed": state.projectStatusFilter === st.key ? "true" : "false",
              onClick: () => {
                state.projectStatusFilter = st.key;
                render();
              },
            },
            `${st.label} (${st.n})`
          )
        )
      ),
      h(
        "div",
        { className: "sw-actions" },
        makeSearchInput({
          focusKey: "project-skill-search",
          value: state.projectSearch,
          placeholder: t("按名称 / 路径 / 标签筛选…", "Filter project skills..."),
          onUpdate: (val, shouldRender) => {
            state.projectSearch = val;
            if (shouldRender) render();
          },
        }),
        h(
          "button",
          {
            type: "button",
            className: "text",
            onClick: () => {
              state.viewMode = state.viewMode === "grid" ? "list" : "grid";
              localStorage.setItem("ctxview.skills.viewMode", state.viewMode);
              render();
            },
          },
          state.viewMode === "grid" ? t("列表视图", "List View") : t("网格视图", "Grid View")
        )
      )
    );

    const tagsRow =
      tagSet.size || hasUntagged
        ? h(
            "div",
            { className: "sw-chips" },
            h(
              "button",
              {
                type: "button",
                className: "sw-chip",
                "aria-pressed": state.projectTagFilter === "all" ? "true" : "false",
                onClick: () => {
                  state.projectTagFilter = "all";
                  render();
                },
              },
              t("全部标签", "All Tags")
            ),
            hasUntagged &&
              h(
                "button",
                {
                  type: "button",
                  className: "sw-chip",
                  "aria-pressed": state.projectTagFilter === "__untagged__" ? "true" : "false",
                  onClick: () => {
                    state.projectTagFilter = "__untagged__";
                    render();
                  },
                },
                t("无标签", "Untagged")
              ),
            Array.from(tagSet)
              .sort()
              .map((tg) =>
                h(
                  "button",
                  {
                    type: "button",
                    className: "sw-chip",
                    "aria-pressed": state.projectTagFilter === tg ? "true" : "false",
                    onClick: () => {
                      state.projectTagFilter = tg;
                      render();
                    },
                  },
                  tg
                )
              )
          )
        : null;

    const selectedGroups = groups.filter((g) => state.projectSelectedGroupIds.has(g.id));
    const anyDisabledInSel = selectedGroups.some((g) => g.enabledCount !== g.totalCount);
    const togglableSel = selectedGroups.filter((g) => (anyDisabledInSel ? g.enabledCount !== g.totalCount : g.enabledCount > 0));
    const canPushCenter = selectedGroups.filter((g) => ["project_only", "project_newer", "diverged"].includes(g.status));
    const canPullProject = selectedGroups.filter((g) => ["project_newer", "center_newer", "diverged"].includes(g.status));
    const selectedCenterSkills = Array.from(new Set(selectedGroups.flatMap((g) => g.centerSkillIds)))
      .map((cid) => centerMap().get(cid))
      .filter(Boolean);

    const batchBar = h(
      "div",
      { className: selectedGroups.length ? "sw-batch-bar" : "sw-toolbar" },
      h(
        "div",
        { className: "sw-row" },
        h(
          "button",
          {
            type: "button",
            className: "text",
            onClick: () => {
              const allVis = visible.length > 0 && visible.every((g) => state.projectSelectedGroupIds.has(g.id));
              if (allVis) visible.forEach((g) => state.projectSelectedGroupIds.delete(g.id));
              else visible.forEach((g) => state.projectSelectedGroupIds.add(g.id));
              render();
            },
          },
          visible.length > 0 && visible.every((g) => state.projectSelectedGroupIds.has(g.id))
            ? t("取消全选当前列表", "Deselect Visible")
            : t(`全选当前列表 (${visible.length})`, `Select Visible (${visible.length})`)
        ),
        selectedGroups.length > 0 && h("span", { style: { fontWeight: 600, fontSize: "12.5px" } }, t(`已选 ${selectedGroups.length} 项`, `${selectedGroups.length} selected`))
      ),
      selectedGroups.length > 0 &&
        h(
          "div",
          { className: "sw-actions" },
          project.supports_skill_toggle &&
            togglableSel.length > 0 &&
            h(
              "button",
              {
                type: "button",
                className: "text primary",
                onClick: async () => {
                  let count = 0;
                  for (const g of togglableSel) {
                    for (const v of g.variants) {
                      if (v.enabled === anyDisabledInSel) continue;
                      try {
                        await apiJSON(`/api/skills/projects/${encodeURIComponent(project.id)}/toggle`, {
                          method: "POST",
                          body: { rel_path: v.relative_path, agent: v.agent, enabled: anyDisabledInSel },
                        });
                      } catch {}
                    }
                    count++;
                  }
                  notify(anyDisabledInSel ? t(`已启用 ${count} 项技能`, `Enabled ${count}`) : t(`已停用 ${count} 项技能`, `Disabled ${count}`), "ok");
                  await loadAll(false);
                },
              },
              anyDisabledInSel ? t(`批量启用 (${togglableSel.length})`, `Enable (${togglableSel.length})`) : t(`批量停用 (${togglableSel.length})`, `Disable (${togglableSel.length})`)
            ),
          canPullProject.length > 0 &&
            h(
              "button",
              {
                type: "button",
                className: "text",
                onClick: async () => {
                  const hasNewer = canPullProject.some((g) => g.status === "project_newer" || g.status === "diverged");
                  if (hasNewer) {
                    const ok = await showConfirm({
                      title: t("用中央仓库覆盖项目本地改动？", "Overwrite project edits from Center?"),
                      message: t("选中项中包含较新或分歧的项目副本，操作将用中央版本覆盖本地修改。", "Some selected skills contain local edits that will be overwritten."),
                      confirmLabel: t("覆盖更新", "Overwrite"),
                      danger: true,
                    });
                    if (!ok) return;
                  }
                  let updated = 0;
                  for (const g of canPullProject) {
                    for (const v of g.variants) {
                      try {
                        await apiJSON(`/api/skills/projects/${encodeURIComponent(project.id)}/pull`, {
                          method: "POST",
                          body: { rel_path: v.relative_path, agent: v.agent, force: true },
                        });
                      } catch {}
                    }
                    updated++;
                  }
                  notify(t(`已从中央更新 ${updated} 项技能`, `Pulled ${updated} skill(s)`), "ok");
                  await loadAll(false);
                },
              },
              t(`批量从中央更新 (${canPullProject.length})`, `Pull (${canPullProject.length})`)
            ),
          canPushCenter.length > 0 &&
            h(
              "button",
              {
                type: "button",
                className: "text",
                onClick: async () => {
                  let updated = 0;
                  let conflicts = 0;
                  for (const g of canPushCenter) {
                    try {
                      const res = await pushProjectSkillToCenterAndAlign(project.id, g);
                      if (res.conflicting > 0) conflicts++;
                      else updated++;
                    } catch {}
                  }
                  if (conflicts > 0) notify(t(`已推送 ${updated} 项；${conflicts} 项因多 Agent 同时存在未同步修改而跳过`, `Pushed ${updated}; ${conflicts} skipped (multi-agent conflict)`), "warn");
                  else notify(t(`已推送并对齐 ${updated} 项技能`, `Pushed ${updated} skill(s)`), "ok");
                  await loadAll(false);
                },
              },
              t(`批量推送中央 (${canPushCenter.length})`, `Push (${canPushCenter.length})`)
            ),
          selectedCenterSkills.length > 0 &&
            h(
              "button",
              { type: "button", className: "text", onClick: () => openBatchProjectTagsDialog(selectedCenterSkills) },
              t(`批量标签 (${selectedCenterSkills.length})`, `Tags (${selectedCenterSkills.length})`)
            ),
          h(
            "button",
            {
              type: "button",
              className: "text sw-btn-danger",
              onClick: async () => {
                const ok = await showConfirm({
                  title: t(`从项目中删除选中的 ${selectedGroups.length} 个技能？`, `Delete ${selectedGroups.length} selected skill(s) from project?`),
                  message: t("将移除选中技能在当前项目内所有 Agent 目录的副本（中央仓库副本会保留）。", "Removes project copies across all agents in this project (Central Library keeps its copies)."),
                  confirmLabel: t("确认删除", "Delete"),
                  danger: true,
                });
                if (!ok) return;
                let deleted = 0;
                for (const g of selectedGroups) {
                  for (const v of g.variants) {
                    try {
                      await apiJSON(`/api/skills/projects/${encodeURIComponent(project.id)}/delete-skill`, {
                        method: "POST",
                        body: { rel_path: v.relative_path, agent: v.agent },
                      });
                    } catch {}
                  }
                  deleted++;
                }
                notify(t(`已从项目移除 ${deleted} 项技能`, `Deleted ${deleted} skill(s)`), "ok");
                await loadAll(false);
              },
            },
            t(`批量删除 (${selectedGroups.length})`, `Delete (${selectedGroups.length})`)
          ),
          h("button", { type: "button", className: "text", onClick: () => { state.projectSelectedGroupIds.clear(); render(); } }, t("清空选择", "Clear"))
        )
    );

    const cards = visible.map((g) => {
      const checked = state.projectSelectedGroupIds.has(g.id);
      const sm = statusMeta(g.status);
      const cb = h("input", {
        type: "checkbox",
        checked,
        "aria-label": g.name,
        onChange: () => {
          if (cb.checked) state.projectSelectedGroupIds.add(g.id);
          else state.projectSelectedGroupIds.delete(g.id);
          render();
        },
      });
      const canPush = ["project_only", "project_newer", "diverged"].includes(g.status);
      const canPull = ["project_newer", "center_newer", "diverged"].includes(g.status);

      const actionButtons = [
        h("button", { type: "button", className: "text", onClick: () => openProjectSkillDetailDialog(project, g) }, t("详情 / 开关", "Inspect")),
      ];
      if (canPush) {
        actionButtons.push(
          h(
            "button",
            {
              type: "button",
              className: "text primary",
              onClick: async () => {
                try {
                  const res = await pushProjectSkillToCenterAndAlign(project.id, g);
                  if (res.conflicting > 0) {
                    notify(t(`多个 Agent 副本均存在未同步修改（${res.conflicting} 份），请逐个核对后再推送`, `Conflict: ${res.conflicting} modified variants`), "warn");
                  } else {
                    notify(t(`已推送到中央并同步各副本: ${g.name}`, `Pushed & aligned: ${g.name}`), "ok");
                  }
                  await loadAll(false);
                } catch (err) {
                  notify(err.message, "err");
                }
              },
            },
            g.status === "project_only" ? t("导入中央", "Import") : t("推送中央", "Push")
          )
        );
      }
      if (canPull) {
        actionButtons.push(
          h(
            "button",
            {
              type: "button",
              className: "text",
              onClick: async () => {
                if (g.status === "project_newer" || g.status === "diverged") {
                  const ok = await showConfirm({
                    title: t(`用中央版本重置 "${g.name}"？`, `Reset "${g.name}" from Center?`),
                    message: t("将覆盖本项目内的本地改动。", "This overwrites local project changes with the Central Library copy."),
                    confirmLabel: t("确认重置", "Reset"),
                    danger: true,
                  });
                  if (!ok) return;
                }
                try {
                  for (const v of g.variants) {
                    await apiJSON(`/api/skills/projects/${encodeURIComponent(project.id)}/pull`, {
                      method: "POST",
                      body: { rel_path: v.relative_path, agent: v.agent, force: true },
                    });
                  }
                  notify(t(`已更新项目副本: ${g.name}`, `Updated from center: ${g.name}`), "ok");
                  await loadAll(false);
                } catch (err) {
                  notify(err.message, "err");
                }
              },
            },
            g.status === "project_newer" ? t("重置为中央版", "Reset") : t("从中央更新", "Pull")
          )
        );
      }
      if (project.supports_skill_toggle) {
        const allEnabled = g.enabledCount === g.totalCount;
        actionButtons.push(
          h("button", {
            type: "button",
            role: "switch",
            className: "sw-toggle",
            "aria-checked": allEnabled ? "true" : "false",
            title: allEnabled ? t("点击停用（移至 skills-disabled）", "Disable skill") : t("点击启用", "Enable skill"),
            onClick: async () => {
              const nextEnabled = !allEnabled;
              for (const v of g.variants) {
                try {
                  await apiJSON(`/api/skills/projects/${encodeURIComponent(project.id)}/toggle`, {
                    method: "POST",
                    body: { rel_path: v.relative_path, agent: v.agent, enabled: nextEnabled },
                  });
                } catch (err) {
                  notify(err.message, "err");
                }
              }
              notify(nextEnabled ? t(`已启用 ${g.name}`, `Enabled ${g.name}`) : t(`已停用 ${g.name}`, `Disabled ${g.name}`), "ok");
              await loadAll(false);
            },
          })
        );
      }
      actionButtons.push(
        h(
          "button",
          {
            type: "button",
            className: "text sw-btn-danger",
            onClick: async () => {
              const ok = await showConfirm({
                title: t(`删除项目内技能 "${g.name}"？`, `Delete "${g.name}" from project?`),
                message: t(
                  `将删除本项目内 ${g.variants.length} 个 Agent 目录下的副本（中央仓库不受影响）。`,
                  `Deletes ${g.variants.length} workspace copy/copies; Central Library stays untouched.`
                ),
                confirmLabel: t("删除", "Delete"),
                danger: true,
              });
              if (!ok) return;
              for (const v of g.variants) {
                try {
                  await apiJSON(`/api/skills/projects/${encodeURIComponent(project.id)}/delete-skill`, {
                    method: "POST",
                    body: { rel_path: v.relative_path, agent: v.agent },
                  });
                } catch {}
              }
              notify(t(`已移除 ${g.name}`, `Deleted ${g.name}`), "ok");
              await loadAll(false);
            },
          },
          t("删除", "Delete")
        )
      );

      return h(
        "div",
        { className: `sw-skill-card${checked ? " is-selected" : ""}${g.enabledCount === 0 ? " is-disabled-skill" : ""}` },
        h(
          "div",
          { className: "sw-skill-card-body" },
          h(
            "div",
            { className: "sw-skill-card-head" },
            h(
              "div",
              { className: "sw-skill-title-row" },
              cb,
              h("button", { type: "button", className: "sw-skill-name", onClick: () => openProjectSkillDetailDialog(project, g) }, g.name)
            ),
            h("span", { className: `sw-badge ${sm.cls}` }, sm.label)
          ),
          g.description && h("p", { className: "sw-skill-desc", style: { marginTop: "6px" } }, g.description),
          h(
            "div",
            { className: "sw-skill-meta", style: { marginTop: "8px" } },
            g.variants.map((v) =>
              h("span", { className: `sw-badge no-dot ${v.enabled ? "ok" : "warn"}` }, `${v.agent_display_name || v.agent}${v.enabled ? "" : ` (${t("停用", "off")})`}`)
            ),
            g.files?.length ? h("span", null, `${g.files.length} ${t("文件", "files")}`) : null,
            g.tags.map((tg) => h("span", { className: "sw-count-pill" }, tg))
          )
        ),
        h("div", { className: "sw-skill-actions" }, actionButtons)
      );
    });

    const listEl = state.projectLoading
      ? h("div", { className: "sw-empty" }, t("正在扫描项目工作区…", "Scanning project workspace..."))
      : visible.length
      ? h("div", { className: state.viewMode === "list" ? "sw-list" : "sw-grid" }, cards)
      : h("div", { className: "sw-empty" }, groups.length ? t("没有符合筛选条件的技能", "No skills match current filters") : t("当前项目暂未检测到任何技能，可点击上方 “+ 添加 Skills” 同步部署。", "No skills found in this project yet. Click '+ Add Skills' above."));

    return h("section", { className: "sw-main" }, headerCard, renderProjectPresetBar(project), renderConflictNotice(), filterToolbar, tagsRow, batchBar, listEl);
  }

  function render() {
    const mount = document.getElementById("viewSkills");
    if (!mount) return;
    if (mount.hidden) return;

    const sidebar = renderSidebar();
    let mainEl;
    if (state.routeKind === "library") {
      mainEl = renderLibraryMain();
    } else if (state.routeKind === "projects") {
      mainEl = renderProjectsOverviewMain();
    } else if (state.routeKind === "project" && state.selectedProjectId) {
      const proj = projectMap().get(state.selectedProjectId);
      mainEl = proj ? renderProjectDetailMain(proj) : renderProjectsOverviewMain();
    } else if (state.selectedAgentKey) {
      const ag = agentMap().get(state.selectedAgentKey);
      mainEl = ag && ag.installed ? renderAgentDetailMain(ag) : renderOverviewMain(state.routeKind === "lobster" ? "lobster" : "coding");
    } else {
      mainEl = renderOverviewMain(state.routeKind === "lobster" ? "lobster" : "coding");
    }

    withPreservedFocus(mount, () => {
      mount.replaceChildren(sidebar, mainEl);
    });
  }

  // -------------------- Public Lifecycle API --------------------
  window.SkillsWorkspace = {
    init() {
      if (state.initialized) return;
      state.initialized = true;
      parseHashIntoState();
    },
    async onActivate() {
      parseHashIntoState();
      if (!state.loadedOnce) {
        await loadAll(true);
      } else {
        if (state.selectedAgentKey) {
          await loadAgentSkills(state.selectedAgentKey, false);
        } else if (state.selectedProjectId) {
          await loadProjectData(state.selectedProjectId, false);
        }
        render();
      }
    },
    async handleHash() {
      return handleRouteChange();
    },
    async refreshOnHostChange() {
      await loadAll(false);
    },
  };
})();
