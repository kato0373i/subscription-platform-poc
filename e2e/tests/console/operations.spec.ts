// business-console FR-005・FR-007・FR-008・FR-010・FR-015（ブラウザ）：商品・プランの作成と公開、契約の検索、スタッフの招待、
// ロールによる無効化、同時編集の検知。
import { randomUUID } from "node:crypto";
import { test, expect } from "../../fixtures/index.ts";
import { createPlan } from "../../helpers/catalog.ts";
import { CONSOLE, latestMail, signInAsNewStaff, uniqueEmail } from "../../helpers/console.ts";
import { subscribe } from "../../helpers/orchestration.ts";

test("FR-008: 商品とプランを作って公開すると、申込ページの URL が表示される", async ({ page, api, world, mail, wait }) => {
  const tenant = await world.createTenant();
  await signInAsNewStaff(page, api, mail, wait, tenant, "admin", "catalog");
  await page.getByRole("link", { name: "商品・プラン" }).click();
  await page.getByRole("button", { name: "商品を作成" }).click();
  await page.getByLabel("商品名").fill("プレミアム会員");
  await page.getByRole("button", { name: "作成", exact: true }).click();
  await expect(page.getByRole("heading", { name: "プレミアム会員" })).toBeVisible();

  await page.getByRole("button", { name: "プランを作成" }).click();
  await page.getByLabel("プラン名").fill("月額プラン");
  await page.getByLabel("料金（税込）").fill("1980");
  await page.getByRole("button", { name: "作成", exact: true }).click();
  await expect(page.getByRole("heading", { name: "月額プラン" })).toBeVisible();
  await expect(page.getByText("下書き", { exact: true }).first()).toBeVisible();
  await expect(page.getByText("1,980円").first()).toBeVisible();

  await page.getByRole("button", { name: "公開する" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "公開する" }).click();
  await expect(page.getByText("公開中", { exact: true }).first()).toBeVisible();
  const planId = page.url().split("/plans/")[1]!;
  await expect(page.getByText(`${CONSOLE}/checkout?plan_id=${planId}`)).toBeVisible();

  const plans = await api.call<{ data: { id: string; status: string; current_price: { amount: number } }[] }>("GET", "/v1/console/plans", { token: tenant.ownerToken, tenantId: tenant.id });
  expect(plans.body.data).toEqual([expect.objectContaining({ id: planId, status: "published", current_price: expect.objectContaining({ amount: 1980 }) })]);
});

test("FR-010: 契約をメールアドレスで検索し、詳細で契約者と請求の履歴を見られる", async ({ page, api, world, mail, wait }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const a = await subscribe(api, world, tenant, plan.id);
  await subscribe(api, world, tenant, plan.id);
  await signInAsNewStaff(page, api, mail, wait, tenant, "viewer", "subs");

  await expect(page.getByRole("row")).toHaveCount(3); // 見出し + 2 件
  await page.getByLabel("検索").fill(a.consumer.email);
  await expect(page.getByRole("row")).toHaveCount(2);
  await page.getByRole("row").nth(1).click();
  await expect(page).toHaveURL(new RegExp(`/subscriptions/${a.subscription.id}$`));
  await expect(page.getByText(a.consumer.email).first()).toBeVisible();
  await expect(page.getByText("支払い済み")).toBeVisible();
  await expect(page.getByText("980円").first()).toBeVisible();
});

test("FR-005: viewer には書き込みの操作が無効で表示され、理由がわかる", async ({ page, api, world, mail, wait }) => {
  const tenant = await world.createTenant();
  await signInAsNewStaff(page, api, mail, wait, tenant, "viewer", "viewer");
  await page.getByRole("link", { name: "商品・プラン" }).click();
  await expect(page.getByRole("button", { name: "商品を作成" })).toBeDisabled();
  await page.getByRole("link", { name: "スタッフ" }).click();
  await expect(page.getByRole("button", { name: "スタッフを招待" })).toBeDisabled();
  await page.getByRole("link", { name: "API キー" }).click();
  await expect(page.getByRole("button", { name: "API キーを発行" })).toBeDisabled();
});

test("FR-007: owner が画面からスタッフを招待すると、招待中として一覧に出て、招待メールが届く", async ({ page, api, world, mail, wait }) => {
  const tenant = await world.createTenant();
  await signInAsNewStaff(page, api, mail, wait, tenant, "owner", "staff");
  await page.getByRole("link", { name: "スタッフ" }).click();
  const email = uniqueEmail("ui");
  await page.getByRole("button", { name: "スタッフを招待" }).click();
  await page.getByLabel("メールアドレス").fill(email);
  await page.getByLabel("名前").fill("画面 招待");
  await page.getByLabel("管理者").check();
  await page.getByRole("button", { name: "招待", exact: true }).click();
  const row = page.getByRole("row").filter({ hasText: email });
  await expect(row).toContainText("招待中");
  await expect(row).toContainText("管理者");
  await latestMail(mail, wait, email, "招待");
});

test("FR-015: ほかのスタッフが先に更新したプランを保存すると、再読み込みを促すエラーになる", async ({ page, api, world, mail, wait }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { publish: false });
  await signInAsNewStaff(page, api, mail, wait, tenant, "admin", "conflict");
  await page.goto(`${CONSOLE}/console/t/${tenant.id}/plans/${plan.id}`);
  await page.getByRole("button", { name: "編集" }).click();
  // 画面を開いた後に、別のスタッフ（API）が更新する
  const other = await api.call("PATCH", `/v1/console/plans/${plan.id}`, {
    token: tenant.ownerToken, tenantId: tenant.id, idempotencyKey: randomUUID(), body: { name: "別の人の変更" },
  });
  expect(other.status).toBe(200);
  await page.getByLabel("プラン名").fill("自分の変更");
  await page.getByRole("button", { name: "保存" }).click();
  await expect(page.getByText("他のスタッフがこの内容を更新しました")).toBeVisible();
  const now = await api.call<{ name: string }>("GET", `/v1/console/plans/${plan.id}`, { token: tenant.ownerToken, tenantId: tenant.id });
  expect(now.body.name).toBe("別の人の変更");
});
