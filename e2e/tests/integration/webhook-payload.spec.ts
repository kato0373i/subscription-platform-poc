// 申込の決済失敗は Webhook・メールの対象外（integration.md §4.1・§5.2・§5.3、P-BIL-5）：AC-INT-032、E12。
import { test, expect } from "../../fixtures/index.ts";
import { createPlan } from "../../helpers/catalog.ts";
import { bodyOf, expectNoMoreRequests, sinkEndpoint } from "../../helpers/integration.ts";
import { checkout, subscribe, type Consumer } from "../../helpers/orchestration.ts";

test("AC-INT-032: 申込の決済失敗（caller_managed）では invoice.payment_failed が届かず、メールの行もない", async ({ api, world, sink, db, wait }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const s = await sinkEndpoint(api, sink, tenant, ["invoice.payment_failed", "subscription.started"]);
  const consumer = (await world.createConsumer()) as Consumer;
  const pm = await world.createPaymentMethod(consumer, "decline");
  const res = await checkout(api, consumer, tenant, plan.id, pm.id);
  expect(res.status, JSON.stringify(res.body)).toBe(422);
  const [inv] = await db.query<{ id: string }>("SELECT id FROM billing.invoices WHERE tenant_id = $1 AND collection_mode = 'caller_managed'", [tenant.id]);
  // イベントは integration の Inbox まで届いたうえで捨てられる
  await wait.until(
    async () =>
      (await db.count(
        `SELECT count(*) FROM integration.processed_messages p JOIN billing.outbox_events o ON o.id = p.message_id
          WHERE o.event_type = 'billing.invoice_payment_failed' AND o.aggregate_id = $1 AND p.consumer = 'integration.webhook'`,
        [inv!.id],
      )) === 1,
    { message: "integration.webhook が処理済み" },
  );
  // 比較のために成功する申込を 1 件流し、その後も payment_failed が来ないことを確かめる
  await subscribe(api, world, tenant, plan.id);
  const recs = await sink.waitForRequests(s.sinkId, 1);
  expect(recs.map((r) => bodyOf(r).type)).toEqual(["subscription.started"]);
  await expectNoMoreRequests(sink, s.sinkId, recs.at(-1)!.seq);
  expect(await db.count("SELECT count(*) FROM integration.email_notifications WHERE invoice_id = $1", [inv!.id])).toBe(0);
  expect(await db.count("SELECT count(*) FROM integration.webhook_events WHERE tenant_id = $1 AND event_type = 'invoice.payment_failed'", [tenant.id])).toBe(0);
});
