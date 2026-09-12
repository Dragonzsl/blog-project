(() => {
  "use strict";

  const themeStorageKey = "blog-theme";

  const copyText = async (value) => {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(value);
      return;
    }
    const input = document.createElement("textarea");
    input.value = value;
    input.setAttribute("readonly", "");
    input.style.position = "fixed";
    input.style.opacity = "0";
    document.body.append(input);
    input.select();
    if (!document.execCommand("copy")) throw new Error("copy failed");
    input.remove();
  };

  const setupTheme = () => {
    const root = document.documentElement;
    const toggles = [...document.querySelectorAll("[data-theme-toggle]")];
    const systemTheme = () => window.matchMedia?.("(prefers-color-scheme: dark)").matches ? "dark" : "light";
    const modes = ["system", "light", "dark"];
    let mode = "system";
    try {
      const saved = localStorage.getItem(themeStorageKey);
      if (modes.includes(saved)) mode = saved;
    } catch {
      // The system preference remains the safe fallback.
    }
    const modeLabel = (value) => value === "system" ? "自动跟随系统" : (value === "dark" ? "暗色" : "亮色") + "模式";
    const sync = () => {
      const theme = mode === "system" ? systemTheme() : mode;
      if (mode === "system") {
        root.removeAttribute("data-theme");
        root.style.colorScheme = "light dark";
      } else {
        root.dataset.theme = mode;
        root.style.colorScheme = mode;
      }
      toggles.forEach((toggle) => {
        const nextMode = modes[(modes.indexOf(mode) + 1) % modes.length];
        toggle.dataset.themeMode = mode;
        toggle.dataset.themeEffective = theme;
        toggle.setAttribute("aria-pressed", String(mode !== "system"));
        toggle.setAttribute("aria-label", "主题：" + modeLabel(mode) + "，当前为" + (theme === "dark" ? "暗色" : "亮色") + "，点击切换为" + modeLabel(nextMode));
        toggle.title = "当前：" + modeLabel(mode) + " · 点击切换为" + modeLabel(nextMode);
        const label = toggle.querySelector("[data-theme-toggle-label]");
        if (label) label.textContent = mode === "system" ? "自动 · " + (theme === "dark" ? "暗色" : "亮色") : modeLabel(mode);
      });
    };
    toggles.forEach((toggle) => {
      toggle.addEventListener("click", () => {
        mode = modes[(modes.indexOf(mode) + 1) % modes.length];
        try {
          localStorage.setItem(themeStorageKey, mode);
        } catch {
          // The current page still updates when storage is unavailable.
        }
        sync();
      });
    });
    sync();
    const media = window.matchMedia?.("(prefers-color-scheme: dark)");
    media?.addEventListener?.("change", () => { if (mode === "system") sync(); });
  };

  const setupSidebars = () => {
    const root = document.documentElement;
    const panels = [...document.querySelectorAll("[data-sidebar-panel]")];
    const drawers = new Map();
    if (!panels.length) return;
    const isOverlay = (panel) => window.innerWidth <= Number(panel.dataset.sidebarBreakpoint || 0);
    panels.forEach((panel) => {
      const name = panel.dataset.drawerName || panel.id;
      const toggles = [...document.querySelectorAll(`[data-sidebar-toggle][aria-controls="${panel.id}"]`)];
      const closeButtons = [...panel.querySelectorAll("[data-sidebar-close]")];
      const scrim = document.querySelector(`[data-sidebar-scrim="${name}"]`);
      let open = false;
      const sync = () => {
        const overlay = isOverlay(panel);
        if (!overlay) open = false;
        panel.setAttribute("aria-hidden", String(overlay ? !open : false));
        toggles.forEach((toggle) => toggle.setAttribute("aria-expanded", String(overlay && open)));
        if (scrim) scrim.hidden = !(overlay && open);
        if (root.dataset.sidebarOpen === name && !(overlay && open)) delete root.dataset.sidebarOpen;
      };
      const close = (restore = true) => {
        if (!open && root.dataset.sidebarOpen !== name) { sync(); return; }
        open = false;
        sync();
        if (restore) toggles[0]?.focus({ preventScroll: true });
      };
      const openPanel = () => {
        if (!isOverlay(panel)) return;
        drawers.forEach((drawer, drawerName) => { if (drawerName !== name) drawer.close(false); });
        open = true;
        root.dataset.sidebarOpen = name;
        sync();
        window.requestAnimationFrame(() => panel.querySelector("a[href],button:not([disabled])")?.focus());
      };
      toggles.forEach((toggle) => toggle.addEventListener("click", () => (open ? close() : openPanel())));
      closeButtons.forEach((button) => button.addEventListener("click", () => close()));
      scrim?.addEventListener("click", () => close());
      panel.querySelectorAll("a[href]").forEach((link) => link.addEventListener("click", () => { if (isOverlay(panel)) close(false); }));
      panel.addEventListener("keydown", (event) => {
        if (event.key !== "Tab" || !isOverlay(panel) || !open) return;
        const focusable = [...panel.querySelectorAll("a[href],button:not([disabled])")];
        if (!focusable.length) { event.preventDefault(); return; }
        const last = focusable[focusable.length - 1];
        if (event.shiftKey && document.activeElement === focusable[0]) { event.preventDefault(); last.focus(); }
        else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); focusable[0].focus(); }
      });
      drawers.set(name, { close, sync });
      sync();
    });
    document.addEventListener("keydown", (event) => {
      if (event.key === "Escape" && root.dataset.sidebarOpen) drawers.get(root.dataset.sidebarOpen)?.close();
    });
    window.addEventListener("resize", () => drawers.forEach((drawer) => drawer.sync()), { passive: true });
  };

  const setupAdminSidebarCollapse = () => {
    const root = document.documentElement;
    const panel = document.querySelector("#admin-sidebar");
    const button = panel?.querySelector("[data-sidebar-collapse]");
    if (!panel || !button) return;
    const storageKey = "blog-admin-sidebar";
    const wide = () => window.innerWidth >= 1024;
    let collapsed = false;
    try {
      collapsed = localStorage.getItem(storageKey) === "collapsed";
    } catch {
      collapsed = false;
    }
    const sync = () => {
      if (wide()) {
        root.dataset.adminSidebar = collapsed ? "collapsed" : "expanded";
        button.hidden = false;
        button.setAttribute("aria-expanded", String(!collapsed));
        button.setAttribute("aria-label", collapsed ? "展开工作台菜单" : "收起工作台菜单");
        const text = button.querySelector(".visually-hidden");
        if (text) text.textContent = collapsed ? "展开工作台菜单" : "收起工作台菜单";
      } else {
        root.removeAttribute("data-admin-sidebar");
        button.hidden = true;
      }
    };
    button.addEventListener("click", () => {
      if (!wide()) return;
      collapsed = !collapsed;
      try {
        localStorage.setItem(storageKey, collapsed ? "collapsed" : "expanded");
      } catch {
        // Keep the current layout usable when storage is unavailable.
      }
      sync();
    });
    window.addEventListener("resize", sync, { passive: true });
    sync();
  };

  const replaceSelection = (textarea, replacement, selectionStart, selectionEnd) => {
    const start = textarea.selectionStart;
    const end = textarea.selectionEnd;
    textarea.setRangeText(replacement, start, end, "select");
    textarea.focus();
    if (selectionStart !== undefined && selectionEnd !== undefined) {
      textarea.setSelectionRange(start + selectionStart, start + selectionEnd);
    }
    textarea.dispatchEvent(new Event("input", { bubbles: true }));
  };

  const markdownReplacement = (action, selected) => {
    const value = selected || "";
    switch (action) {
      case "bold":
        return { text: `**${value || "加粗文字"}**`, start: 2, end: 2 + (value || "加粗文字").length };
      case "italic":
        return { text: `*${value || "强调文字"}*`, start: 1, end: 1 + (value || "强调文字").length };
      case "strike":
        return { text: `~~${value || "删除线文字"}~~`, start: 2, end: 2 + (value || "删除线文字").length };
      case "inline-code":
        return { text: `\`${value || "代码"}\``, start: 1, end: 1 + (value || "代码").length };
      case "heading":
        return { text: (value || "小节标题").split("\n").map((line) => `## ${line}`).join("\n") };
      case "quote":
        return { text: (value || "引用内容").split("\n").map((line) => `> ${line}`).join("\n") };
      case "unordered-list":
        return { text: (value || "列表项").split("\n").map((line) => `- ${line}`).join("\n") };
      case "ordered-list":
        return { text: (value || "列表项").split("\n").map((line, index) => `${index + 1}. ${line}`).join("\n") };
      case "code-block":
        return { text: `\`\`\`text\n${value || "代码"}\n\`\`\`` };
      case "link":
        return { text: `[${value || "链接文字"}](https://example.com)`, start: 1, end: 1 + (value || "链接文字").length };
      case "image":
        return { text: `![${value || "图片说明"}](/media/example.jpg)`, start: 2, end: 2 + (value || "图片说明").length };
      case "rule":
        return { text: "\n---\n" };
      default:
        return null;
    }
  };

  const setupMarkdownToolbar = () => {
    const textarea = document.querySelector("textarea.markdown-editor");
    if (!textarea) {
      return;
    }
    document.querySelectorAll("[data-md-action]").forEach((button) => {
      button.addEventListener("click", () => {
        const action = button.dataset.mdAction;
        if (action === "undo" || action === "redo") {
          textarea.focus();
          document.execCommand(action);
          return;
        }
        const replacement = markdownReplacement(action, textarea.value.slice(textarea.selectionStart, textarea.selectionEnd));
        if (replacement) {
          replaceSelection(textarea, replacement.text, replacement.start, replacement.end);
        }
      });
    });
    textarea.addEventListener("keydown", (event) => {
      if (!(event.metaKey || event.ctrlKey) || event.altKey) {
        return;
      }
      const action = { b: "bold", i: "italic", k: "link" }[event.key.toLowerCase()];
      if (!action) {
        return;
      }
      event.preventDefault();
      const replacement = markdownReplacement(action, textarea.value.slice(textarea.selectionStart, textarea.selectionEnd));
      replaceSelection(textarea, replacement.text, replacement.start, replacement.end);
    });
  };

  const focusHashTarget = () => {
    if (!window.location.hash) {
      return;
    }
    let id = window.location.hash.slice(1);
    try {
      id = decodeURIComponent(id);
    } catch {
      return;
    }
    const target = document.getElementById(id);
    if (target && typeof target.focus === "function") {
      window.setTimeout(() => target.focus({ preventScroll: true }), 0);
    }
  };

  const setupOperationAnchors = () => {
    document.querySelectorAll(".content-status-tabs a, .admin-pagination a").forEach((link) => {
      link.addEventListener("click", (event) => {
        const href = link.getAttribute("href");
        if (!href || href.includes("#") || href.startsWith("javascript:")) {
          return;
        }
        event.preventDefault();
        window.location.assign(`${href}#content-results`);
      });
    });
  };

  const setupSubmitFeedback = () => {
    document.querySelectorAll("[data-submit-feedback]").forEach((form) => {
      form.addEventListener("submit", (event) => {
        const message = form.dataset.confirm;
        if (message && !window.confirm(message)) {
          event.preventDefault();
          return;
        }
        form.setAttribute("aria-busy", "true");
        form.querySelectorAll("button[type='submit']").forEach((button) => {
          button.disabled = true;
          button.dataset.originalLabel = button.textContent;
          button.textContent = "处理中…";
        });
      });
    });
  };

  const setupCopyButtons = () => {
    document.addEventListener("click", async (event) => {
      const button = event.target.closest("[data-copy-value]");
      if (!button) return;
      const label = button.dataset.copyLabel || button.textContent;
      try {
        await copyText(button.dataset.copyValue || "");
        button.textContent = "已复制";
        button.dataset.state = "success";
        window.setTimeout(() => { button.textContent = label; button.dataset.state = ""; }, 2200);
      } catch {
        button.textContent = "复制失败";
        window.setTimeout(() => { button.textContent = label; }, 2200);
      }
    });
  };

  const setupMediaControls = () => {
    const filter = document.querySelector("[data-media-filter]");
    const library = document.querySelector("[data-media-library]");
    const cards = library ? [...library.querySelectorAll("[data-media-kind]")] : [];
    filter?.addEventListener("change", () => {
      cards.forEach((card) => { card.hidden = filter.value !== "all" && card.dataset.mediaKind !== filter.value; });
    });
    document.querySelectorAll("[data-media-view]").forEach((button) => {
      button.addEventListener("click", () => {
        const view = button.dataset.mediaView;
        if (library) library.dataset.view = view;
        document.querySelectorAll("[data-media-view]").forEach((item) => item.setAttribute("aria-pressed", String(item === button)));
      });
    });
    document.querySelector("[data-file-input]")?.addEventListener("change", (event) => {
      const name = event.target.files?.[0]?.name || "未选择文件";
      const target = document.querySelector("[data-file-name]");
      if (target) target.textContent = name;
    });
  };

  if ("scrollRestoration" in window.history) {
    window.history.scrollRestoration = "auto";
  }
  setupTheme();
  setupSidebars();
  setupAdminSidebarCollapse();
  setupMarkdownToolbar();
  setupOperationAnchors();
  setupSubmitFeedback();
  setupCopyButtons();
  setupMediaControls();
  focusHashTarget();
  window.addEventListener("hashchange", focusHashTarget);
})();
