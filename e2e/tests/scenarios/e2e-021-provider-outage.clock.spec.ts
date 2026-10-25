// E2E-021 決済代行の障害（申込・更新）（orchestration.md §11b.3）：AC-ORC-013・023、AC-BIL-037、P-PAY-4。
import { test, expect } from "../../fixtures/index.ts";
import { createCoupon, createPlan } from "../../helpers/catalog.ts";
import { JOB, addMonths, checkout, expectInvariants, jst, meSubscription, setBehavior, startAtNext15, subscribe, type Consumer } from "../../helpers/orchestration.ts";

test("E2E-021: 申込は 503 で補償済み・同じキーの再送で新しい契約が 201。更新は段数を消費せず、リース切れの再送で同じキー :1 で回収", async ({ api, world, db, wait, clock }) => {
  const D = await startAtNext15(clock);
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const coupon = await createCoupon(api, tenant, { value: 50 });
  // (b) の契約は先に作っておく
  const other = await subscribe(api, world, tenant, plan.id);

  // (a) 申込
  const consumer = (await world.createConsumer()) as Consumer;
  const pm = await world.createPaymentMethod(consumer, "ok");
  await setBehavior(api, pm.id, "provider_down", 1);
  const key = crypto.randomUUID();
  const down = await checkout(api, consumer, tenant, plan.id, pm.id, { key, coupon: coupon.code });
  expect(down.status, JSON.stringify(down.body)).toBe(503);
  expect(down.problem?.code).toBe("provider_unavailable");
  expect(down.headers["retry-after"]).toBeDefined();
  const [failed] = await db.query<{ id: string; status: string }>("SELECT id, status FROM subscription.subscriptions WHERE customer_account_id = $1", [consumer.customerAccountId]);
  expect(failed!.status).toBe("signup_failed");
  expect(await db.count("SELECT count(*) FROM billing.invoices WHERE subscription_id = $1 AND status = 'void'", [failed!.id])).toBe(1);
  expect(await db.count("SELECT count(*) FROM catalog.coupon_redemptions WHERE subscription_id = $1 AND status = 'released'", [failed!.id])).toBe(1);
  expect(await db.count("SELECT count(*) FROM payments.payment_attempts WHERE customer_account_id = $1", [consumer.customerAccountId])).toBe(0);
  expect(await db.count("SELECT count(*) FROM orchestration.checkout_sagas WHERE idempotency_key = $1 AND status = 'aborted'", [key])).toBe(1);
  const retry = await checkout(api, consumer, tenant, plan.id, pm.id, { key, coupon: coupon.code });
  expect(retry.status, JSON.stringify(retry.body)).toBe(201);
  expect(retry.body.subscription.status).toBe("active");
  expect(retry.body.subscription.id).not.toBe(failed!.id);
  expect(await db.count("SELECT count(*) FROM catalog.coupon_redemptions WHERE subscription_id = $1 AND status = 'confirmed'", [retry.body.subscription.id])).toBe(1);

  // (b) 更新
  const renewal = addMonths(D, 1);
  await clock.setTo(jst(renewal, "00:30"));
  await setBehavior(api, other.pm.id, "provider_down", 1);
  await clock.runJob(JOB.dispatch, { asOf: renewal, force: true });
  const proc = await wait.until(
    async () => {
      const [p] = await db.query<{ state: string; attempt_status: string | null }>(
        "SELECT state, attempt_status FROM orchestration.billing_cycle_processes WHERE subscription_id = $1 AND kind = 'renewal'",
        [other.subscription.id],
      );
      return p?.attempt_status === "provider_unavailable" ? p : undefined;
    },
    { message: "更新の回収が provider_unavailable" },
  );
  expect(proc.state).toBe("collecting");
  expect((await meSubscription(api, other.consumer, other.subscription.id)).status).toBe("active");
  const [inv] = await db.query<{ id: string; dunning_stage: number }>("SELECT id, dunning_stage FROM billing.invoices WHERE subscription_id = $1 AND kind = 'renewal'", [other.subscription.id]);
  expect(inv!.dunning_stage).toBe(0);
  expect(await db.count("SELECT count(*) FROM payments.payment_attempts WHERE payable_id = $1", [inv!.id])).toBe(0);

  await clock.advance("2m");
  await clock.runJob(JOB.collectionDue, { force: true });
  await wait.until(async () => (await meSubscription(api, other.consumer, other.subscription.id)).next_renewal_date === addMonths(D, 2), { message: "更新が確定する" });
  const attempts = await db.query<{ idempotency_key: string; status: string }>("SELECT idempotency_key, status FROM payments.payment_attempts WHERE payable_id = $1", [inv!.id]);
  expect(attempts).toEqual([{ idempotency_key: `invoice:${inv!.id}:1`, status: "succeeded" }]);
  await expectInvariants(db, wait, tenant.id);
});
