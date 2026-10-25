// console-dashboard FR-001・FR-002・FR-006〜FR-008（ブラウザ）：ログイン後のダッシュボード、目標の設定、要対応から契約の一覧へ、スマートフォンの幅。
import { test, expect } from "../../fixtures/index.ts";
import { createPlan } from "../../helpers/catalog.ts";
import { CONSOLE, signInAsNewStaff } from "../../helpers/console.ts";
import { subscribe } from "../../helpers/orchestration.ts";
import { untilOverview } from "../../helpers/reporting.ts";

test("FR-001・FR-002・FR-006: ダッシュボードに MRR と KPI が出て、目標を設定すると達成率が出る。要対応から絞り込んだ契約の一覧に移れる", async ({ page, api, world, mail, wait }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 1960 });
  await subscribe(api, world, tenant, plan.id);
  await untilOverview(api, wait, tenant, (o) => o.kgi.mrr === 1960, "申込が集計される");
  await signInAsNewStaff(page, api, mail, wait, tenant, "admin", "dash");

  await expect(page.getByTestId("kgi-mrr")).toHaveText("1,960円");
  await expect(page.getByTestId("kgi-net-new")).toHaveText("+1,960円");
  await expect(page.getByTestId("kpi-new-subscriptions-value")).toHaveText("1 件");
  await expect(page.getByTestId("kpi-revenue-value")).toHaveText("1,960円");
  await expect(page.getByTestId("kpi-churn-value")).toHaveText("—");

  await page.getByTestId("goal").getByRole("button", { name: "目標を設定" }).click();
  await page.getByLabel("目標の MRR").fill("10000");
  await page.getByRole("dialog").getByRole("button", { name: "保存" }).click();
  await expect(page.getByTestId("goal-rate")).toHaveText("19.6%");

  await page.getByRole("button", { name: "表で表示" }).click();
  await expect(page.getByTestId("trend-table").getByRole("row")).toHaveCount(13);

  await page.getByTestId("attention-past_due").click();
  await expect(page).toHaveURL(new RegExp(`/console/t/${tenant.id}/subscriptions\\?status=past_due$`));
  await expect(page.getByRole("tab", { name: "支払い遅延" })).toHaveAttribute("aria-selected", "true");
});

test("FR-002: 閲覧者には目標の設定が無効で表示される", async ({ page, api, world, mail, wait }) => {
  const tenant = await world.createTenant();
  await signInAsNewStaff(page, api, mail, wait, tenant, "viewer", "dashview");
  await expect(page.getByTestId("goal").getByRole("button", { name: "目標を設定" })).toBeDisabled();
});

test("FR-008: 幅 375px でメニューを開いて画面を移ると、メニューが閉じ、ページは横にはみ出さない", async ({ page, api, world, mail, wait }) => {
  await page.setViewportSize({ width: 375, height: 812 });
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  await subscribe(api, world, tenant, plan.id);
  await signInAsNewStaff(page, api, mail, wait, tenant, "owner", "mobile");
  const noOverflow = async () => expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(375);

  await expect(page.getByTestId("kgi-mrr")).toBeVisible();
  await noOverflow();
  const nav = page.getByRole("link", { name: "商品・プラン" });
  await expect(nav).not.toBeInViewport();
  await page.getByTestId("nav-toggle").click();
  await expect(nav).toBeInViewport();
  await nav.click();
  await expect(page).toHaveURL(new RegExp(`/console/t/${tenant.id}/products$`));
  await expect(nav).not.toBeInViewport();
  await expect(page.getByRole("heading", { name: "商品・プラン" })).toBeVisible();
  await noOverflow();

  for (const path of ["subscriptions", "webhooks", "settings/staff", "settings/dunning"]) {
    await page.goto(`${CONSOLE}/console/t/${tenant.id}/${path}`);
    await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
    await noOverflow();
  }
  // ドロワーは画面の幅いっぱい
  await page.goto(`${CONSOLE}/console/t/${tenant.id}/settings/staff`);
  await page.getByRole("button", { name: "スタッフを招待" }).click();
  const box = await page.getByRole("dialog").boundingBox();
  expect(box?.width).toBeGreaterThanOrEqual(370);
  await noOverflow();
});
