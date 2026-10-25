// E2E-007 支払い失敗 → リトライで回収（orchestration.md §11b.3）：AC-ORC-020・024・025、AC-BIL-013・035、FR-021。
import { test, expect } from "../../fixtures/index.ts";
import { createPlan } from "../../helpers/catalog.ts";
import { JOB, addDays, addMonths, expectInvariants, jst, meSubscription, setBehavior, startAtNext15, subscribe, untilStatus } from "../../helpers/orchestration.ts";

test("E2E-007: 更新の決済が 2 回失敗 → past_due、3 回目（+3 日 10:00）で回収して active。試行のキーは :1〜:3", async ({ api, world, db, wait, clock }) => {
  const D = await startAtNext15(clock);
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const { consumer, pm, subscription: sub } = await subscribe(api, world, tenant, plan.id);
  await setBehavior(api, pm.id, "decline", 2);
  const renewal = addMonths(D, 1);

  await clock.setTo(jst(renewal, "00:30"));
  await clock.runJob(JOB.dispatch, { asOf: renewal, force: true });
  const pd = await untilStatus(api, wait, consumer, sub.id, "past_due");
  expect(pd.entitled).toBe(true);
  const [inv] = await db.query<{ id: string }>("SELECT id FROM billing.invoices WHERE subscription_id = $1 AND kind = 'renewal'", [sub.id]);

  await clock.setTo(jst(addDays(renewal, 1), "10:00"));
  await clock.runJob(JOB.collectionDue, { force: true });
  await wait.until(async () => (await db.count("SELECT count(*) FROM payments.payment_attempts WHERE payable_id = $1 AND status = 'failed'", [inv!.id])) === 2, { message: "2 回目の失敗" });
  expect((await meSubscription(api, consumer, sub.id)).status).toBe("past_due");

  await clock.setTo(jst(addDays(renewal, 3), "10:00"));
  await clock.runJob(JOB.collectionDue, { force: true });
  const ok = await untilStatus(api, wait, consumer, sub.id, "active");
  expect(ok.next_renewal_date).toBe(addMonths(D, 2));
  const attempts = await db.query<{ idempotency_key: string; status: string }>("SELECT idempotency_key, status FROM payments.payment_attempts WHERE payable_id = $1 ORDER BY collection_seq", [inv!.id]);
  expect(attempts).toEqual([
    { idempotency_key: `invoice:${inv!.id}:1`, status: "failed" },
    { idempotency_key: `invoice:${inv!.id}:2`, status: "failed" },
    { idempotency_key: `invoice:${inv!.id}:3`, status: "succeeded" },
  ]);
  await wait.until(
    async () => {
      const ps = await db.query<{ kind: string; outcome: string | null }>("SELECT kind, outcome FROM orchestration.billing_cycle_processes WHERE subscription_id = $1 AND kind <> 'signup' ORDER BY created_at", [sub.id]);
      return JSON.stringify(ps) === JSON.stringify([{ kind: "renewal", outcome: "payment_failed" }, { kind: "retry", outcome: "payment_failed" }, { kind: "retry", outcome: "paid" }]);
    },
    { message: "renewal・retry・retry が閉じる" },
  );
  await expectInvariants(db, wait, tenant.id);
});
