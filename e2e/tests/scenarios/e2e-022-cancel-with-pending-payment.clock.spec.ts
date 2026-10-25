// E2E-022 結果待ちの決済がある契約の解約（orchestration.md §11b.3）：AC-ORC-028、AC-BIL-021・022・039、P-BIL-3・P-SUB-5。
import { test, expect } from "../../fixtures/index.ts";
import { createPlan } from "../../helpers/catalog.ts";
import { JOB, addDays, addMonths, jst, meSubscription, setBehavior, startAtNext15, subscribe, untilStatus } from "../../helpers/orchestration.ts";

test("E2E-022: 結果不明の請求がある past_due の契約を解約 → 照合で成功が確定しても契約は canceled のまま、請求は paid・異常 paid_after_void_request", async ({ api, world, db, wait, clock }) => {
  const D = await startAtNext15(clock);
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const { consumer, pm, subscription: sub } = await subscribe(api, world, tenant, plan.id);
  await setBehavior(api, pm.id, "decline", 1);
  const renewal = addMonths(D, 1);
  await clock.setTo(jst(renewal, "00:30"));
  await clock.runJob(JOB.dispatch, { asOf: renewal, force: true });
  await untilStatus(api, wait, consumer, sub.id, "past_due");
  const [inv] = await db.query<{ id: string }>("SELECT id FROM billing.invoices WHERE subscription_id = $1 AND kind = 'renewal'", [sub.id]);

  await setBehavior(api, pm.id, "timeout", 1);
  await clock.setTo(jst(addDays(renewal, 1), "10:00"));
  await clock.runJob(JOB.collectionDue, { force: true });
  await wait.until(async () => (await db.count("SELECT count(*) FROM billing.invoices WHERE id = $1 AND status = 'payment_unknown'", [inv!.id])) === 1, { message: "請求が payment_unknown" });

  const res = await api.call<{ status: string }>("PUT", `/v1/me/subscriptions/${sub.id}/scheduled-cancellation`, { token: consumer.token, body: {} });
  expect(res.body.status).toBe("canceled");
  await wait.until(async () => (await db.count("SELECT count(*) FROM billing.invoices WHERE id = $1 AND status = 'payment_unknown' AND void_requested_at IS NOT NULL", [inv!.id])) === 1, {
    message: "無効化の要求が記録される",
  });

  await clock.advance("1m");
  await clock.runJob(JOB.resolveUnknown, { force: true });
  await wait.until(async () => (await db.count("SELECT count(*) FROM billing.invoices WHERE id = $1 AND status = 'paid'", [inv!.id])) === 1, { message: "照合で paid" });
  expect(await db.count("SELECT count(*) FROM billing.invoice_payment_records WHERE invoice_id = $1 AND anomaly = 'paid_after_void_request'", [inv!.id])).toBe(1);
  expect(await db.count("SELECT count(*) FROM payments.payment_attempts WHERE payable_id = $1 AND status = 'succeeded'", [inv!.id])).toBe(1);
  await wait.until(async () => (await db.count("SELECT count(*) FROM orchestration.billing_cycle_processes WHERE invoice_id = $1 AND state <> 'completed'", [inv!.id])) === 0, {
    message: "プロセスが閉じる（invoice_paid を canceled で受けてアラート）",
  });
  expect((await meSubscription(api, consumer, sub.id)).status).toBe("canceled");
  const list = await api.call<{ data: { id: string; status: string }[] }>("GET", `/v1/me/subscriptions/${sub.id}/invoices`, { token: consumer.token });
  expect(list.body.data.find((i) => i.id === inv!.id)?.status).toBe("paid");
});
