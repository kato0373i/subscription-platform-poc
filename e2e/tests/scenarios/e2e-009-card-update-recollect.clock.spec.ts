// E2E-009 支払い遅延中にカードを変更 → すぐに再決済（orchestration.md §11b.3）：AC-ORC-029、AC-BIL-019・020、FR-017・C-04・P-X-1。
import { test, expect } from "../../fixtures/index.ts";
import { createPlan } from "../../helpers/catalog.ts";
import { JOB, addDays, addMonths, expectInvariants, jst, meSubscription, setBehavior, startAtNext15, subscribe, untilStatus } from "../../helpers/orchestration.ts";

test("E2E-009: past_due でカードを変えると即時の再決済（collection_seq=2）で active。翌日の回収時刻にも試行は増えない", async ({ api, world, db, wait, clock }) => {
  const D = await startAtNext15(clock);
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const { consumer, pm: pm1, subscription: sub } = await subscribe(api, world, tenant, plan.id);
  await setBehavior(api, pm1.id, "decline", 99);
  const renewal = addMonths(D, 1);
  await clock.setTo(jst(renewal, "00:30"));
  await clock.runJob(JOB.dispatch, { asOf: renewal, force: true });
  await untilStatus(api, wait, consumer, sub.id, "past_due");

  await clock.setTo(jst(renewal, "12:00"));
  const pm2 = await world.createPaymentMethod(consumer, "ok");
  const chg = await api.call<{ recollection: string }>("PUT", `/v1/me/subscriptions/${sub.id}/payment-method`, { token: consumer.token, body: { payment_method_id: pm2.id } });
  expect(chg.status, JSON.stringify(chg.body)).toBe(200);
  expect(chg.body.recollection).toBe("requested");
  await untilStatus(api, wait, consumer, sub.id, "active");

  await clock.setTo(jst(addDays(renewal, 1), "10:00"));
  await clock.runJob(JOB.collectionDue, { force: true });
  expect((await meSubscription(api, consumer, sub.id)).status).toBe("active");
  const [inv] = await db.query<{ id: string }>("SELECT id FROM billing.invoices WHERE subscription_id = $1 AND kind = 'renewal'", [sub.id]);
  const attempts = await db.query<{ idempotency_key: string; payment_method_id: string; status: string }>(
    "SELECT idempotency_key, payment_method_id, status FROM payments.payment_attempts WHERE payable_id = $1 ORDER BY collection_seq",
    [inv!.id],
  );
  expect(attempts).toEqual([
    { idempotency_key: `invoice:${inv!.id}:1`, payment_method_id: pm1.id, status: "failed" },
    { idempotency_key: `invoice:${inv!.id}:2`, payment_method_id: pm2.id, status: "succeeded" },
  ]);
  expect(await db.count("SELECT count(*) FROM orchestration.billing_cycle_processes WHERE subscription_id = $1 AND kind = 'recovery' AND outcome = 'paid'", [sub.id])).toBe(1);
  await expectInvariants(db, wait, tenant.id);
});
