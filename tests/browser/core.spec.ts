import { expect, test } from "@playwright/test";

test.describe("public publishing baseline", () => {
  test("home is readable and secure", async ({ page }) => {
    const response = await page.goto("/");
    expect(response?.status()).toBe(200);
    await expect(page.locator("main")).toBeVisible();
    await expect(page.locator("h1").first()).toBeVisible();
    await expect(page.locator(".back-to-top")).toBeHidden();
    const footerLink = page.locator(".site-footer nav a").first();
    await expect(footerLink).toBeVisible();
    expect(await footerLink.evaluate((element) => {
      const rect = element.getBoundingClientRect();
      return rect.width >= 44 && rect.height >= 44;
    })).toBe(true);
    const taxonomyLinks = page.locator(".article-card .article-meta a");
    expect(await taxonomyLinks.evaluateAll((elements) => elements.every((element) => {
      const rect = element.getBoundingClientRect();
      return rect.width >= 32 && rect.height >= 32;
    }))).toBe(true);
    expect(await response?.headerValue("x-content-type-options")).toBe("nosniff");
    expect(await response?.headerValue("content-security-policy")).toContain("default-src");
  });

  test("admin remains protected and keyboard landmarks exist", async ({ page }) => {
    const response = await page.goto("/admin/plugins");
    expect([200, 303]).toContain(response?.status());
    if (response?.status() === 303) {
      await expect(page).toHaveURL(/\/admin\/(login|setup)/);
    }
    await page.keyboard.press("Tab");
    await expect(page.locator(":focus")).toBeVisible();
  });

  test("search controls keep one cross-browser height", async ({ page }) => {
    const response = await page.goto("/search?q=博客系统");
    expect(response?.status()).toBe(200);
    const heights = await page
      .locator(".search-form input, .search-form select, .search-form button")
      .evaluateAll((elements) => elements.map((element) => Math.round(element.getBoundingClientRect().height)));
    expect(heights.length).toBe(6);
    expect(new Set(heights).size).toBe(1);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1)).toBe(true);
  });

  test("hidden article form fields do not overlap visible inputs", async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto("/posts/demo-article-08");

    const trap = page.locator(".comment-trap > input");
    await expect(trap).toHaveCount(1);
    const rect = await trap.evaluate((element) => {
      const { width, height } = element.getBoundingClientRect();
      return { width, height };
    });
    expect(rect.width).toBeLessThanOrEqual(1);
    expect(rect.height).toBeLessThanOrEqual(1);
  });
});

test.describe("site sidebar navigation", () => {
  test("switches between a collapsible desktop rail and responsive drawers", async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto("/");

    const sidebar = page.locator("#public-sidebar");
    await expect(sidebar).toBeVisible();
    await expect(sidebar).toHaveAttribute("aria-hidden", "false");
    await expect(page.locator(".sidebar-toggle")).toBeHidden();
    await expect(page.locator("[data-sidebar-collapse]")).toBeVisible();
    await page.locator("[data-sidebar-collapse]").click();
    expect(await page.evaluate(() => document.documentElement.dataset.siteSidebar)).toBe("collapsed");
    await page.locator("[data-sidebar-collapse]").click();
    expect(await page.evaluate(() => document.documentElement.dataset.siteSidebar)).toBe("expanded");
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1)).toBe(true);

    await page.setViewportSize({ width: 1280, height: 900 });
    await page.reload();
    await expect(page.locator(".sidebar-toggle")).toBeVisible();
    await expect(sidebar).toHaveAttribute("aria-hidden", "true");
    await page.locator(".sidebar-toggle").click();
    await expect(sidebar).toHaveAttribute("aria-hidden", "false");
    await expect(page.locator(".sidebar-toggle")).toBeVisible();
    await expect(page.locator(".sidebar-toggle")).toHaveAttribute("aria-label", "关闭网站导航");
    await page.locator(".sidebar-toggle").click();
    await expect(sidebar).toHaveAttribute("aria-hidden", "true");
    await page.locator(".sidebar-toggle").click();
    await expect(sidebar).toHaveAttribute("aria-hidden", "false");
    await page.keyboard.press("Escape");
    await expect(sidebar).toHaveAttribute("aria-hidden", "true");
    await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight));
    expect(await page.locator(".sidebar-toggle").evaluate((element) => getComputedStyle(element).position)).toBe("fixed");
    expect(await page.locator(".sidebar-toggle").evaluate((element) => {
      const rect = element.getBoundingClientRect();
      return rect.top >= 0 && rect.bottom <= window.innerHeight;
    })).toBe(true);

    await page.setViewportSize({ width: 375, height: 812 });
    await page.reload();
    await expect(page.getByRole("button", { name: "打开网站导航" })).toBeVisible();
    await expect(sidebar).toHaveAttribute("aria-hidden", "true");

    await page.getByRole("button", { name: "打开网站导航" }).click();
    await expect(sidebar).toHaveAttribute("aria-hidden", "false");
    await expect(page.locator('[data-sidebar-scrim="site"]')).toBeVisible();
    await expect(page.locator(".site-header .sidebar-toggle")).toBeVisible();
    await expect(page.locator(".site-header .sidebar-toggle")).toHaveAttribute("aria-label", "关闭网站导航");
    expect(await page.evaluate(() => document.documentElement.dataset.sidebarOpen)).toBe("site");
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1)).toBe(true);

    await page.locator(".site-header .sidebar-toggle").click();
    await expect(sidebar).toHaveAttribute("aria-hidden", "true");
    expect(await page.evaluate(() => document.documentElement.dataset.sidebarOpen || "")).toBe("");
  });

  test("uses a right-side mobile table-of-contents drawer only when needed", async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto("/posts/demo-article-08");
    const desktopToc = page.locator("#article-toc");
    await expect(desktopToc).toBeVisible();
    await expect(page.locator(".article-toc-trigger")).toBeVisible();
    await expect(page.locator(".article-toc-trigger")).toHaveAttribute("aria-label", "收起文章目录");
    expect(await page.locator(".article-toc-trigger").evaluate((element) => getComputedStyle(element).position)).toBe("fixed");
    await desktopToc.getByRole("button", { name: "收起文章目录" }).click();
    await expect(desktopToc).toBeHidden();
    expect(await page.locator(".article-toc-trigger").evaluate((element) => getComputedStyle(element).position)).toBe("fixed");
    await expect(page.locator(".article-toc-trigger")).toHaveAttribute("aria-label", "展开文章目录");
    await page.locator(".article-toc-trigger").click();
    await expect(desktopToc).toBeVisible();

    await page.setViewportSize({ width: 375, height: 812 });
    await page.reload();

    await expect(page.locator(".article-toc-trigger")).toBeVisible();
    await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight));
    expect(await page.locator(".article-toc-trigger").evaluate((element) => getComputedStyle(element).position)).toBe("fixed");
    expect(await page.locator(".article-toc-trigger").evaluate((element) => {
      const rect = element.getBoundingClientRect();
      return rect.width === 48 && rect.height === 48 && rect.top >= 0 && rect.bottom <= window.innerHeight && rect.right <= window.innerWidth;
    })).toBe(true);
    const toc = page.locator("#article-toc");
    await expect(toc).toHaveAttribute("aria-hidden", "true");
    await page.getByRole("button", { name: /本文目录/ }).click();
    await expect(toc).toHaveAttribute("aria-hidden", "false");
    await expect(page.getByRole("button", { name: "关闭本文目录" })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.dataset.sidebarOpen)).toBe("toc");
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1)).toBe(true);

    await page.getByRole("button", { name: "关闭本文目录" }).click();
    await expect(toc).toHaveAttribute("aria-hidden", "true");

    await page.goto("/posts/demo-article-01");
    expect(await page.locator(".article-toc-trigger").count()).toBe(0);
    expect(await page.locator("#article-toc").count()).toBe(0);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1)).toBe(true);
  });

  test("keeps both navigation handles inside the viewport across responsive classes", async ({ page }) => {
    for (const [width, height] of [[1440, 900], [1280, 900], [1024, 900], [768, 900], [390, 844], [320, 700], [844, 390]]) {
      await page.setViewportSize({ width, height });
      await page.goto("/posts/demo-article-08");

      const metrics = await page.locator(".article-toc-trigger").evaluate((tocTrigger) => {
        const sidebarToggle = document.querySelector(".site-header .sidebar-toggle");
        const sidebarCollapse = document.querySelector("[data-sidebar-collapse]");
        const rect = tocTrigger.getBoundingClientRect();
        const style = getComputedStyle(tocTrigger);
        const sidebarToggleStyle = sidebarToggle ? getComputedStyle(sidebarToggle) : null;
        const sidebarCollapseStyle = sidebarCollapse ? getComputedStyle(sidebarCollapse) : null;
        return {
          toc: { position: style.position, width: rect.width, height: rect.height, top: rect.top, right: rect.right },
          site: { position: sidebarToggleStyle?.position, display: sidebarToggleStyle?.display },
          wideSite: sidebarCollapseStyle?.display,
          scrollWidth: document.documentElement.scrollWidth,
          viewportWidth: window.innerWidth,
          viewportHeight: window.innerHeight,
        };
      });

      expect(metrics.toc.position).toBe("fixed");
      expect(metrics.toc.width).toBe(48);
      expect(metrics.toc.height).toBe(48);
      expect(metrics.toc.top).toBeGreaterThanOrEqual(0);
      expect(metrics.toc.right).toBeLessThanOrEqual(width);
      expect(metrics.scrollWidth).toBeLessThanOrEqual(metrics.viewportWidth + 1);
      if (width >= 1440) {
        expect(metrics.wideSite).not.toBe("none");
      } else {
        expect(metrics.site.position).toBe("fixed");
        expect(metrics.site.display).not.toBe("none");
      }
    }
  });

  test("keeps drawer controls inside the viewport at unusual sizes", async ({ page }) => {
    for (const [width, height] of [[280, 600], [321, 700], [759, 500], [1023, 500], [1439, 400]]) {
      await page.setViewportSize({ width, height });
      await page.goto("/posts/demo-article-08");

      const siteToggle = page.locator(".site-header .sidebar-toggle");
      await siteToggle.click();
      await page.waitForTimeout(350);
      const siteOpen = await page.evaluate(() => {
        const toggle = document.querySelector<HTMLButtonElement>(".site-header .sidebar-toggle");
        const panel = document.querySelector<HTMLElement>("#public-sidebar");
        if (!toggle || !panel) return false;
        const buttonRect = toggle.getBoundingClientRect();
        const panelRect = panel.getBoundingClientRect();
        return document.documentElement.dataset.sidebarOpen === "site"
          && panel.getAttribute("aria-hidden") === "false"
          && buttonRect.x >= 0
          && buttonRect.y >= 0
          && buttonRect.right <= window.innerWidth + 1
          && buttonRect.bottom <= window.innerHeight + 1
          && panelRect.x >= 0
          && panelRect.right <= window.innerWidth + 1
          && document.documentElement.scrollWidth <= window.innerWidth + 1;
      });
      expect(siteOpen).toBe(true);
      await siteToggle.click();

      const tocToggle = page.locator(".article-toc-trigger");
      await tocToggle.click();
      await page.waitForTimeout(350);
      const tocOpen = await page.evaluate(() => {
        const toggle = document.querySelector<HTMLElement>(".article-toc-trigger");
        const panel = document.querySelector<HTMLElement>("#article-toc");
        if (!toggle || !panel) return false;
        const buttonRect = toggle.getBoundingClientRect();
        const panelRect = panel.getBoundingClientRect();
        return document.documentElement.dataset.sidebarOpen === "toc"
          && panel.getAttribute("aria-hidden") === "false"
          && buttonRect.x >= 0
          && buttonRect.y >= 0
          && buttonRect.right <= window.innerWidth + 1
          && buttonRect.bottom <= window.innerHeight + 1
          && panelRect.x >= 0
          && panelRect.right <= window.innerWidth + 1
          && document.documentElement.scrollWidth <= window.innerWidth + 1;
      });
      expect(tocOpen).toBe(true);
      await tocToggle.click();
    }
  });
});
