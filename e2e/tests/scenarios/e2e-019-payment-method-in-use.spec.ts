// E2E-019 使用中の支払方法の削除（orchestration.md §11b.3）：AC-ORC-031・032、C-04。
import { test, expect } from "../../fixtures/index.ts";
import { createPlan } from "../../helpers/catalog.ts";
import { ALL_EVENT_TYPES, bodyOf, expectNoMoreRequests, sinkEndpoint } from "../../helpers/integration.ts";
import { subscribe } from "../../helpers/orchestration.ts";

test("E2E-019: 使用中の支払方法は削除できず 409、別の支払方法に変えると削除できる", async ({ api, world, db, sink, wait }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const w = await sinkEndpoint(api, sink, tenant, ALL_EVENT_TYPES);
  const { consumer, pm: pm1, subscription } = await subscribe(api, world, tenant, plan.id);

  const del = await api.call("DELETE", `/v1/me/payment-methods/${pm1.id}`, { token: consumer.token });
  expect(del.status).toBe(409);
  expect(del.problem?.code).toBe("payment_method_in_use");
  expect(await db.count("SELECT count(*) FROM payments.payment_methods WHERE id = $1 AND status = 'active'", [pm1.id])).toBe(1);

  const pm2 = await world.createPaymentMethod(consumer, "ok");
  const chg = await api.call<{ recollection: string; payment_method_id: string }>("PUT", `/v1/me/subscriptions/${subscription.id}/payment-method`, {
    token: consumer.token,
    body: { payment_method_id: pm2.id },
  });
  expect(chg.status, JSON.stringify(chg.body)).toBe(200);
  expect(chg.body).toMatchObject({ recollection: "none", payment_method_id: pm2.id });

  expect((await api.call("DELETE", `/v1/me/payment-methods/${pm1.id}`, { token: consumer.token })).status).toBe(204);
  expect((await api.call("DELETE", `/v1/me/payment-methods/${pm1.id}`, { token: consumer.token })).status).toBe(204); // 削除済みも 204
  const list = await api.call<{ data: { id: string }[] }>("GET", "/v1/me/payment-methods", { token: consumer.token });
  expect(list.body.data.map((m) => m.id)).not.toContain(pm1.id);
  expect(await db.count("SELECT count(*) FROM payments.payment_methods WHERE id = $1 AND status = 'deleted'", [pm1.id])).toBe(1);
  expect(await db.count("SELECT count(*) FROM subscription.subscriptions WHERE id = $1 AND payment_method_id = $2", [subscription.id, pm2.id])).toBe(1);
  // subscription.payment_method_changed は外部に出さない（integration-webhook の購読対象でもない）。送信済みになった後も sink には申込の 2 件だけ
  await wait.until(
    async () =>
      (await db.count(
        "SELECT count(*) FROM subscription.outbox_events WHERE event_type = 'subscription.payment_method_changed' AND aggregate_id = $1 AND published_at IS NOT NULL",
        [subscription.id],
      )) === 1,
    { message: "payment_method_changed が送信済み" },
  );
  const recs = await sink.waitForRequests(w.sinkId, 2);
  expect(recs.map((r) => bodyOf(r).type).sort()).toEqual(["invoice.paid", "subscription.started"]);
  await expectNoMoreRequests(sink, w.sinkId, recs.at(-1)!.seq, 2_000);
});
