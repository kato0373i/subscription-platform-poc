// subscription.md §7.2（解約の予約）：AC-SUB-014 の API、予約と取り消し、プラン変更予約との関係（X-5・X-6）
import { test, expect } from "../../fixtures/index.ts";
import { consoleOf, createPlan, type Plan } from "../../helpers/catalog.ts";
import { seedActive, type MeSubscription } from "../../helpers/subscription.ts";

test(
  "解約を予約すると適用日（次の更新日）を返し、利用権は残る。取り消せる。他人の契約は 404",
  { tag: ["@isolation:meScheduleCancellation", "@isolation:meRevokeCancellation"] },
  async ({ api, world, tokens, db }) => {
    const tenant = await world.createTenant();
    const plan = await createPlan(api, tenant);
    const seeded = await seedActive(api, world, tokens, tenant, plan.id, 2);
    const [mine, others] = [await seeded.consumer(1), await seeded.consumer(2)];
    const subId = (await api.call<{ data: MeSubscription[] }>("GET", "/v1/me/subscriptions", { token: mine })).body.data[0]!.id;
    const path = `/v1/me/subscriptions/${subId}/scheduled-cancellation`;

    expect((await api.call("PUT", path, { token: others, body: {} })).status).toBe(404);
    expect((await api.call("DELETE", path, { token: others })).status).toBe(404);

    const res = await api.call<MeSubscription>("PUT", path, { token: mine, body: {} });
    expect(res.status, JSON.stringify(res.body)).toBe(200);
    expect(res.body).toMatchObject({
      status: "active",
      entitled: true,
      scheduled_cancellation: { effective_date: seeded.firstRenewalDate, revocable: true, source: "customer" },
    });
    const events = await db.query<{ payload: { reason: string; effective_date: string; subscription: { cancel_at_period_end: boolean } } }>(
      "SELECT payload FROM subscription.outbox_events WHERE aggregate_id = $1 AND event_type = 'subscription.cancellation_scheduled'",
      [subId],
    );
    expect(events).toHaveLength(1);
    expect(events[0]!.payload).toMatchObject({ reason: "customer", effective_date: seeded.firstRenewalDate, subscription: { cancel_at_period_end: true } });

    const revoked = await api.call<MeSubscription>("DELETE", path, { token: mine });
    expect(revoked.status).toBe(200);
    expect(revoked.body.scheduled_cancellation).toBeNull();
  },
);

test("X-5・X-6: 解約予約中はプラン変更できず、プラン変更予約中に解約を予約するとプラン変更予約は消える", async ({ api, world, tokens }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant);
  const call = consoleOf(api, tenant);
  const upper = await call<Plan>("POST", "/v1/console/plans", { product_id: plan.product_id, name: "上位", billing_interval: "month", trial_days: 0, amount: 1980 });
  await call("POST", `/v1/console/plans/${upper.body.id}/publish`);
  const seeded = await seedActive(api, world, tokens, tenant, plan.id, 1);
  const token = await seeded.consumer(1);
  const subId = (await api.call<{ data: MeSubscription[] }>("GET", "/v1/me/subscriptions", { token })).body.data[0]!.id;

  await api.call("PUT", `/v1/me/subscriptions/${subId}/scheduled-plan-change`, { token, body: { plan_id: upper.body.id } });
  const canceled = await api.call<MeSubscription>("PUT", `/v1/me/subscriptions/${subId}/scheduled-cancellation`, { token, body: {} });
  expect(canceled.body.scheduled_plan_change).toBeNull();
  expect(canceled.body.scheduled_cancellation).not.toBeNull();

  const res = await api.call("PUT", `/v1/me/subscriptions/${subId}/scheduled-plan-change`, { token, body: { plan_id: upper.body.id } });
  expect(res.status).toBe(409);
  expect(res.problem?.code).toBe("cancellation_scheduled");
  // 事業者停止によるシステムの解約予約の取り消し（AC-SUB-029）は、tenant.suspended を購読する orchestration の実装後
});
