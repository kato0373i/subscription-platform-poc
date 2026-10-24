// payments.md §7.8・§8.2（非同期の確定が Webhook → Inbox → SQS → worker-billing の受信ループで反映される）
// AC-PAY-019 の決済の部分（更新課金を通す確認は orchestration・billing の実装後）。
import { test, expect } from "../../fixtures/index.ts";
import { createPlan } from "../../helpers/catalog.ts";

test("確定待ちの決済を dev の settle で確定すると、Webhook の経路を通って succeeded（resolution=webhook）になる", async ({ api, world, db, wait }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  await world.seedSubscriptions(tenant, plan.id, 1, "async_ok");
  const [row] = await db.query<{ id: string; status: string; payable_id: string }>("SELECT id, status, payable_id FROM payments.payment_attempts WHERE tenant_id = $1", [tenant.id]);
  expect(row!.status).toBe("pending_async");

  const res = await api.call("POST", `/v1/dev/payments/stub/payables/invoice/${row!.payable_id}/settle`, { body: {} });
  expect(res.status, JSON.stringify(res.body)).toBe(202);
  const done = await wait.until(
    async () => {
      const [r] = await db.query<{ status: string; resolution: string | null }>("SELECT status, resolution FROM payments.payment_attempts WHERE id = $1", [row!.id]);
      return r!.status === "succeeded" ? r : undefined;
    },
    { message: "Webhook の受信ループで確定する" },
  );
  expect(done.resolution).toBe("webhook");
  expect(await db.count("SELECT count(*) FROM payments.provider_events WHERE provider_order_ref = $1 AND status = 'processed'", [`stub_${row!.id}`])).toBe(1);
  expect(await db.count("SELECT count(*) FROM payments.outbox_events WHERE aggregate_id = $1 AND event_type = 'payments.payment_succeeded'", [row!.id])).toBe(1);

  // 確定済みの試行の請求には進行中の試行がないので 404、同じ注文の 2 回目の settle は 409
  expect((await api.call("POST", `/v1/dev/payments/stub/payables/invoice/${row!.payable_id}/settle`, { body: {} })).status).toBe(404);
  expect((await api.call("POST", `/v1/dev/payments/stub/charges/stub_${row!.id}/settle`, { body: {} })).status).toBe(409);
});
