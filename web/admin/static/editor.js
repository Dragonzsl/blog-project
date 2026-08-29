(() => {
  const slugFromTitle = (value, fallback) => {
    let normalized = value;
    try {
      normalized = value.normalize("NFC");
    } catch (_) {
      // Older browsers can still use the unnormalized input.
    }
    let result = "";
    let pendingHyphen = false;
    for (const character of normalized.trim()) {
      if (/[\p{L}\p{N}]/u.test(character)) {
        result += character.toLowerCase();
        pendingHyphen = false;
      } else if (result && !pendingHyphen) {
        result += "-";
        pendingHyphen = true;
      }
    }
    return result.replace(/-+$/, "") || fallback;
  };

  const setupAutoSlug = () => {
    const form = document.querySelector("#content-form");
    const title = form?.querySelector('input[name="title"]');
    const slug = form?.querySelector("[data-slug-input]");
    const mode = form?.querySelector('input[name="auto_slug"]');
    if (!title || !slug || !mode) return;

    const initialTitle = title.value;
    const initialSlug = slug.value;
    const initialAuto = mode.value === "1";
    const fallback = slug.dataset.slugFallback === "page" ? "page" : "article";
    const refresh = () => {
      if (!title.value.trim()) {
        slug.value = "";
        return;
      }
      slug.value = slugFromTitle(title.value, fallback);
    };
    title.addEventListener("input", () => {
      if (!initialAuto && title.value === initialTitle) {
        mode.value = "0";
        slug.value = initialSlug;
        return;
      }
      mode.value = "1";
      refresh();
    });
    if (initialAuto && title.value.trim()) refresh();
  };

  const form = document.querySelector("[data-snapshot-url]");
  const status = document.querySelector("[data-snapshot-status]");
  setupAutoSlug();
  if (!form || !status) return;

  const interval = Number(form.dataset.snapshotInterval || 15000);
  let changeVersion = 0;
  let savedVersion = 0;
  let browserVersion = Date.now();

  const markChanged = () => {
    changeVersion += 1;
    status.textContent = "有尚未快照的编辑";
  };
  form.addEventListener("input", markChanged);
  form.addEventListener("change", markChanged);

  const save = async () => {
    if (changeVersion === savedVersion || document.hidden) return;
    const capturedVersion = changeVersion;
    browserVersion = Math.max(Date.now(), browserVersion + 1);
    const body = new URLSearchParams(new FormData(form));
    body.set("browser_version", String(browserVersion));
    status.textContent = "正在保存编辑快照…";
    try {
      const response = await fetch(form.dataset.snapshotUrl, {
        method: "POST",
        body,
        credentials: "same-origin",
        headers: { Accept: "application/json" },
      });
      if (response.status === 204) {
        savedVersion = Math.max(savedVersion, capturedVersion);
        status.textContent = capturedVersion === changeVersion ? "编辑快照已保存" : "新编辑等待下次快照";
      } else if (response.status === 409) {
        status.textContent = "内容版本已变化，请刷新后合并编辑";
        window.clearInterval(timer);
      } else {
        status.textContent = "编辑快照暂未保存，将自动重试";
      }
    } catch (_) {
      status.textContent = "网络不可用，编辑仍保留在当前页面";
    }
  };

  const timer = window.setInterval(save, Math.max(5000, interval));
})();
