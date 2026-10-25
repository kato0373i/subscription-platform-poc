// E2E-013 申込の二重送信（orchestration.md §11b.3）：AC-ORC-011・012。
import { test, expect } from "../../fixtures/index.ts";
import { createPlan } from "../../helpers/catalog.ts";
import { checkout, expectInvariants, type Consumer } from "../../helpers/orchestration.ts";

test("E2E-013: 同じキー・同じ本文の再送は同じ応答（Idempotent-Replayed）、同時送信でも契約・請求・成功した試行は 1 件", async ({ api, world, db, wait }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });

  // (a) 続けて 2 回
  const a = (await world.createConsumer()) as Consumer;
  const pmA = await world.createPaymentMethod(a, "ok");
  const keyA = crypto.randomUUID();
  const first = await checkout(api, a, tenant, plan.id, pmA.id, { key: keyA });
  const second = await checkout(api, a, tenant, plan.id, pmA.id, { key: keyA });
  expect(first.status).toBe(201);
  expect(second.status).toBe(201);
  expect(second.body).toEqual(first.body);
  expect(second.headers["idempotent-replayed"]).toBe("true");

  // (b) 同時に 2 件
  const b = (await world.createConsumer()) as Consumer;
  const pmB = await world.createPaymentMethod(b, "ok");
  const keyB = crypto.randomUUID();
  const both = await Promise.all([checkout(api, b, tenant, plan.id, pmB.id, { key: keyB }), checkout(api, b, tenant, plan.id, pmB.id, { key: keyB })]);
  const statuses = both.map((r) => r.status).sort();
  expect([[201, 201], [201, 409]]).toContainEqual(statuses);
  for (const r of both.filter((r) => r.status === 409)) expect(r.problem?.code).toBe("request_in_progress");

  for (const c of [a, b]) {
    expect(await db.count("SELECT count(*) FROM subscription.subscriptions WHERE customer_account_id = $1", [c.customerAccountId])).toBe(1);
    expect(await db.count("SELECT count(*) FROM billing.invoices WHERE customer_account_id = $1", [c.customerAccountId])).toBe(1);
    expect(await db.count("SELECT count(*) FROM payments.payment_attempts WHERE customer_account_id = $1 AND status = 'succeeded'", [c.customerAccountId])).toBe(1);
  }
  // 同じキーで本文が違えば 422
  const other = await api.call("POST", "/v1/me/subscriptions", { token: a.token, idempotencyKey: keyA, body: { tenant_id: tenant.id, plan_id: plan.id, payment_method_id: pmA.id, expect_trial: true } });
  expect(other.status).toBe(422);
  expect(other.problem?.code).toBe("idempotency_key_reused");
  await expectInvariants(db, wait, tenant.id);
});
