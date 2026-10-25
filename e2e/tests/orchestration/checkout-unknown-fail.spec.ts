// orchestration.md §4.1「結果不明の後の確定」：AC-ORC-010、P-ORC-2。
import { test, expect } from "../../fixtures/index.ts";
import { createCoupon, createPlan } from "../../helpers/catalog.ts";
import { checkout, untilStatus, type Consumer } from "../../helpers/orchestration.ts";

test("AC-ORC-010: timeout_fail で 202 → 照合で失敗が確定すると signup_failed・請求 void・クーポン released、同じキーの再送は 422 payment_declined", async ({ api, world, db, wait }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const coupon = await createCoupon(api, tenant, { value: 10 });
  const consumer = (await world.createConsumer()) as Consumer;
  const pm = await world.createPaymentMethod(consumer, "timeout_fail");
  const key = crypto.randomUUID();
  const res = await checkout(api, consumer, tenant, plan.id, pm.id, { key, coupon: coupon.code });
  expect(res.status, JSON.stringify(res.body)).toBe(202);
  const id = res.body.subscription.id;
  await wait.until(
    async () => {
      await api.dev.runJob("payments-resolve-unknown", { force: true, wait_seconds: 60 });
      return (await db.count("SELECT count(*) FROM payments.payment_attempts WHERE tenant_id = $1 AND status = 'failed'", [tenant.id])) === 1;
    },
    { timeout: 90_000, interval: 5_000, message: "照合で失敗が確定する" },
  );
  await untilStatus(api, wait, consumer, id, "signup_failed");
  expect(await db.count("SELECT count(*) FROM billing.invoices WHERE subscription_id = $1 AND status = 'void'", [id])).toBe(1);
  expect(await db.count("SELECT count(*) FROM catalog.coupon_redemptions WHERE subscription_id = $1 AND status = 'released'", [id])).toBe(1);
  const again = await checkout(api, consumer, tenant, plan.id, pm.id, { key, coupon: coupon.code });
  expect(again.status).toBe(422);
  expect(again.problem?.code).toBe("payment_declined");
});
