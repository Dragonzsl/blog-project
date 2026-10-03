import { expect, test } from "@playwright/test";

// Only run against a disposable site with an owner-session fixture.
test.describe("recoverable theme deletion", () => {
  test.skip(!process.env.THEME_SMOKE_STORAGE_STATE || !process.env.THEME_SMOKE_PACKAGE, "requires isolated session and theme ZIP");
  test.use({ storageState: process.env.THEME_SMOKE_STORAGE_STATE });
  test("remove and restore a theme with no JavaScript on a narrow screen", async ({ browser, baseURL }) => {
    const context = await browser.newContext({ baseURL, storageState: process.env.THEME_SMOKE_STORAGE_STATE, javaScriptEnabled: false, viewport: { width: 390, height: 844 } });
    const page = await context.newPage();
    try {
      await page.goto("/admin/themes");
      let installed = page.getByRole("region", { name: "已安装主题", exact: true });
      let card = installed.locator(".theme-card").filter({ has: page.getByRole("heading", { name: "删除测试主题", exact: true }) });
      if (!await card.count()) {
        await page.getByLabel("主题 ZIP 包").setInputFiles(process.env.THEME_SMOKE_PACKAGE!);
        await page.getByRole("button", { name: "上传并验证" }).click();
        await expect(page).toHaveURL(/notice=uploaded/);
      }
      await expect(card).toBeVisible();
      const confirm = card.getByRole("checkbox", { name: "确认删除 删除测试主题 · 1.0.0" });
      await card.getByRole("button", { name: "删除主题", exact: true }).click();
      await expect(confirm).not.toBeChecked();
      const csrf = await page.locator('input[name="csrf_token"]').first().inputValue();
      const rejected = await page.request.post("/admin/themes/smoke-delete/1.0.0/delete", { form: { confirm_action: "1" }, headers: { Origin: new URL(page.url()).origin } });
      expect(rejected.status()).toBe(403);
      const missingConfirmation = await page.request.post("/admin/themes/smoke-delete/1.0.0/delete", { form: { csrf_token: csrf }, headers: { Origin: new URL(page.url()).origin } });
      expect(missingConfirmation.status()).toBe(422);
      await confirm.check();
      await card.getByRole("button", { name: "删除主题", exact: true }).click();
      await expect(page).toHaveURL(/notice=removed/);
      await expect(card).toHaveCount(0);
      const removed = page.getByRole("region", { name: "已删除主题", exact: true });
      await expect(removed.getByRole("heading", { name: "删除测试主题" })).toBeVisible();
      await page.reload();
      expect(await page.locator("html").evaluate(el => el.scrollWidth <= window.innerWidth + 1)).toBe(true);
      await page.screenshot({ path: test.info().outputPath("theme-delete-mobile.png"), fullPage: true });
      await removed.getByRole("button", { name: "重新添加" }).click();
      await expect(page).toHaveURL(/notice=restored/);
      await expect(card.getByRole("button", { name: "启用", exact: true })).toBeVisible();
      await card.getByRole("button", { name: "启用", exact: true }).click();
      await expect(page).toHaveURL(/notice=activated/);
      await expect(card.getByRole("button", { name: "删除主题", exact: true })).toHaveCount(0);
      const activeDeletion = await page.request.post("/admin/themes/smoke-delete/1.0.0/delete", { form: { csrf_token: csrf, confirm_action: "1" }, headers: { Origin: new URL(page.url()).origin } });
      expect(activeDeletion.status()).toBe(422);
      await page.getByRole("button", { name: "回退到默认主题" }).click();
      await expect(page).toHaveURL(/notice=rollback/);
    } finally { await context.close(); }
  });
});
