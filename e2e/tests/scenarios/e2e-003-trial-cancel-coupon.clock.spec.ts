// E2E-003 トライアル中の解約（クーポン付き）（orchestration.md §11b.3）：AC-ORC-002、P-ORC-4、AC-CAT-020。
import { test, expect } from "../../fixtures/index.ts";
import { createCoupon, createPlan } from "../../helpers/catalog.ts";
import { JOB, addDays, checkout, jst, startAtNext15, subscribe, untilStatus, type Consumer } from "../../helpers/orchestration.ts";

test("E2E-003: クーポン付きのトライアル申込はクーポンが即 confirmed、トライアル中に解約すると請求・試行なしで canceled、同じクーポンは使用済み", async ({ api, world, db, wait, clock }) => {
  const D = await startAtNext15(clock);
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980, trialDays: 14 });
  const coupon = await createCoupon(api, tenant, { value: 50, cycles: 1 });
  const { consumer, pm, subscription: sub, invoice } = await subscribe(api, world, tenant, plan.id, "ok", { coupon: coupon.code });
  expect(sub.status).toBe("trialing");
  expect(invoice).toBeNull();
  expect(await db.count("SELECT count(*) FROM catalog.coupon_redemptions WHERE subscription_id = $1 AND status = 'confirmed'", [sub.id])).toBe(1);

  await clock.setTo(jst(addDays(D, 5), "09:00"));
  expect((await api.call("PUT", `/v1/me/subscriptions/${sub.id}/scheduled-cancellation`, { token: consumer.token, body: {} })).status).toBe(200);
  const end = addDays(D, 14);
  await clock.setTo(jst(end, "00:30"));
  await clock.runJob(JOB.dispatch, { asOf: end, force: true });
  await untilStatus(api, wait, consumer, sub.id, "canceled");

  const again = await checkout(api, consumer as Consumer, tenant, plan.id, pm.id, { coupon: coupon.code });
  expect(again.status).toBe(422);
  expect(again.problem?.code).toBe("coupon_already_used");
  expect(await db.count("SELECT count(*) FROM billing.invoices WHERE subscription_id = $1", [sub.id])).toBe(0);
  expect(await db.count("SELECT count(*) FROM payments.payment_attempts WHERE customer_account_id = $1", [consumer.customerAccountId])).toBe(0);
  await wait.until(async () => (await db.count("SELECT count(*) FROM orchestration.billing_cycle_processes WHERE subscription_id = $1 AND outcome = 'ended'", [sub.id])) === 1, {
    message: "renewal プロセスが ended",
  });
});
