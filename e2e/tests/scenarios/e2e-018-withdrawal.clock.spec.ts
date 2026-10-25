// E2E-018 退会の制限（orchestration.md §11b.3）：AC-ORC-039・040、AC-IDN-029・033、D-05・P-SUB-3・P-X-8。
import { test, expect } from "../../fixtures/index.ts";
import { createPlan } from "../../helpers/catalog.ts";
import { JOB, addMonths, jst, startAtNext15, subscribe, untilStatus } from "../../helpers/orchestration.ts";

test("E2E-018: 有効な契約（解約予約中を含む）があると退会できず、期間終了で解約済みになると退会できる", async ({ api, world, db, wait, clock }) => {
  const D = await startAtNext15(clock);
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const { consumer, pm, subscription: sub } = await subscribe(api, world, tenant, plan.id);
  const r1 = await api.call("DELETE", "/v1/me", { token: consumer.token });
  expect(r1.status).toBe(409);
  expect(r1.problem?.code).toBe("has_open_subscriptions");
  expect((await api.call("PUT", `/v1/me/subscriptions/${sub.id}/scheduled-cancellation`, { token: consumer.token, body: {} })).status).toBe(200);
  expect((await api.call("DELETE", "/v1/me", { token: consumer.token })).status).toBe(409);
  expect(await db.count("SELECT count(*) FROM customer.customer_accounts WHERE id = $1 AND status = 'active'", [consumer.customerAccountId])).toBe(1);

  const renewal = addMonths(D, 1);
  await clock.setTo(jst(renewal, "00:30"));
  await clock.runJob(JOB.dispatch, { asOf: renewal, force: true });
  await untilStatus(api, wait, consumer, sub.id, "canceled");
  expect((await api.call("DELETE", "/v1/me", { token: consumer.token })).status).toBe(204);
  const after = await api.call("GET", "/v1/me/subscriptions", { token: consumer.token });
  expect(after.status).toBe(403);
  expect(after.problem?.code).toBe("account_withdrawn");
  expect(await db.count("SELECT count(*) FROM customer.customer_accounts WHERE id = $1 AND status = 'withdrawn' AND email IS NULL", [consumer.customerAccountId])).toBe(1);
  await wait.untilEvent("customer.withdrawn", { aggregateId: consumer.customerAccountId, schema: "customer" });
  await wait.until(async () => (await db.count("SELECT count(*) FROM payments.payment_methods WHERE id = $1 AND status = 'deleted'", [pm.id])) === 1, { message: "支払方法が無効" });
});
