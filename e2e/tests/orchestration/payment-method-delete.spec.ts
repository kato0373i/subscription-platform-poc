// orchestration.md §4.5（支払方法の削除）：AC-ORC-031（pending_initial）・032。
import { test, expect } from "../../fixtures/index.ts";
import { createPlan } from "../../helpers/catalog.ts";
import { checkout, type Consumer } from "../../helpers/orchestration.ts";

test("AC-ORC-031: 申込の結果待ち（pending_initial）の契約で使用中の支払方法は 409 payment_method_in_use", async ({ api, world }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const consumer = (await world.createConsumer()) as Consumer;
  const pm = await world.createPaymentMethod(consumer, "timeout_lost");
  expect((await checkout(api, consumer, tenant, plan.id, pm.id)).status).toBe(202);
  const res = await api.call("DELETE", `/v1/me/payment-methods/${pm.id}`, { token: consumer.token });
  expect(res.status).toBe(409);
  expect(res.problem?.code).toBe("payment_method_in_use");
});

test("AC-ORC-032: 使われていない支払方法の削除は 204、もう一度も 204。存在しない ID は 404", async ({ api, world }) => {
  const consumer = (await world.createConsumer()) as Consumer;
  const pm = await world.createPaymentMethod(consumer, "ok");
  expect((await api.call("DELETE", `/v1/me/payment-methods/${pm.id}`, { token: consumer.token })).status).toBe(204);
  expect((await api.call("DELETE", `/v1/me/payment-methods/${pm.id}`, { token: consumer.token })).status).toBe(204);
  expect((await api.call("DELETE", `/v1/me/payment-methods/${crypto.randomUUID()}`, { token: consumer.token })).status).toBe(404);
});
