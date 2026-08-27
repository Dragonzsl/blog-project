import { expect, test } from "@playwright/test";

test.describe("article display smoke", () => {
  test.skip(!process.env.ARTICLE_SMOKE, "requires an isolated published article fixture");

  test("keeps title scale and long-title layout bounded", async ({ page }) => {
    const response = await page.goto("/posts/markdown-migrated-post");
    expect(response?.status()).toBe(200);

    const readMetrics = () =>
      page.locator(".article-header h1").evaluate((element) => {
        const style = getComputedStyle(element);
        const rect = element.getBoundingClientRect();
        return {
          fontSize: Number.parseFloat(style.fontSize),
          height: rect.height,
          right: rect.right,
        };
      });

    const desktop = await readMetrics();
    expect(desktop.fontSize).toBeGreaterThanOrEqual(48);
    expect(desktop.fontSize).toBeLessThanOrEqual(96);

    await page.setViewportSize({ width: 566, height: 863 });
    await page.reload();
    const mobile = await readMetrics();
    expect(mobile.fontSize).toBeGreaterThanOrEqual(44.8);
    expect(mobile.fontSize).toBeLessThanOrEqual(64);

    await page.locator(".article-header h1").evaluate((element) => {
      element.textContent = "一个非常长的中文文章标题用于检验响应式排版以及Safari高度";
    });
    const longTitle = await readMetrics();
    expect(longTitle.height).toBeLessThan(300);
    expect(longTitle.right).toBeLessThanOrEqual(566);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1)).toBe(true);
  });
});
