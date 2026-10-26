// consumer-mypage FR-005〜FR-008・FR-010（ブラウザ）：解約の予約と取り消し、プラン変更の予約と取り消し、カードの登録と削除、退会、スマートフォンの幅。
import { test, expect } from "../../fixtures/index.ts";
import { consoleOf, createPlan } from "../../helpers/catalog.ts";
import { MYPAGE, expectNoOverflow, idToken, login, newConsumer, signUp, subscribeAs } from "../../helpers/mypage.ts";

test("FR-006・FR-007: 解約を予約して取り消し、同じ商品の別のプランへの変更を予約して取り消せる", async ({ page, api, world, mail, wait }) => {
  const tenant = await world.createTenant();
  const basic = await createPlan(api, tenant, { amount: 980 });
  // 同じ商品に上位のプランを足す
  const pro = await consoleOf(api, tenant)<{ id: string }>("POST", "/v1/console/plans", { product_id: basic.product_id, name: "プロ", billing_interval: "month", trial_days: 0, amount: 1980 });
  expect((await consoleOf(api, tenant)("POST", `/v1/console/plans/${pro.body.id}/publish`)).status).toBe(200);
  const c = newConsumer("ops");
  await signUp(page, mail, wait, c);
  await expect(page.getByText("契約はまだありません")).toBeVisible();
  const { subscription } = await subscribeAs(api, await idToken(page), tenant.id, basic.id);

  await page.goto(`${MYPAGE}/subscriptions/${subscription.id}`);
  await expect(page.getByText("980円／月")).toBeVisible();
  // プラン変更の予約と取り消し
  await page.getByRole("button", { name: "プランを選ぶ" }).click();
  await page.getByLabel("プロ（1,980円／月）").check();
  await expect(page.getByRole("dialog")).toContainText("1,980円／月 になります。今日の請求はありません");
  await page.getByRole("button", { name: "変更を予約する" }).click();
  await expect(page.getByTestId("plan-change-alert")).toContainText("「プロ」（1,980円／月）に変わります");
  await page.getByRole("button", { name: "変更を取り消す" }).click();
  await expect(page.getByTestId("plan-change-alert")).toHaveCount(0);
  // 解約の予約と取り消し
  await page.getByRole("button", { name: "この契約を解約する" }).click();
  await expect(page.getByRole("dialog")).toContainText("の更新で請求されずに解約されます");
  await page.getByRole("dialog").getByRole("button", { name: "解約を予約する" }).click();
  await expect(page.getByTestId("cancellation-alert")).toContainText("まで利用できます");
  await expect(page.getByRole("button", { name: "プランを選ぶ" })).toHaveCount(0); // 解約の予約中はプラン変更を出さない
  await page.goto(`${MYPAGE}/`);
  await expect(page.getByTestId("subscription-card")).toContainText("解約予定");
  await page.getByTestId("subscription-card").click();
  await page.getByRole("button", { name: "解約を取り消す" }).click();
  await expect(page.getByTestId("cancellation-alert")).toHaveCount(0);
  await expect(page.getByText("次回の更新日", { exact: true })).toBeVisible();
});

test("FR-008: カードを登録・削除でき、契約に使っているカードは削除できない", async ({ page, api, world, mail, wait }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const c = newConsumer("card");
  await signUp(page, mail, wait, c);
  await expect(page.getByText("契約はまだありません")).toBeVisible();
  await subscribeAs(api, await idToken(page), tenant.id, plan.id);

  await page.getByRole("link", { name: "支払方法" }).click();
  await expect(page.getByTestId("payment-method")).toHaveCount(1);
  await expect(page.getByTestId("payment-method")).toContainText("1 件の契約で使用中");
  await page.getByTestId("payment-method").getByRole("button", { name: "削除" }).click();
  await expect(page.getByTestId("pm-in-use")).toContainText("このカードは削除できません");

  await page.getByRole("button", { name: "カードを登録する" }).click();
  await page.getByLabel("カード番号").fill("1234");
  await page.getByLabel("有効期限（MM/YY）").fill("12/30");
  await page.getByRole("button", { name: "登録する" }).click();
  await expect(page.getByTestId("card-form").getByRole("alert")).toContainText("カード番号と有効期限");
  await page.getByLabel("カード番号").fill("4242 4242 4242 4242");
  await page.getByLabel("有効期限（MM/YY）").fill("12/30");
  await page.getByRole("button", { name: "登録する" }).click();
  await expect(page.getByTestId("payment-method")).toHaveCount(2);
  const unused = page.getByTestId("payment-method").filter({ hasText: "未使用" });
  await expect(unused).toContainText("•••• 4242（12/30）");
  await unused.getByRole("button", { name: "削除" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "削除する" }).click();
  await expect(page.getByTestId("payment-method")).toHaveCount(1);
});

test("FR-010: 有効な契約が残っていると退会できない。契約がなければ退会でき、同じアカウントではログインできなくなる", async ({ page, api, world, mail, wait }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const busy = newConsumer("wd-busy");
  await signUp(page, mail, wait, busy);
  await expect(page.getByText("契約はまだありません")).toBeVisible();
  await subscribeAs(api, await idToken(page), tenant.id, plan.id);
  await page.getByRole("link", { name: "アカウント" }).click();
  await expect(page.getByTestId("withdraw-blocked")).toContainText(tenant.name);
  await expect(page.getByRole("button", { name: "退会する" })).toHaveCount(0);
  await page.getByRole("button", { name: "ログアウト" }).click();
  await expect(page).toHaveURL(/notice=logout/);

  const free = newConsumer("wd-free");
  await signUp(page, mail, wait, free);
  await page.getByRole("link", { name: "アカウント" }).click();
  await expect(page.getByText(free.email)).toBeVisible();
  await page.getByRole("button", { name: "退会する" }).click();
  await page.getByRole("dialog").getByLabel("メールアドレス").fill("other@example.com");
  await page.getByRole("dialog").getByRole("button", { name: "退会する" }).click();
  await expect(page.getByText("登録しているメールアドレスを入力してください", { exact: true })).toBeVisible();
  await page.getByRole("dialog").getByLabel("メールアドレス").fill(free.email);
  await page.getByRole("dialog").getByRole("button", { name: "退会する" }).click();
  await expect(page).toHaveURL(/notice=withdrawn/);
  await expect(page.getByText("退会しました")).toBeVisible();
  // Cognito のアカウントも削除されている（ADR-M03）
  await login(page, free);
  await expect(page.getByRole("alert")).toContainText("メールアドレスまたはパスワードが正しくありません");
});

test("FR-013・NFR-007: パスワードを変更でき、幅 375px で一覧・詳細・支払方法が横にはみ出さない", async ({ page, api, world, mail, wait }) => {
  await page.setViewportSize({ width: 375, height: 812 });
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 1980 });
  const c = newConsumer("mobile");
  await signUp(page, mail, wait, c);
  await expect(page.getByText("契約はまだありません")).toBeVisible();
  const { subscription } = await subscribeAs(api, await idToken(page), tenant.id, plan.id);
  await page.reload();
  await expect(page.getByTestId("subscription-card")).toBeVisible();
  await expectNoOverflow(page);
  await page.getByTestId("subscription-card").click();
  await expect(page).toHaveURL(new RegExp(`/subscriptions/${subscription.id}$`));
  await expect(page.getByTestId("invoices")).toContainText("1,980円");
  await expectNoOverflow(page);
  await page.getByRole("link", { name: "支払方法" }).click();
  await expectNoOverflow(page);

  await page.getByRole("link", { name: "アカウント" }).click();
  await page.getByRole("button", { name: "パスワードを変更する" }).click();
  await page.getByRole("textbox", { name: "現在のパスワード" }).fill(c.password);
  await page.getByRole("textbox", { name: "新しいパスワード" }).fill(`${c.password}z2`);
  await page.getByRole("dialog").getByRole("button", { name: "変更する" }).click();
  await expect(page.getByText("パスワードを変更しました")).toBeVisible();
  await page.getByRole("button", { name: "ログアウト" }).click();
  await expect(page).toHaveURL(/notice=logout/);
  await login(page, { email: c.email, password: `${c.password}z2` });
  await expect(page.getByTestId("subscription-card")).toBeVisible();
});
