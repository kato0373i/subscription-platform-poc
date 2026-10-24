// billing.md §7.1・§7.2（契約者・事業者の請求の照会）：AC-BIL-031、FR-018。請求は一括シード（billing.IssueInvoice を通る）で作る。
import { test, expect } from "../../fixtures/index.ts";
import { consoleOf, createPlan } from "../../helpers/catalog.ts";
import { seedActive } from "../../helpers/subscription.ts";
import type { Invoice, MeInvoice } from "../../helpers/billing.ts";
import { expectIsolated } from "../../helpers/isolation.ts";

test(
  "申込の請求（initial・caller_managed）が paid で作られ、契約者と事業者から見える。他人の契約は空の一覧（AC-BIL-031）",
  { tag: "@isolation:meListInvoices" },
  async ({ api, world, tokens, db }) => {
    const tenant = await world.createTenant();
    const plan = await createPlan(api, tenant, { amount: 980 });
    const seeded = await seedActive(api, world, tokens, tenant, plan.id, 2);
    const [sub1] = await seeded.subscriptions();
    const me1 = await seeded.consumer(1);

    const list = await api.call<{ data: MeInvoice[]; has_more: boolean }>("GET", `/v1/me/subscriptions/${sub1!.id}/invoices`, { token: me1 });
    expect(list.status, JSON.stringify(list.body)).toBe(200);
    expect(list.body.has_more).toBe(false);
    expect(list.body.data).toHaveLength(1);
    const inv = list.body.data[0]!;
    expect(inv).toMatchObject({ status: "paid", kind: "initial", total: 980, tax: { rate: "10%", amount: 89 }, next_collection_at: null });
    expect(inv.lines).toEqual([{ kind: "plan", description: "プラン料金", amount: 980 }]);
    expect(inv.period.start).toBe(sub1!.current_period.start);
    expect(inv.paid_at).not.toBeNull();

    const me2 = await seeded.consumer(2); // 他人の契約・存在しない契約は 404 ではなく空の一覧（存在を漏らさない。§7.1）
    const theirs = await api.call<{ data: MeInvoice[] }>("GET", `/v1/me/subscriptions/${sub1!.id}/invoices`, { token: me2 });
    expect(theirs.status).toBe(200);
    expect(theirs.body.data).toEqual([]);
    const none = await api.call<{ data: MeInvoice[] }>("GET", `/v1/me/subscriptions/${crypto.randomUUID()}/invoices`, { token: me1 });
    expect(none.body.data).toEqual([]);

    // console の一覧と詳細
    const call = consoleOf(api, tenant);
    const cl = await call<{ data: Invoice[] }>("GET", `/v1/console/subscriptions/${sub1!.id}/invoices`);
    expect(cl.status).toBe(200);
    expect(cl.body.data[0]).toMatchObject({ id: inv.id, subscription_id: sub1!.id, collection_mode: "caller_managed", collection_seq: 1, attempt_count: 0, dunning_stage: 0 });
    expect(cl.body.data[0]).not.toHaveProperty("customer_account_id");
    const one = await call<Invoice>("GET", `/v1/console/invoices/${inv.id}`);
    expect(one.status).toBe(200);
    expect(one.body).toMatchObject({ status: "paid", subtotal: 980, discount_total: 0, total: 980 });

    // 決済の冪等キーは invoice:{請求 ID}:1（P-X-3）、invoice_issued・invoice_paid を 1 件ずつ出す
    expect(await db.count("SELECT count(*) FROM payments.payment_attempts WHERE idempotency_key = $1", [`invoice:${inv.id}:1`])).toBe(1);
    expect(await db.count("SELECT count(*) FROM billing.outbox_events WHERE aggregate_id = $1 AND event_type IN ('billing.invoice_issued','billing.invoice_paid')", [inv.id])).toBe(2);
    expect(await db.count("SELECT count(*) FROM billing.invoice_payment_records WHERE invoice_id = $1 AND outcome = 'succeeded' AND source = 'sync'", [inv.id])).toBe(1);
  },
);

test("他の事業者の請求は console の詳細で 404", { tag: "@isolation:consoleGetInvoice" }, async ({ api, world, tokens }) => {
  const a = await world.createTenant();
  const b = await world.createTenant();
  const plan = await createPlan(api, b, { amount: 980 });
  const seeded = await seedActive(api, world, tokens, b, plan.id, 1);
  const [sub] = await seeded.subscriptions();
  const list = await consoleOf(api, b)<{ data: Invoice[] }>("GET", `/v1/console/subscriptions/${sub!.id}/invoices`);
  const id = list.body.data[0]!.id;
  await expectIsolated((as) => consoleOf(api, as === "owner" ? b : a)("GET", `/v1/console/invoices/${id}`));
  // 一覧は RLS で空になる
  expect((await consoleOf(api, a)<{ data: Invoice[] }>("GET", `/v1/console/subscriptions/${sub!.id}/invoices`)).body.data).toEqual([]);
});

test("申込の決済が失敗すると請求は void（signup_failed）、契約は signup_failed", async ({ api, world, db }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const seeded = await world.seedSubscriptions(tenant, plan.id, 1, "decline");
  expect(seeded.failed).toBe(1);
  const [inv] = await db.query<{ id: string; status: string; void_reason: string; attempt_count: number; subscription_id: string }>(
    "SELECT id, status, void_reason, attempt_count, subscription_id FROM billing.invoices WHERE tenant_id = $1",
    [tenant.id],
  );
  expect(inv).toMatchObject({ status: "void", void_reason: "signup_failed", attempt_count: 1 });
  const events = await db.query<{ event_type: string; payload: { collection_mode?: string } }>(
    "SELECT event_type, payload FROM billing.outbox_events WHERE aggregate_id = $1 ORDER BY aggregate_version, occurred_at",
    [inv!.id],
  );
  expect(events.map((e) => e.event_type)).toEqual(["billing.invoice_issued", "billing.invoice_payment_failed", "billing.invoice_voided"]);
  expect(events[1]!.payload.collection_mode).toBe("caller_managed");
  expect(await db.count("SELECT count(*) FROM subscription.subscriptions WHERE id = $1 AND status = 'signup_failed'", [inv!.subscription_id])).toBe(1);
});
