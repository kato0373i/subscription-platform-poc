// consumer-mypage FR-009・FR-012（ブラウザ・clock）：支払い遅延 → 支払い失敗のメールのリンク → ログイン → カードの変更 → 即時の再決済。
import { test, expect } from "../../fixtures/index.ts";
import { createPlan } from "../../helpers/catalog.ts";
import { JOB, addMonths, jst, setBehavior, startAtNext15 } from "../../helpers/orchestration.ts";
import { MYPAGE, idToken, login, newConsumer, signUp, subscribeAs } from "../../helpers/mypage.ts";

test("FR-009・FR-012: 支払い失敗のメールのリンクからログインしてカードを変えると、再決済に失敗したら案内され、別のカードで回復する", async ({ page, api, world, mail, wait, clock, db }) => {
  const D = await startAtNext15(clock);
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const c = newConsumer("recover");
  await signUp(page, mail, wait, c);
  await expect(page.getByText("契約はまだありません")).toBeVisible();
  const { pmId, subscription } = await subscribeAs(api, await idToken(page), tenant.id, plan.id);
  await page.getByRole("link", { name: "アカウント" }).click();
  await page.getByRole("button", { name: "ログアウト" }).click();
  await expect(page).toHaveURL(/notice=logout/);

  // 更新の決済が失敗して支払い遅延になり、支払い失敗のメールにマイページのリンクが載る
  await setBehavior(api, pmId, "decline", 99);
  const renewal = addMonths(D, 1);
  await clock.setTo(jst(renewal, "00:30"));
  await clock.runJob(JOB.dispatch, { asOf: renewal, force: true });
  const sent = await wait.until(
    async () => (await db.query<{ variables: Record<string, string> }>("SELECT variables FROM integration.email_notifications WHERE subscription_id = $1 AND template_key = 'payment_failed' AND status = 'sent'", [subscription.id]))[0],
    { message: "支払い失敗のメール" },
  );
  const link = String(sent.variables.payment_method_update_url);
  expect(link).toBe(`${MYPAGE}/subscriptions/${subscription.id}/payment-method`);

  // リンクを開く → ログイン → 支払方法の変更の画面に戻る
  await page.goto(link);
  await expect(page).toHaveURL(/\/mypage\/login\?return=/);
  await page.getByLabel("メールアドレス").fill(c.email);
  await page.getByRole("textbox", { name: "パスワード" }).fill(c.password);
  await page.getByRole("button", { name: "ログイン" }).click();
  await expect(page).toHaveURL(link);
  await expect(page.getByText("未払いの請求があります")).toBeVisible();

  // 拒否されるカードでは回復しない
  await page.getByLabel("カード番号").fill("4000000000000002");
  await page.getByLabel("有効期限（MM/YY）").fill("12/30");
  await page.getByRole("button", { name: "登録して支払う" }).click();
  await expect(page.getByTestId("recollection-result")).toHaveAttribute("data-result", "failed", { timeout: 30_000 });
  await expect(page.getByText("お支払いができませんでした")).toBeVisible();

  // 有効なカードで回復する
  await page.getByRole("button", { name: "新しいカードを登録する" }).click();
  await page.getByLabel("カード番号").fill("4242424242424242");
  await page.getByLabel("有効期限（MM/YY）").fill("12/31");
  await page.getByRole("button", { name: "登録して支払う" }).click();
  await expect(page.getByTestId("recollection-result")).toHaveAttribute("data-result", "succeeded", { timeout: 30_000 });
  await page.getByRole("link", { name: "契約に戻る" }).click();
  await expect(page.getByText("利用中")).toBeVisible();
  await expect(page.getByTestId("invoices")).toContainText("お支払い済み");
  void login;
});
