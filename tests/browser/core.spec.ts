import { expect, test } from "@playwright/test";

test.describe("default theme public experience", () => {
  test("admin remains protected without a session", async ({ page }) => {
    await page.goto("/admin/plugins");
    await expect(page).toHaveURL(/\/admin\/(login|setup)/);
    await page.keyboard.press("Tab");
    await expect(page.locator(":focus")).toBeVisible();
  });

  test("home preserves security, keyboard access and compact featured layout", async ({ page, browserName }) => {
    const response = await page.goto("/");
    expect(response?.status()).toBe(200);
    expect(await response?.headerValue("x-content-type-options")).toBe("nosniff");
    expect(await response?.headerValue("content-security-policy")).toContain("default-src");
    await expect(page.locator("main h1")).toBeVisible();
    await expect(page.locator(".back-to-top")).toBeHidden();
    // Safari's default keyboard mode uses Option+Tab to include links.
    await page.keyboard.press(browserName === "webkit" ? "Alt+Tab" : "Tab");
    await expect(page.locator(".skip-link")).toBeFocused();
    await expect.poll(() => page.locator(".skip-link").evaluate(el => el.getBoundingClientRect().top)).toBeGreaterThanOrEqual(0);
    const featured = page.locator(".featured-card");
    if (await featured.count()) {
      const heading = await page.locator("#featured-heading").boundingBox();
      const card = await featured.boundingBox();
      expect(heading!.y + heading!.height).toBeLessThan(card!.y);
      expect(card!.height).toBeLessThan(600);
      expect(await featured.evaluate(el => el.querySelectorAll("a").length)).toBe(0);
    }
  });

  test("header stays on home only and icons are centered", async ({ page }) => {
    await page.goto("/");
    expect(await page.locator(".site-header").evaluate(el => getComputedStyle(el).position)).toBe("sticky");
    await page.evaluate(() => window.scrollTo(0, 600));
    expect(await page.locator(".site-header").evaluate(el => el.getBoundingClientRect().top)).toBeGreaterThanOrEqual(0);
    for (const selector of [".header-search", ".site-header .theme-toggle"]) {
      const offsets = await page.locator(selector).evaluate(el => {
        const button = el.getBoundingClientRect(), icon = el.querySelector("svg")!.getBoundingClientRect();
        return [Math.abs(button.x + button.width / 2 - icon.x - icon.width / 2), Math.abs(button.y + button.height / 2 - icon.y - icon.height / 2)];
      });
      expect(Math.max(...offsets)).toBeLessThan(1);
    }
    for (const path of ["/articles", "/archive"]) {
      await page.goto(path);
      expect(await page.locator(".site-header").evaluate(el => getComputedStyle(el).position)).toBe("relative");
    }
  });

  test("all public pages fit narrow, short and wide viewports", async ({ page }) => {
    for (const colorScheme of ["light", "dark"] as const) {
      await page.emulateMedia({ colorScheme });
      for (const [width, height] of [[1440, 900], [820, 700], [390, 844], [280, 600], [844, 390]]) {
        await page.setViewportSize({ width, height });
        for (const path of ["/", "/articles", "/archive", "/categories", "/tags", "/search?q=山居", "/missing-review-page"]) {
          await page.goto(path);
          expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), `${path} at ${width}`).toBe(true);
          if (path.startsWith("/search")) {
            const box = await page.locator(".spotlight").boundingBox();
            expect(box!.y + box!.height).toBeLessThanOrEqual(height + 1);
            await expect(page.locator("[data-search-close]")).toBeVisible();
          }
        }
      }
    }
  });

  test("Spotlight opens from icon and shortcut, restores focus and searches", async ({ page }) => {
    await page.goto("/");
    const trigger = page.locator("[data-search-open]");
    await trigger.click();
    await expect(page.locator(".spotlight")).toBeVisible();
    await expect(page.locator("#spotlight-query")).toBeFocused();
    await page.locator("#spotlight-query").fill("山居");
    await expect(page.locator("[data-search-status]")).toContainText("找到");
    await page.locator("#spotlight-query").fill("山");
    await expect(page.locator("[data-search-results]")).toBeEmpty();
    await expect(page.locator("[data-search-status]")).toContainText("至少 2");
    await page.keyboard.press("Escape");
    await expect(page.locator(".spotlight")).toBeHidden();
    await expect(trigger).toBeFocused();
    await page.keyboard.press("Control+k");
    await expect(page.locator("#spotlight-query")).toBeFocused();
    await page.locator("[data-search-close]").click();
    await expect(page.locator(".spotlight")).toBeHidden();
    await expect(page).toHaveURL(/\/$/);
  });

  test("a delayed response cannot replace a newer query", async ({ page }) => {
    let release!: () => void;
    let started!: () => void;
    const pending = new Promise<void>(resolve => { started = resolve; });
    const gate = new Promise<void>(resolve => { release = resolve; });
    await page.route("**/search?q=old-query", async route => {
      started();
      await gate;
      await route.fulfill({ contentType: "text/html", body: '<div data-search-results>stale result</div><span data-search-status>stale</span>' }).catch(() => {});
    });
    await page.goto("/");
    await page.locator("[data-search-open]").click();
    await page.locator("#spotlight-query").fill("old-query");
    await pending;
    await page.locator("#spotlight-query").fill("x");
    release();
    await expect(page.locator("[data-search-status]")).toContainText("至少 2");
    await expect(page.locator("[data-search-results]")).toBeEmpty();
  });

  test("public navigation and search work without JavaScript", async ({ browser, baseURL }) => {
    const context = await browser.newContext({ javaScriptEnabled: false, baseURL, ignoreHTTPSErrors: true });
    try {
      const page = await context.newPage();
      await page.goto("/");
      await page.getByRole("link", { name: "搜索文章", exact: true }).click();
      await page.locator("#spotlight-query").fill("山居");
      await page.locator("#spotlight-query").press("Enter");
      await expect(page).toHaveURL(/\/search\?q=/);
      await expect(page.locator("[data-search-status]")).toContainText("找到");
      await page.locator("[data-search-close]").click();
      await expect(page).toHaveURL(/\/$/);
    } finally { await context.close(); }
  });
});

test.describe("isolated article fixture", () => {
  test.skip(!process.env.THEME_ARTICLE_FIXTURE, "requires a published fixture with headings");
  test("table of contents remains usable on desktop and mobile", async ({ page }) => {
    for (const width of [1440, 390, 280]) {
      await page.setViewportSize({ width, height: 700 });
      await page.goto(process.env.THEME_ARTICLE_FIXTURE!);
      const toc = page.locator("#article-toc");
      const toggle = page.locator(".article-toc-trigger");
      await expect(toggle).toBeVisible();
      if (width < 1440) await toggle.click();
      await expect(toc).toBeVisible();
      await toc.locator('a[href^="#"]').first().click();
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
    }
  });
});
