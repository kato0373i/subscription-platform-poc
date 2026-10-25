// E2E-010 支払い遅延中の解約（orchestration.md §11b.3）：AC-ORC-027・034、AC-BIL-028、D-03・P-ORC-1。
import { test, expect } from "../../fixtures/index.ts";
import { createPlan } from "../../helpers/catalog.ts";
import { JOB, addDays, addMonths, expectInvariants, jst, meSubscription, setBehavior, startAtNext15, subscribe, untilStatus } from "../../helpers/orchestration.ts";

test("E2E-010: past_due の契約を解約すると即時に canceled、請求は void。回収時刻にも試行は増えない", async ({ api, world, db, wait, clock, tokens }) => {
  const D = await startAtNext15(clock);
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const { consumer, pm, subscription: sub } = await subscribe(api, world, tenant, plan.id);
  await setBehavior(api, pm.id, "decline", 99);
  const renewal = addMonths(D, 1);
  await clock.setTo(jst(renewal, "00:30"));
  await clock.runJob(JOB.dispatch, { asOf: renewal, force: true });
  await untilStatus(api, wait, consumer, sub.id, "past_due");

  const res = await api.call<{ status: string; entitled: boolean }>("PUT", `/v1/me/subscriptions/${sub.id}/scheduled-cancellation`, { token: consumer.token, body: {} });
  expect(res.status).toBe(200);
  expect(res.body).toMatchObject({ status: "canceled", entitled: false });
  const [inv] = await db.query<{ id: string }>("SELECT id FROM billing.invoices WHERE subscription_id = $1 AND kind = 'renewal'", [sub.id]);
  await wait.until(async () => (await db.count("SELECT count(*) FROM billing.invoices WHERE id = $1 AND status = 'void' AND void_reason = 'subscription_canceled'", [inv!.id])) === 1, {
    message: "請求が void",
  });
  await wait.untilEvent("billing.invoice_voided", { aggregateId: inv!.id, schema: "billing" });

  await clock.setTo(jst(addDays(renewal, 1), "10:00"));
  await clock.runJob(JOB.collectionDue, { force: true });
  expect(await db.count("SELECT count(*) FROM payments.payment_attempts WHERE payable_id = $1", [inv!.id])).toBe(1);
  expect((await meSubscription(api, consumer, sub.id)).status).toBe("canceled");
  // ops の再決済も not_collectible（AC-BIL-028）
  const op = await tokens.operator();
  expect((await api.call("POST", `/v1/ops/invoices/${inv!.id}/collect`, { token: op })).problem?.code).toBe("invoice_not_collectible");
  await expectInvariants(db, wait, tenant.id);
});
