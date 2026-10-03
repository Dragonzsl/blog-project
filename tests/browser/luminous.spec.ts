import { expect, test } from "@playwright/test";

// Use a disposable instance with luminous-editorial active and a headed article.
test.describe("luminous editorial interaction", () => {
  test.skip(!process.env.LUMINOUS_BASE_URL || !process.env.LUMINOUS_ARTICLE_FIXTURE, "requires isolated luminous theme fixtures");
  test.use({ baseURL: process.env.LUMINOUS_BASE_URL });

  test("desktop navigation does not cover the page", async ({ page }) => {
    for (const width of [1024, 1200, 1440]) {
      await page.setViewportSize({ width, height: 900 });
      await page.goto("/");
      await expect(page.locator("#public-sidebar")).toBeHidden();
      await expect(page.locator(".primary-nav")).toBeVisible();
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
    }
  });

  test("directory links remain clickable across drawer breakpoints", async ({ page }) => {
    for (const width of [280, 390, 820, 1024, 1200, 1439, 1440]) {
      await page.setViewportSize({ width, height: 800 });
      await page.goto(process.env.LUMINOUS_ARTICLE_FIXTURE!);
      const toc = page.locator("#article-toc");
      const toggle = page.locator(".article-toc-trigger");
      if (width < 1440) {
        await expect(toc).toBeHidden();
        await toggle.click();
        await expect(toc).toBeVisible();
        await expect(toggle).toHaveAttribute("aria-expanded", "true");
        await page.keyboard.press("Escape");
        await expect(toc).toBeHidden();
        await expect(toggle).toBeFocused();
        await toggle.click();
      }
      await expect(toc).toBeVisible();
      const first = toc.locator('a[href^="#"]').first();
      const target = await first.getAttribute("href");
      await first.click();
      await expect.poll(() => decodeURIComponent(new URL(page.url()).hash)).toBe(target);
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
    }
  });

  test("mobile navigation closes and restores keyboard focus", async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto("/");
    const toggle = page.locator('.sidebar-toggle[aria-controls="public-sidebar"]');
    await toggle.click();
    await expect(page.locator("#public-sidebar")).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(page.locator("#public-sidebar")).toBeHidden();
    await expect(toggle).toBeFocused();
  });

  test("custom accent and directory work without JavaScript", async ({ browser }) => {
    const context = await browser.newContext({ baseURL: process.env.LUMINOUS_BASE_URL, javaScriptEnabled: false, viewport: { width: 390, height: 844 } });
    try {
      const page = await context.newPage();
      const response = await page.goto(process.env.LUMINOUS_ARTICLE_FIXTURE!);
      expect(await response?.headerValue("content-security-policy")).not.toContain("unsafe-inline");
      expect(await response?.headerValue("content-security-policy")).toContain("style-src 'self' 'sha256-");
      const accent = process.env.LUMINOUS_ACCENT || "#6C5CE7";
      expect(await page.locator("body").evaluate(el => getComputedStyle(el).getPropertyValue("--accent").trim())).toBe(accent);
      await expect(page.locator("#article-toc")).toBeVisible();
      await expect(page.locator("#article-toc")).toHaveAttribute("aria-hidden", "false");
      await page.locator('#article-toc a[href^="#"]').first().click();
      expect(new URL(page.url()).hash).not.toBe("");
    } finally { await context.close(); }
  });
});
