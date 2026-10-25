// E2E-011 結果がわからない決済 → 照合で確定（orchestration.md §11b.3）：AC-ORC-008・009、AC-PAY-017。
import { test, expect } from "../../fixtures/index.ts";
import { createPlan } from "../../helpers/catalog.ts";
import { checkout, expectInvariants, untilStatus, type Consumer } from "../../helpers/orchestration.ts";

test("E2E-011: timeout の申込は 202（pending_initial）、照合で成功が確定すると同じキーの再送は 201 active", async ({ api, world, db, wait }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const consumer = (await world.createConsumer()) as Consumer;
  const pm = await world.createPaymentMethod(consumer, "timeout");
  const key = crypto.randomUUID();

  for (let i = 0; i < 2; i++) {
    const res = await checkout(api, consumer, tenant, plan.id, pm.id, { key });
    expect(res.status, JSON.stringify(res.body)).toBe(202);
    expect(res.headers.location).toMatch(/^\/v1\/me\/subscriptions\/[0-9a-f-]{36}$/);
    expect(res.body).toMatchObject({ subscription: { status: "pending_initial" }, processing: true });
  }
  const [row] = await db.query<{ id: string; status: string }>("SELECT id, status FROM subscription.subscriptions WHERE customer_account_id = $1", [consumer.customerAccountId]);
  expect(row!.status).toBe("pending_initial");
  const sub = await api.call<{ entitled: boolean }>("GET", `/v1/me/subscriptions/${row!.id}`, { token: consumer.token });
  expect(sub.body.entitled).toBe(false);
  expect(await db.count("SELECT count(*) FROM payments.payment_attempts WHERE tenant_id = $1 AND status = 'unknown'", [tenant.id])).toBe(1);
  expect(await db.count("SELECT count(*) FROM orchestration.billing_cycle_processes WHERE subscription_id = $1 AND state = 'awaiting_outcome'", [row!.id])).toBe(1);
  expect(await db.count("SELECT count(*) FROM orchestration.checkout_sagas WHERE idempotency_key = $1 AND status = 'pending_outcome'", [key])).toBe(1);
  expect(await db.count("SELECT count(*) FROM platform.idempotency_keys WHERE key = $1 AND status = 'completed'", [key])).toBe(0);

  // 照合（結果不明の試行は 30 秒以上経ったものだけを照会する）
  await wait.until(
    async () => {
      const run = await api.dev.runJob("payments-resolve-unknown", { force: true, wait_seconds: 60 });
      expect(run.status).toBe(200);
      return (await db.count("SELECT count(*) FROM payments.payment_attempts WHERE tenant_id = $1 AND status = 'succeeded' AND resolution = 'reconcile'", [tenant.id])) === 1;
    },
    { timeout: 90_000, interval: 5_000, message: "照合で成功が確定する" },
  );
  await untilStatus(api, wait, consumer, row!.id, "active");
  const again = await checkout(api, consumer, tenant, plan.id, pm.id, { key });
  expect(again.status, JSON.stringify(again.body)).toBe(201);
  expect(again.body.subscription).toMatchObject({ id: row!.id, status: "active" });
  expect(await db.count("SELECT count(*) FROM payments.stub_charges c JOIN payments.payment_attempts a ON a.provider_order_ref = c.order_ref WHERE a.tenant_id = $1", [tenant.id])).toBe(1);
  expect(await db.count("SELECT count(*) FROM orchestration.checkout_sagas WHERE idempotency_key = $1 AND status = 'completed'", [key])).toBe(1);
  await expectInvariants(db, wait, tenant.id);
});
