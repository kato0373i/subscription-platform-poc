// E2E-001 トライアルなしの申込（orchestration.md §11b.3）：AC-ORC-001、AC-BIL-011、P-X-3。
// 申込ページ（ブラウザでのカードのトークン化）は申込ページの実装後に追加する。ここでは API で支払方法を登録する。
import { test, expect } from "../../fixtures/index.ts";
import { createPlan } from "../../helpers/catalog.ts";
import { bodyOf, sinkEndpoint } from "../../helpers/integration.ts";
import { checkout, expectInvariants, type Consumer } from "../../helpers/orchestration.ts";
import { verifySignature } from "../../helpers/webhook-signature.ts";

test("E2E-001: トライアルなしの申込は 201 active、請求 paid（caller_managed）、試行 1 件（invoice:{id}:1）", async ({ api, world, db, wait, sink }) => {
  const tenant = await world.createTenant({ apiKey: true });
  const plan = await createPlan(api, tenant, { amount: 980 });
  const w1 = await sinkEndpoint(api, sink, tenant, ["subscription.started", "invoice.paid"]);
  const consumer = (await world.createConsumer()) as Consumer;
  const pm = await world.createPaymentMethod(consumer, "ok");
  const today = ((await api.dev.getClock()).body as { today: string }).today;

  const res = await checkout(api, consumer, tenant, plan.id, pm.id);
  expect(res.status, JSON.stringify(res.body)).toBe(201);
  const { subscription: sub, invoice } = res.body;
  expect(sub).toMatchObject({ status: "active", tenant_id: tenant.id, plan_id: plan.id, trial_applied: false, trial_end: null });
  expect(sub.current_period.start).toBe(today);
  const end = new Date(`${sub.current_period.end}T00:00:00Z`);
  end.setUTCDate(end.getUTCDate() + 1);
  expect(sub.next_renewal_date).toBe(end.toISOString().slice(0, 10));
  expect(invoice).toMatchObject({ total: 980, tax: 89, status: "paid" });

  const [cus] = await db.query<{ tenant_customer_id: string }>("SELECT tenant_customer_id FROM subscription.subscriptions WHERE id = $1", [sub.id]);
  const ent = await api.call<{ entitled: boolean }>("GET", `/v1/tenant/customers/${cus!.tenant_customer_id}/entitlements`, { token: tenant.apiKey });
  expect(ent.status, JSON.stringify(ent.body)).toBe(200);

  expect(await db.count("SELECT count(*) FROM billing.invoices WHERE subscription_id = $1 AND kind = 'initial' AND collection_mode = 'caller_managed'", [sub.id])).toBe(1);
  expect(await db.count("SELECT count(*) FROM payments.payment_attempts WHERE idempotency_key = $1 AND status = 'succeeded'", [`invoice:${invoice!.id}:1`])).toBe(1);
  expect(await db.count("SELECT count(*) FROM billing.invoice_payment_records WHERE invoice_id = $1 AND source = 'sync' AND outcome = 'succeeded'", [invoice!.id])).toBe(1);
  expect(
    await db.count(
      "SELECT count(*) FROM subscription.subscriptions s JOIN customer.tenant_customer_links l ON l.tenant_id = s.tenant_id AND l.customer_account_id = s.customer_account_id WHERE s.id = $1 AND l.id = s.tenant_customer_id",
      [sub.id],
    ),
  ).toBe(1);
  await expectInvariants(db, wait, tenant.id);

  // W1 に subscription.started・invoice.paid が届く。契約者 ID は cus_…（内部 ID を出さない）
  const recs = await sink.waitForRequests(w1.sinkId, 2);
  const byType = Object.fromEntries(recs.map((r) => [bodyOf(r).type, r]));
  expect(Object.keys(byType).sort()).toEqual(["invoice.paid", "subscription.started"]);
  const started = bodyOf(byType["subscription.started"]!);
  expect(started.data.object).toMatchObject({ object: "subscription", id: sub.id, customer_id: cus!.tenant_customer_id, status: "active" });
  expect(started.data.object.customer_id).toMatch(/^cus_/);
  expect(JSON.stringify(started)).not.toContain(consumer.customerAccountId);
  expect(bodyOf(byType["invoice.paid"]!).data.object).toMatchObject({ object: "invoice", id: invoice!.id, amount: 980 });
  for (const r of recs) expect(verifySignature(r, w1.endpoint.secret)).toBe(true);
});
