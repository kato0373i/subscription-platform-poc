// E2E-006 解約予約 → 期間終了で解約済み（orchestration.md §11b.3）：AC-ORC-018、FR-016。
import { test, expect } from "../../fixtures/index.ts";
import { createPlan } from "../../helpers/catalog.ts";
import { JOB, addDays, addMonths, expectInvariants, jst, meSubscription, startAtNext15, subscribe, untilStatus } from "../../helpers/orchestration.ts";

test("E2E-006: 解約予約した契約は期間の最終日まで利用でき、更新日に請求なしで canceled（プロセス ended）", async ({ api, world, db, wait, clock }) => {
  const D = await startAtNext15(clock);
  const tenant = await world.createTenant({ apiKey: true });
  const plan = await createPlan(api, tenant, { amount: 980 });
  const { consumer, subscription: sub } = await subscribe(api, world, tenant, plan.id);
  await clock.setTo(jst(addDays(D, 5), "09:00"));
  expect((await api.call("PUT", `/v1/me/subscriptions/${sub.id}/scheduled-cancellation`, { token: consumer.token, body: {} })).status).toBe(200);
  const renewal = addMonths(D, 1);
  await clock.setTo(jst(addDays(renewal, -1), "12:00"));
  const before = await api.call<{ entitled: boolean; scheduled_cancellation: unknown }>("GET", `/v1/me/subscriptions/${sub.id}`, { token: consumer.token });
  expect(before.body.entitled).toBe(true);
  expect(before.body.scheduled_cancellation).not.toBeNull();

  await clock.setTo(jst(renewal, "00:30"));
  await clock.runJob(JOB.dispatch, { asOf: renewal, force: true });
  await untilStatus(api, wait, consumer, sub.id, "canceled");
  expect((await meSubscription(api, consumer, sub.id)).entitled).toBe(false);
  expect(await db.count("SELECT count(*) FROM billing.invoices WHERE subscription_id = $1", [sub.id])).toBe(1);
  await wait.until(async () => (await db.count("SELECT count(*) FROM orchestration.billing_cycle_processes WHERE subscription_id = $1 AND kind = 'renewal' AND outcome = 'ended'", [sub.id])) === 1, {
    message: "renewal プロセスが ended で閉じる",
  });
  await expectInvariants(db, wait, tenant.id);
});
