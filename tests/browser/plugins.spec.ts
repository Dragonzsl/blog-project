import { expect, test } from "@playwright/test";

// Opt-in only: initialize a disposable site and supply its private owner session.
// Never point this test at the user's working site.
test.describe("recoverable plugin deletion", () => {
  test.skip(!process.env.PLUGIN_SMOKE_STORAGE_STATE, "requires an isolated owner-session fixture");
  test.use({ storageState: process.env.PLUGIN_SMOKE_STORAGE_STATE });
  test.describe.configure({ mode: "serial" });
  const pluginFor = (project: string) => ({
    chromium: { id: "analytics.local", name: "本地统计" },
    firefox: { id: "contentapi.readonly", name: "只读内容 API" },
    webkit: { id: "comments.local", name: "本地评论" },
  })[project]!;

  test("confirm deletion, reject forged requests, and add the plugin again", async ({ page }, testInfo) => {
    const plugin = pluginFor(testInfo.project.name);
    await page.goto("/admin/plugins");
    const installed = page.getByRole("region", { name: "插件列表", exact: true });
    const card = installed.locator(".plugin-card").filter({ has: page.getByRole("heading", { name: plugin.name, exact: true }) });
    const form = card.locator('form[action$="/delete"]');
    const confirmation = form.getByRole("checkbox", { name: `确认删除 ${plugin.name}` });
    const button = form.getByRole("button", { name: "删除插件" });

    await button.click();
    await expect(confirmation).not.toBeChecked();
    await expect(card).toBeVisible();
    await confirmation.check();
    let acceptDeletion = false;
    page.on("dialog", (dialog) => acceptDeletion ? dialog.accept() : dialog.dismiss());
    await button.click();
    await expect(card).toBeVisible();

    const missingCSRF = await page.request.post(`/admin/plugins/${plugin.id}/delete`, {
      form: { confirm_action: "1" }, headers: { Origin: new URL(page.url()).origin },
    });
    expect(missingCSRF.status()).toBe(403);
    acceptDeletion = true;
    await button.click();
    await expect(page).toHaveURL(/notice=removed/);
    await expect(installed.getByRole("heading", { name: plugin.name, exact: true })).toHaveCount(0);
    const removed = page.getByRole("region", { name: "已删除插件", exact: true }).locator(".plugin-card").filter({ has: page.getByRole("heading", { name: plugin.name, exact: true }) });
    await expect(removed.getByRole("heading", { name: plugin.name, exact: true })).toBeVisible();
    await expect(page.getByRole("status")).toContainText("配置和数据已保留");
    await page.reload();
    await expect(removed.getByRole("button", { name: "重新添加" })).toBeVisible();
    await removed.getByRole("button", { name: "重新添加" }).click();
    await expect(page).toHaveURL(/notice=restored/);
    await expect(card.getByText("已停用", { exact: true })).toBeVisible();
    await expect(card.getByRole("button", { name: "启用", exact: true })).toBeVisible();
  });

  test("delete and restore on a narrow screen without JavaScript", async ({ browser, baseURL }, testInfo) => {
    const plugin = pluginFor(testInfo.project.name);
    const context = await browser.newContext({
      baseURL, storageState: process.env.PLUGIN_SMOKE_STORAGE_STATE,
      javaScriptEnabled: false, viewport: { width: 390, height: 844 },
    });
    const page = await context.newPage();
    try {
      await page.goto("/admin/plugins");
      const installed = page.getByRole("region", { name: "插件列表", exact: true });
      const card = installed.locator(".plugin-card").filter({ has: page.getByRole("heading", { name: plugin.name, exact: true }) });
      const confirmation = card.getByRole("checkbox", { name: `确认删除 ${plugin.name}` });
      await confirmation.check();
      await card.getByRole("button", { name: "删除插件" }).click();
      await expect(page).toHaveURL(/notice=removed/);
      const removed = page.getByRole("region", { name: "已删除插件", exact: true }).locator(".plugin-card").filter({ has: page.getByRole("heading", { name: plugin.name, exact: true }) });
      await expect(removed.getByRole("button", { name: "重新添加" })).toBeVisible();
      expect(await page.locator("html").evaluate((element) => element.scrollWidth <= window.innerWidth + 1)).toBe(true);
      await page.screenshot({ path: testInfo.outputPath("plugin-removal-mobile.png"), fullPage: true });
      await removed.getByRole("button", { name: "重新添加" }).click();
      await expect(card.getByText("已停用", { exact: true })).toBeVisible();
    } finally {
      await context.close();
    }
  });
});
