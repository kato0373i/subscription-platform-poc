// E2E-005 プラン変更 → 更新時に切り替え（orchestration.md §11b.3）：FR-015・D-04、AC-SUB-015。
import { test, expect } from "../../fixtures/index.ts";
import { createPlan, type Plan } from "../../helpers/catalog.ts";
import { JOB, addDays, addMonths, expectInvariants, jst, startAtNext15, subscribe } from "../../helpers/orchestration.ts";

test("E2E-005: プラン変更の予約は次の更新日から。更新の請求は 1,980 円で新しい価格版", async ({ api, world, db, wait, clock }) => {
  const D = await startAtNext15(clock);
  const tenant = await world.createTenant();
  const m980 = await createPlan(api, tenant, { amount: 980 });
  // 同じ商品の 1,980 円のプラン
  const m1980 = await api.call<Plan>("POST", "/v1/console/plans", {
    token: tenant.ownerToken,
    tenantId: tenant.id,
    body: { product_id: m980.product_id, name: "M1980", billing_interval: "month", trial_days: 0, amount: 1980 },
  });
  expect(m1980.status, JSON.stringify(m1980.body)).toBe(201);
  expect((await api.call("POST", `/v1/console/plans/${m1980.body.id}/publish`, { token: tenant.ownerToken, tenantId: tenant.id })).status).toBe(200);
  const { consumer, subscription: sub } = await subscribe(api, world, tenant, m980.id);

  await clock.setTo(jst(addDays(D, 5), "09:00"));
  const chg = await api.call<{ plan_id: string; scheduled_plan_change: { plan_id: string; effective_date: string } }>("PUT", `/v1/me/subscriptions/${sub.id}/scheduled-plan-change`, {
    token: consumer.token,
    body: { plan_id: m1980.body.id },
  });
  expect(chg.status, JSON.stringify(chg.body)).toBe(200);
  const renewal = addMonths(D, 1);
  expect(chg.body.scheduled_plan_change.effective_date).toBe(renewal);
  expect(chg.body.plan_id).toBe(m980.id);

  await clock.setTo(jst(renewal, "00:30"));
  await clock.runJob(JOB.dispatch, { asOf: renewal, force: true });
  await wait.until(
    async () => {
      const s = await api.call<{ plan_id: string; scheduled_plan_change: unknown }>("GET", `/v1/me/subscriptions/${sub.id}`, { token: consumer.token });
      return s.body.plan_id === m1980.body.id && s.body.scheduled_plan_change === null;
    },
    { message: "プランが切り替わる" },
  );
  const [inv] = await db.query<{ id: string; total_amount: number; status: string }>("SELECT id, total_amount, status FROM billing.invoices WHERE subscription_id = $1 AND kind = 'renewal'", [sub.id]);
  await wait.until(async () => (await db.count("SELECT count(*) FROM billing.invoices WHERE id = $1 AND status = 'paid'", [inv!.id])) === 1, { message: "更新の請求が paid" });
  expect(inv!.total_amount).toBe(1980);
  expect(
    await db.count(
      "SELECT count(*) FROM billing.invoice_lines l JOIN catalog.price_versions v ON v.id = l.price_version_id WHERE l.invoice_id = $1 AND l.kind = 'plan' AND v.plan_id = $2",
      [inv!.id, m1980.body.id],
    ),
  ).toBe(1);
  await expectInvariants(db, wait, tenant.id);
});
