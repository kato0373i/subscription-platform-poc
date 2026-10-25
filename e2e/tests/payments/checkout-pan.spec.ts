// 申込ページでカード番号が自社に届かないこと（payments.md §11.3）：AC-PAY-007、NFR-007。
// 拒否のカードもトークン化までは同じ経路を通るので、3 枚で同じ手順を流す。
import { test, expect } from "../../fixtures/index.ts";
import { createPlan } from "../../helpers/catalog.ts";
import {
  STUB_CARDS, expectPanNotInDb, expectPanNotInLogs, fillCardAndSubmit, findPan, openCheckout, recordRequests, settled,
} from "../../helpers/checkout.ts";
import { subscribe } from "../../helpers/orchestration.ts";

const cases = [
  { scenario: "ok", pan: STUB_CARDS.ok, status: 201 },
  { scenario: "decline", pan: STUB_CARDS.decline, status: 422 },
  { scenario: "insufficient", pan: STUB_CARDS.insufficient, status: 422 },
] as const;

for (const c of cases) {
  test(`AC-PAY-007（${c.scenario}）: カード番号は自社への要求・ブラウザの保存領域・DB・ログのどこにも現れない`, async ({ page, api, world, db, baseURL }) => {
    const started = new Date();
    const tenant = await world.createTenant();
    const plan = await createPlan(api, tenant, { amount: 980 });
    const consumer = await world.createConsumer();

    // 1. 記録の開始
    const reqs = recordRequests(page);
    // 2. 申込ページを開く：入力欄に name がなく、<form> の外にある
    await openCheckout(page, `plan_id=${plan.id}`, consumer.token);
    const number = page.locator('input[autocomplete="cc-number"]');
    expect(await number.getAttribute("name")).toBeNull();
    expect(await number.evaluate((el) => el.closest("form") === null)).toBe(true);
    expect(await page.locator('input[autocomplete="cc-exp"]').evaluate((el) => el.closest("form") === null && !el.hasAttribute("name"))).toBe(true);
    await expect(page.locator("#plan-price")).toContainText("980円");

    // 3〜4. 入力と申込
    const subRes = page.waitForResponse((r) => r.url().endsWith("/v1/me/subscriptions") && r.request().method() === "POST");
    await fillCardAndSubmit(page, c.pan);
    expect((await subRes).status()).toBe(c.status);
    await expect(page.locator("#result")).toHaveAttribute("data-status", c.status === 201 ? "succeeded" : "error");

    // 5. 入力欄は空
    await expect(number).toHaveValue("");

    // 6〜8. 記録したすべての要求にカード番号がない・トークンは送っている・自社以外への要求がない
    await settled(reqs);
    expect(reqs.length).toBeGreaterThanOrEqual(5);
    for (const r of reqs) {
      const text = [r.url, ...Object.values(r.headers), r.body].join("\n");
      expect(findPan(text, c.pan), `${r.method} ${r.url}`).toEqual([]);
    }
    const pm = reqs.find((r) => r.method === "POST" && r.url.endsWith("/v1/me/payment-methods"));
    expect(pm, "支払方法の登録の要求が記録されている").toBeTruthy();
    const body = JSON.parse(pm!.body) as { token: string };
    expect(body.token).toMatch(new RegExp(`^tok_stub_${c.scenario}_${c.pan.replace(/\D/g, "").slice(-4)}_1228_[a-z0-9]{8}$`));
    expect(Object.keys(body).sort()).toEqual(["method_type", "provider", "token"]);
    const origin = new URL(baseURL!).origin;
    expect(reqs.filter((r) => r.origin !== origin).map((r) => r.url)).toEqual([]);

    // 9. ブラウザの保存領域
    const storage = await page.evaluate(() => JSON.stringify({ l: { ...localStorage }, s: { ...sessionStorage }, c: document.cookie }));
    expect(findPan(storage, c.pan)).toEqual([]);

    // 10・11. DB とログ
    await expectPanNotInDb(db, c.pan);
    await expectPanNotInLogs(c.pan, started);
  });
}

test("申込ページはトークンがなければ申し込めず、公開できないプランは「現在お申し込みいただけません」", async ({ page, api, world }) => {
  await page.goto(`/checkout?plan_id=${crypto.randomUUID()}`);
  await expect(page.locator("#result")).toHaveAttribute("data-status", "error");
  await expect(page.locator("#submit")).toBeDisabled();

  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const consumer = await world.createConsumer();
  const res = await api.call("POST", `/v1/console/plans/${plan.id}/unpublish`, { token: tenant.ownerToken, tenantId: tenant.id });
  expect(res.status, JSON.stringify(res.body)).toBe(200);
  await page.goto(`/checkout?plan_id=${plan.id}#token=${encodeURIComponent(consumer.token)}`);
  await expect(page.locator("#result")).toHaveText("現在お申し込みいただけません。");
  // フラグメントのトークンはアドレスバーから消える
  expect(page.url()).not.toContain("#token=");
  // 申込ページの応答は外部のスクリプト・送信先を許さない
  const page2 = await page.request.get("/checkout");
  expect(page2.headers()["content-security-policy"]).toContain("connect-src 'self'");
});

test("支払方法の変更（?subscription=。支払い遅延のメールのリンク）：新しいカードをトークン化して契約の支払方法を差し替える", async ({ page, api, world, db }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const { consumer, subscription, pm } = await subscribe(api, world, tenant, plan.id);
  await openCheckout(page, `subscription=${subscription.id}`, consumer.token);
  await expect(page.locator("#title")).toHaveText("お支払い方法の変更");
  const put = page.waitForResponse((r) => r.url().endsWith(`/v1/me/subscriptions/${subscription.id}/payment-method`));
  await fillCardAndSubmit(page, STUB_CARDS.ok, "11/30");
  expect((await put).status()).toBe(200);
  await expect(page.locator("#result")).toHaveText("お支払い方法を変更しました。");
  const [row] = await db.query<{ payment_method_id: string }>("SELECT payment_method_id FROM subscription.subscriptions WHERE id = $1", [subscription.id]);
  expect(row!.payment_method_id).not.toBe(pm.id);
  expect(await db.count("SELECT count(*) FROM payments.payment_methods WHERE id = $1 AND (display->>'exp_month')::int = 11", [row!.payment_method_id])).toBe(1);
});
