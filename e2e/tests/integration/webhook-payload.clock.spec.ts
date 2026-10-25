// 更新課金のイベントの中身・購読の絞り込み・停止した事業者への配信（integration.md §4.1・§4.2・§9 E13）：AC-INT-008・009・034。
import { test, expect } from "../../fixtures/index.ts";
import { createPlan } from "../../helpers/catalog.ts";
import { bodyOf, sinkEndpoint } from "../../helpers/integration.ts";
import { JOB, addMonths, jst, startAtNext15, subscribe, untilStatus } from "../../helpers/orchestration.ts";

test("AC-INT-008・009: subscription.renewed の本文に evt_・cus_・version があり内部 ID がない。invoice.paid だけを購読する送信先には invoice.paid だけが届く", async ({ api, world, sink, clock, db, wait }) => {
  const D = await startAtNext15(clock);
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const a = await sinkEndpoint(api, sink, tenant, ["subscription.renewed"]);
  const b = await sinkEndpoint(api, sink, tenant, ["invoice.paid"]);
  const { consumer, subscription } = await subscribe(api, world, tenant, plan.id);
  const renewal = addMonths(D, 1);
  await clock.setTo(jst(renewal, "00:30"));
  await clock.runJob(JOB.dispatch, { asOf: renewal, force: true });
  await untilStatus(api, wait, consumer, subscription.id, "active");

  const [renewed] = await sink.waitForRequests(a.sinkId, 1);
  const body = bodyOf(renewed!);
  const [link] = await db.query<{ tenant_customer_id: string }>("SELECT tenant_customer_id FROM subscription.subscriptions WHERE id = $1", [subscription.id]);
  const [inv] = await db.query<{ id: string }>("SELECT id FROM billing.invoices WHERE subscription_id = $1 AND kind = 'renewal'", [subscription.id]);
  const [internal] = await db.query<{ id: string; corr: string; ver: number }>(
    "SELECT id, correlation_id AS corr, aggregate_version AS ver FROM subscription.outbox_events WHERE aggregate_id = $1 AND event_type = 'subscription.renewed'",
    [subscription.id],
  );
  expect(body.id).toMatch(/^evt_[0-9A-HJKMNP-TV-Z]{26}$/);
  expect(body).toMatchObject({ type: "subscription.renewed", api_version: "2026-10-01", tenant_id: tenant.id, data: { invoice_id: inv!.id } });
  expect(body.data.object).toMatchObject({
    object: "subscription", id: subscription.id, customer_id: link!.tenant_customer_id, status: "active", entitled: true, version: Number(internal!.ver),
    current_period: { start: renewal }, next_renewal_date: addMonths(D, 2), cancel_at_period_end: false,
  });
  expect(body.created_at).toMatch(/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$/);
  const text = renewed!.body_text;
  for (const bad of ["customer_account_id", consumer.customerAccountId, internal!.id, internal!.corr]) expect(text).not.toContain(bad);
  expect(body.data.object).not.toHaveProperty("tenant_id");

  // invoice.paid だけを購読：申込と更新の 2 件、どちらも invoice.paid
  const paid = await sink.waitForRequests(b.sinkId, 2);
  expect(paid.map((r) => bodyOf(r).type)).toEqual(["invoice.paid", "invoice.paid"]);
  const renewalPaid = paid.map((r) => bodyOf(r)).find((x) => x.data.object.id === inv!.id)!;
  expect(renewalPaid.data.object).toMatchObject({ object: "invoice", subscription_id: subscription.id, amount: 980 });
  expect(renewalPaid.data.object).not.toHaveProperty("customer_id");
  expect(await sink.requests(a.sinkId)).toHaveLength(1);
});

test("AC-INT-034・E13: 事業者を停止すると、停止に伴う cancellation_scheduled（reason = tenant_suspended）と期間終了の canceled が届く", async ({ api, world, sink, clock, wait, tokens }) => {
  const D = await startAtNext15(clock);
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const s = await sinkEndpoint(api, sink, tenant, ["subscription.cancellation_scheduled", "subscription.canceled"]);
  const { consumer, subscription } = await subscribe(api, world, tenant, plan.id);
  const op = await tokens.operator();
  expect((await api.call("POST", `/v1/ops/tenants/${tenant.id}/suspend`, { token: op, body: { reason: "AC-INT-034" } })).status).toBe(200);
  await wait.until(
    async () => {
      await clock.runJob(JOB.bulk, { force: true });
      const r = await api.call<{ data: { status: string }[] }>("GET", "/v1/ops/bulk-cancellation-jobs", { token: op, query: { tenant_id: tenant.id } });
      return r.body.data[0]?.status === "completed";
    },
    { message: "一括解約が完了する" },
  );
  const [scheduled] = await sink.waitForRequests(s.sinkId, 1);
  expect(bodyOf(scheduled!)).toMatchObject({
    type: "subscription.cancellation_scheduled",
    data: { reason: "tenant_suspended", effective_date: subscription.next_renewal_date, object: { id: subscription.id, cancel_at_period_end: true } },
  });
  const end = addMonths(D, 1);
  await clock.setTo(jst(end, "00:30"));
  await clock.runJob(JOB.dispatch, { asOf: end, force: true });
  await untilStatus(api, wait, consumer, subscription.id, "canceled");
  const recs = await sink.waitForRequests(s.sinkId, 2);
  expect(bodyOf(recs[1]!)).toMatchObject({ type: "subscription.canceled", data: { reason: "tenant_suspended", object: { id: subscription.id, status: "canceled" } } });
});
