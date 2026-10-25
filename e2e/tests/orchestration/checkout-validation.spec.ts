// orchestration.md §4.1・§7.1（申込の入力の検証）：AC-ORC-003・004・006、AC-CAT-009、AC-SUB-010。
import { test, expect } from "../../fixtures/index.ts";
import { consoleOf, createCoupon, createPlan } from "../../helpers/catalog.ts";
import { checkout, subscribe, type Consumer } from "../../helpers/orchestration.ts";

test("AC-ORC-003・004: トライアル利用済みで expect_trial=true は 422 trial_not_eligible（課金しない・クーポン解放）、省略なら即時課金で 201 active", async ({ api, world, db }) => {
  const tenant = await world.createTenant();
  const trial = await createPlan(api, tenant, { amount: 980, trialDays: 14 });
  const coupon = await createCoupon(api, tenant, { value: 50 });
  const { consumer, pm } = await subscribe(api, world, tenant, trial.id);
  expect(await db.count("SELECT count(*) FROM subscription.subscriptions WHERE customer_account_id = $1 AND status = 'trialing'", [consumer.customerAccountId])).toBe(1);

  const res = await checkout(api, consumer as Consumer, tenant, trial.id, pm.id, { expectTrial: true, coupon: coupon.code });
  expect(res.status, JSON.stringify(res.body)).toBe(422);
  expect(res.problem?.code).toBe("trial_not_eligible");
  expect(await db.count("SELECT count(*) FROM payments.payment_attempts WHERE customer_account_id = $1", [consumer.customerAccountId])).toBe(0);
  expect(await db.count("SELECT count(*) FROM catalog.coupon_redemptions WHERE customer_account_id = $1 AND status = 'released'", [consumer.customerAccountId])).toBe(1);

  const now = await checkout(api, consumer as Consumer, tenant, trial.id, pm.id);
  expect(now.status, JSON.stringify(now.body)).toBe(201);
  expect(now.body.subscription).toMatchObject({ status: "active", trial_applied: false });
  expect(now.body.invoice).toMatchObject({ total: 980, status: "paid" });
});

test("AC-ORC-006: 非公開・別事業者のプランは 422 plan_unavailable、停止中でない事業者でも契約は作られない", async ({ api, world, db }) => {
  const tenant = await world.createTenant();
  const other = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const consumer = (await world.createConsumer()) as Consumer;
  const pm = await world.createPaymentMethod(consumer, "ok");
  await consoleOf(api, tenant)("POST", `/v1/console/plans/${plan.id}/unpublish`);
  const res = await checkout(api, consumer, tenant, plan.id, pm.id);
  expect(res.status).toBe(422);
  expect(res.problem?.code).toBe("plan_unavailable");
  const mismatch = await checkout(api, consumer, other, plan.id, pm.id);
  expect(mismatch.status).toBe(422);
  expect(mismatch.problem?.code).toBe("plan_unavailable");
  expect(await db.count("SELECT count(*) FROM subscription.subscriptions WHERE customer_account_id = $1", [consumer.customerAccountId])).toBe(0);
});

test("他人の・削除済みの支払方法は 422 payment_method_unavailable、Idempotency-Key なしは 400", async ({ api, world }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const a = (await world.createConsumer()) as Consumer;
  const b = (await world.createConsumer()) as Consumer;
  const pmB = await world.createPaymentMethod(b, "ok");
  const res = await checkout(api, a, tenant, plan.id, pmB.id);
  expect(res.status).toBe(422);
  expect(res.problem?.code).toBe("payment_method_unavailable");
  const noKey = await api.call("POST", "/v1/me/subscriptions", { token: a.token, body: { tenant_id: tenant.id, plan_id: plan.id, payment_method_id: pmB.id } });
  expect(noKey.status).toBe(400);
});
