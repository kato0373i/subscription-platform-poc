// E2E-008 5 回のリトライがすべて失敗 → 自動解約（orchestration.md §11b.3）：AC-ORC-026、AC-BIL-016・036、FR-021・FR-006。
import { test, expect } from "../../fixtures/index.ts";
import { consoleOf, createPlan } from "../../helpers/catalog.ts";
import { JOB, addDays, addMonths, expectInvariants, jst, meSubscription, setBehavior, startAtNext15, subscribe, untilStatus } from "../../helpers/orchestration.ts";

test("E2E-008: 方針 [1,2,3,4,5] ですべて失敗すると請求 uncollectible、契約 canceled（reason=dunning）", async ({ api, world, db, wait, clock }) => {
  const D = await startAtNext15(clock);
  const tenant = await world.createTenant();
  expect((await consoleOf(api, tenant)("PUT", "/v1/console/dunning-policy", { retry_offsets_days: [1, 2, 3, 4, 5] })).status).toBe(200);
  const plan = await createPlan(api, tenant, { amount: 980 });
  const { consumer, pm, subscription: sub } = await subscribe(api, world, tenant, plan.id);
  await setBehavior(api, pm.id, "decline", 99);
  const renewal = addMonths(D, 1);
  await clock.setTo(jst(renewal, "00:30"));
  await clock.runJob(JOB.dispatch, { asOf: renewal, force: true });
  await untilStatus(api, wait, consumer, sub.id, "past_due");
  const [inv] = await db.query<{ id: string }>("SELECT id FROM billing.invoices WHERE subscription_id = $1 AND kind = 'renewal'", [sub.id]);
  for (let i = 1; i <= 5; i++) {
    await clock.setTo(jst(addDays(renewal, i), "10:00"));
    await clock.runJob(JOB.collectionDue, { force: true });
    await wait.until(async () => (await db.count("SELECT count(*) FROM payments.payment_attempts WHERE payable_id = $1 AND status = 'failed'", [inv!.id])) === i + 1, {
      message: `${i + 1} 回目の失敗`,
    });
  }
  await untilStatus(api, wait, consumer, sub.id, "canceled");
  expect((await meSubscription(api, consumer, sub.id)).entitled).toBe(false);
  expect(await db.count("SELECT count(*) FROM subscription.subscriptions WHERE id = $1 AND cancel_reason = 'dunning'", [sub.id])).toBe(1);
  const seqs = await db.query<{ collection_seq: number }>("SELECT collection_seq FROM payments.payment_attempts WHERE payable_id = $1 ORDER BY collection_seq", [inv!.id]);
  expect(seqs.map((s) => s.collection_seq)).toEqual([1, 2, 3, 4, 5, 6]);
  expect(await db.count("SELECT count(*) FROM billing.invoices WHERE id = $1 AND status = 'uncollectible' AND void_requested_at IS NULL", [inv!.id])).toBe(1);
  await expectInvariants(db, wait, tenant.id);
});
