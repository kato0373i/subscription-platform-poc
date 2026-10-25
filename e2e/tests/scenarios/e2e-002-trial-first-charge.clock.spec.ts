// E2E-002 トライアルあり → 初回課金（orchestration.md §11b.3）：FR-013・FR-019・D-12。
import { test, expect } from "../../fixtures/index.ts";
import { createPlan } from "../../helpers/catalog.ts";
import { JOB, addDays, addMonths, expectInvariants, jst, startAtNext15, subscribe, untilStatus } from "../../helpers/orchestration.ts";

test("E2E-002: トライアル 14 日の申込は trialing・請求なし、トライアル終了日の更新で active・初回の請求 980 円", async ({ api, world, db, wait, clock }) => {
  const D = await startAtNext15(clock);
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980, trialDays: 14 });
  const { consumer, subscription: sub, invoice } = await subscribe(api, world, tenant, plan.id);
  expect(sub).toMatchObject({ status: "trialing", trial_applied: true, trial_end: addDays(D, 13) });
  expect(invoice).toBeNull();
  expect(await db.count("SELECT count(*) FROM billing.invoices WHERE subscription_id = $1", [sub.id])).toBe(0);
  expect(await db.count("SELECT count(*) FROM payments.payment_attempts WHERE customer_account_id = $1", [consumer.customerAccountId])).toBe(0);

  const first = addDays(D, 14);
  await clock.setTo(jst(first, "00:30"));
  await clock.runJob(JOB.dispatch, { asOf: first, force: true });
  const s = await untilStatus(api, wait, consumer, sub.id, "active");
  expect(s.current_period).toEqual({ start: first, end: addDays(addMonths(first, 1), -1) });
  const [inv] = await db.query<{ id: string; total_amount: number; status: string; kind: string }>("SELECT id, total_amount, status, kind FROM billing.invoices WHERE subscription_id = $1", [sub.id]);
  expect(inv).toMatchObject({ total_amount: 980, status: "paid", kind: "renewal" });
  expect(await db.count("SELECT count(*) FROM payments.payment_attempts WHERE idempotency_key = $1 AND status = 'succeeded'", [`invoice:${inv!.id}:1`])).toBe(1);
  await wait.until(async () => (await db.count("SELECT count(*) FROM orchestration.billing_cycle_processes WHERE subscription_id = $1 AND kind = 'renewal' AND outcome = 'paid'", [sub.id])) === 1, {
    message: "renewal プロセスが閉じる",
  });
  await expectInvariants(db, wait, tenant.id);
});
