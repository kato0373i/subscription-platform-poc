// orchestration.md §4.8（退会）：AC-ORC-039・040、AC-IDN-029。解約予約中を含むシナリオは E2E-018。
import { test, expect } from "../../fixtures/index.ts";
import { createPlan } from "../../helpers/catalog.ts";
import { subscribe, type Consumer } from "../../helpers/orchestration.ts";

test("有効な契約があれば 409 has_open_subscriptions、契約がなければ 204 で以後の /v1/me は 403 account_withdrawn", async ({ api, world, db, wait }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const { consumer } = await subscribe(api, world, tenant, plan.id);
  const res = await api.call("DELETE", "/v1/me", { token: consumer.token });
  expect(res.status).toBe(409);
  expect(res.problem?.code).toBe("has_open_subscriptions");
  expect(await db.count("SELECT count(*) FROM customer.customer_accounts WHERE id = $1 AND status = 'active'", [consumer.customerAccountId])).toBe(1);

  const lonely = (await world.createConsumer()) as Consumer;
  const pm = await world.createPaymentMethod(lonely, "ok");
  const pm2 = await world.createPaymentMethod(lonely, "ok"); // AC-PAY-026：有効な支払方法が 2 件
  expect((await api.call("DELETE", "/v1/me", { token: lonely.token })).status).toBe(204);
  const after = await api.call("GET", "/v1/me/subscriptions", { token: lonely.token });
  expect(after.status).toBe(403);
  expect(after.problem?.code).toBe("account_withdrawn");
  expect(await db.count("SELECT count(*) FROM customer.customer_accounts WHERE id = $1 AND status = 'withdrawn'", [lonely.customerAccountId])).toBe(1);
  await wait.until(async () => (await db.count("SELECT count(*) FROM payments.payment_methods WHERE id IN ($1, $2) AND status = 'deleted'", [pm.id, pm2.id])) === 2, {
    message: "customer.withdrawn の購読で支払方法が無効になる",
  });
});
