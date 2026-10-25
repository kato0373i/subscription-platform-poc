// orchestration.md §9.2（0 円の初回請求）：AC-ORC-015、AC-BIL-004、FR-005。
import { test, expect } from "../../fixtures/index.ts";
import { createCoupon, createPlan } from "../../helpers/catalog.ts";
import { checkout, type Consumer } from "../../helpers/orchestration.ts";

test("AC-ORC-015: 100% 引きのクーポンは 201、請求 total=0 で paid、試行 0 件", async ({ api, world, db }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const coupon = await createCoupon(api, tenant, { value: 100 });
  const consumer = (await world.createConsumer()) as Consumer;
  const pm = await world.createPaymentMethod(consumer, "ok");
  const res = await checkout(api, consumer, tenant, plan.id, pm.id, { coupon: coupon.code });
  expect(res.status, JSON.stringify(res.body)).toBe(201);
  expect(res.body.subscription.status).toBe("active");
  expect(res.body.invoice).toMatchObject({ total: 0, tax: 0, status: "paid" });
  expect(await db.count("SELECT count(*) FROM payments.payment_attempts WHERE customer_account_id = $1", [consumer.customerAccountId])).toBe(0);
  expect(await db.count("SELECT count(*) FROM billing.invoices WHERE id = $1 AND paid_via = 'zero_amount'", [res.body.invoice!.id])).toBe(1);
  expect(await db.count("SELECT count(*) FROM catalog.coupon_redemptions WHERE subscription_id = $1 AND status = 'confirmed'", [res.body.subscription.id])).toBe(1);
});
