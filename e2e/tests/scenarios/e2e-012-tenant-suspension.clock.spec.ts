// E2E-012 事業者の停止 → 一括解約（orchestration.md §11b.3）：AC-ORC-005・034・035、FR-026・D-01、AC-SUB-029。
import { test, expect } from "../../fixtures/index.ts";
import { createPlan } from "../../helpers/catalog.ts";
import { JOB, addDays, addMonths, checkout, expectInvariants, jst, setBehavior, startAtNext15, subscribe, untilStatus, type Consumer } from "../../helpers/orchestration.ts";

type BulkJob = { status: string; sweep_round: number; counts: { scheduled_at_period_end: number; canceled_immediately: number } };

test("E2E-012: 停止すると past_due は即時解約、active・trialing は期間終了で解約。停止後の申込は 422 tenant_not_active", async ({ api, world, db, wait, clock, tokens }) => {
  const D = await startAtNext15(clock);
  const tenant = await world.createTenant();
  const m980 = await createPlan(api, tenant, { amount: 980 });
  const tr14 = await createPlan(api, tenant, { amount: 980, trialDays: 14 });
  const x = await subscribe(api, world, tenant, m980.id);
  const y = await subscribe(api, world, tenant, m980.id);
  await setBehavior(api, y.pm.id, "decline", 99);
  const renewal = addMonths(D, 1);
  await clock.setTo(jst(addDays(renewal, -10), "09:00"));
  const z = await subscribe(api, world, tenant, tr14.id);
  await clock.setTo(jst(renewal, "00:30"));
  await clock.runJob(JOB.dispatch, { asOf: renewal, force: true });
  await untilStatus(api, wait, y.consumer, y.subscription.id, "past_due");
  await wait.until(async () => (await db.count("SELECT count(*) FROM billing.invoices WHERE subscription_id = $1 AND kind = 'renewal' AND status = 'paid'", [x.subscription.id])) === 1, {
    message: "X の更新",
  });

  const op = await tokens.operator();
  expect((await api.call("POST", `/v1/ops/tenants/${tenant.id}/suspend`, { token: op, body: { reason: "E2E-012" } })).status).toBe(200);
  const job = await wait.until(
    async () => {
      await clock.runJob(JOB.bulk, { force: true });
      const r = await api.call<{ data: BulkJob[] }>("GET", "/v1/ops/bulk-cancellation-jobs", { token: op, query: { tenant_id: tenant.id } });
      return r.body.data[0]?.status === "completed" ? r.body.data[0] : undefined;
    },
    { message: "一括解約が完了する" },
  );
  expect(job).toMatchObject({ sweep_round: 2, counts: { scheduled_at_period_end: 2, canceled_immediately: 1 } });
  await untilStatus(api, wait, y.consumer, y.subscription.id, "canceled");
  await wait.until(async () => (await db.count("SELECT count(*) FROM billing.invoices WHERE subscription_id = $1 AND status = 'void'", [y.subscription.id])) === 1, { message: "Y の請求が void" });

  const newcomer = (await world.createConsumer()) as Consumer;
  const pm = await world.createPaymentMethod(newcomer, "ok");
  const rejected = await checkout(api, newcomer, tenant, m980.id, pm.id);
  expect(rejected.status).toBe(422);
  expect(rejected.problem?.code).toBe("tenant_not_active");

  // Z のトライアル終了日・X の期間終了の翌日に解約される（新しい請求はない）
  const zEnd = addDays(addDays(renewal, -10), 14);
  await clock.setTo(jst(zEnd, "00:30"));
  await clock.runJob(JOB.dispatch, { asOf: zEnd, force: true });
  await untilStatus(api, wait, z.consumer, z.subscription.id, "canceled");
  const xEnd = addMonths(D, 2);
  await clock.setTo(jst(xEnd, "00:30"));
  await clock.runJob(JOB.dispatch, { asOf: xEnd, force: true });
  await untilStatus(api, wait, x.consumer, x.subscription.id, "canceled");
  for (const s of [x, z]) {
    expect(await db.count("SELECT count(*) FROM subscription.subscriptions WHERE id = $1 AND cancel_reason = 'tenant_suspended'", [s.subscription.id])).toBe(1);
  }
  expect(await db.count("SELECT count(*) FROM billing.invoices WHERE subscription_id = $1", [x.subscription.id])).toBe(2);
  expect(await db.count("SELECT count(*) FROM billing.invoices WHERE subscription_id = $1", [z.subscription.id])).toBe(0);
  expect(await db.count("SELECT count(*) FROM orchestration.bulk_cancellation_jobs WHERE tenant_id = $1 AND sweep_round = 2", [tenant.id])).toBe(1);
  await expectInvariants(db, wait, tenant.id);
});
