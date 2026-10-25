// orchestration.md §4.1（初回の決済の拒否）：AC-ORC-007、AC-CAT-024・030。
import { test, expect } from "../../fixtures/index.ts";
import { createCoupon, createPlan } from "../../helpers/catalog.ts";
import { checkout, expectInvariants, type Consumer } from "../../helpers/orchestration.ts";

test("AC-ORC-007: decline は 422 payment_declined（decline_category=generic）、契約 signup_failed・請求 void・クーポン released", async ({ api, world, db, wait }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const coupon = await createCoupon(api, tenant, { value: 10 });
  const consumer = (await world.createConsumer()) as Consumer;
  const pm = await world.createPaymentMethod(consumer, "decline");
  const res = await checkout(api, consumer, tenant, plan.id, pm.id, { coupon: coupon.code });
  expect(res.status, JSON.stringify(res.body)).toBe(422);
  expect(res.problem).toMatchObject({ code: "payment_declined", decline_category: "generic" });
  const [sub] = await db.query<{ id: string; status: string }>("SELECT id, status FROM subscription.subscriptions WHERE customer_account_id = $1", [consumer.customerAccountId]);
  expect(sub!.status).toBe("signup_failed");
  expect(await db.count("SELECT count(*) FROM billing.invoices WHERE subscription_id = $1 AND status = 'void' AND void_reason = 'signup_failed'", [sub!.id])).toBe(1);
  expect(await db.count("SELECT count(*) FROM catalog.coupon_redemptions WHERE subscription_id = $1 AND status = 'released'", [sub!.id])).toBe(1);
  expect(await db.count("SELECT count(*) FROM orchestration.billing_cycle_processes WHERE subscription_id = $1 AND outcome = 'signup_failed'", [sub!.id])).toBe(1);
  // 残高不足は insufficient_funds の分類
  const c2 = (await world.createConsumer()) as Consumer;
  const pm2 = await world.createPaymentMethod(c2, "insufficient");
  const r2 = await checkout(api, c2, tenant, plan.id, pm2.id);
  expect(r2.problem).toMatchObject({ code: "payment_declined", decline_category: "insufficient_funds" });
  await expectInvariants(db, wait, tenant.id);
  // sink に subscription.started が届かないことの確認は integration モジュールの実装後
});
