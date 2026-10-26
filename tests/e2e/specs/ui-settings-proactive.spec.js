// (5) settings shows expected UI version; proactive card and back-button badge still render.
const { test } = require('@playwright/test');
const { EXPECT_UI_VERSION } = require('../env');
const { S, requireIdentity, expect } = require('./ui-helpers');
test.beforeEach(requireIdentity);

test(`settings shows v${EXPECT_UI_VERSION}`, async ({ page }) => {
  await page.goto('/ui/');
  await page.evaluate(() => document.querySelector('#openSettings')?.click() || document.querySelector('#settings')?.showModal());
  await expect(page.locator(S.version)).toContainText(`v${EXPECT_UI_VERSION}`);
});
test('proactive card renders in conversation list', async ({ page }) => {
  await page.goto('/ui/#/');
  const n = await page.locator(S.proactive).count();
  test.skip(n === 0, 'no proactive (선톡) item for this identity yet');
  await expect(page.locator(S.proactive).first()).toBeVisible();
});
test('back button badge renders for unread', async ({ page }) => {
  await page.goto('/ui/');
  await expect(page.locator(S.backBtn)).toBeAttached();
  await expect(page.locator(S.backBadge)).toBeAttached();
  const unread = await page.locator(`${S.convList} .unread`).count();
  test.skip(unread === 0, 'no unread proactive thread to show a badge');
  await expect(page.locator(S.backBadge)).toBeVisible();
});
