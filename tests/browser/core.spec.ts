import { expect, test } from "@playwright/test";

test.describe("public publishing baseline", () => {
  test("home is readable and secure", async ({ page }) => {
    const response = await page.goto("/");
    expect(response?.status()).toBe(200);
    await expect(page.locator("main")).toBeVisible();
    await expect(page.locator("h1").first()).toBeVisible();
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
});
