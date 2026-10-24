// subscription.md §7.2（プラン変更の予約）：AC-SUB-022、予約の適用日、取り消し
import { test, expect } from "../../fixtures/index.ts";
import { consoleOf, createPlan, createProduct, type Plan } from "../../helpers/catalog.ts";
import { seedActive, type MeSubscription } from "../../helpers/subscription.ts";

test(
  "同じ商品の別プランへの予約は次の更新日から。取り消せる。他人の契約は 404",
  { tag: ["@isolation:meSchedulePlanChange", "@isolation:meRevokePlanChange"] },
  async ({ api, world, tokens, db }) => {
    const tenant = await world.createTenant();
    const plan = await createPlan(api, tenant, { amount: 980 });
    const call = consoleOf(api, tenant);
    // 同じ商品の上位プラン
    const upper = await call<Plan>("POST", "/v1/console/plans", { product_id: plan.product_id, name: "上位", billing_interval: "month", trial_days: 0, amount: 1980 });
    await call("POST", `/v1/console/plans/${upper.body.id}/publish`);
    const seeded = await seedActive(api, world, tokens, tenant, plan.id, 2);
    const [mine, others] = [await seeded.consumer(1), await seeded.consumer(2)];
    const subId = (await api.call<{ data: MeSubscription[] }>("GET", "/v1/me/subscriptions", { token: mine })).body.data[0]!.id;

    const cross = await api.call("PUT", `/v1/me/subscriptions/${subId}/scheduled-plan-change`, { token: others, body: { plan_id: upper.body.id } });
    expect(cross.status).toBe(404);
    expect((await api.call("DELETE", `/v1/me/subscriptions/${subId}/scheduled-plan-change`, { token: others })).status).toBe(404);

    const res = await api.call<MeSubscription>("PUT", `/v1/me/subscriptions/${subId}/scheduled-plan-change`, { token: mine, body: { plan_id: upper.body.id } });
    expect(res.status, JSON.stringify(res.body)).toBe(200);
    expect(res.body.plan_id).toBe(plan.id);
    expect(res.body.scheduled_plan_change).toEqual({ plan_id: upper.body.id, effective_date: seeded.firstRenewalDate });
    // 同じ内容の再実行は 200 で、イベントは増えない
    expect((await api.call("PUT", `/v1/me/subscriptions/${subId}/scheduled-plan-change`, { token: mine, body: { plan_id: upper.body.id } })).status).toBe(200);
    expect(await db.count("SELECT count(*) FROM subscription.outbox_events WHERE aggregate_id = $1 AND event_type = 'subscription.plan_change_scheduled'", [subId])).toBe(1);

    const revoked = await api.call<MeSubscription>("DELETE", `/v1/me/subscriptions/${subId}/scheduled-plan-change`, { token: mine });
    expect(revoked.status).toBe(200);
    expect(revoked.body.scheduled_plan_change).toBeNull();
    expect((await api.call("DELETE", `/v1/me/subscriptions/${subId}/scheduled-plan-change`, { token: mine })).status).toBe(200);
  },
);

test("AC-SUB-022: 別の商品・非公開・現在と同じプランへは 422 plan_change_not_allowed", async ({ api, world, tokens }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const call = consoleOf(api, tenant);
  const otherProduct = await createPlan(api, tenant, { amount: 500 });
  const hidden = await call<Plan>("POST", "/v1/console/plans", { product_id: plan.product_id, name: "非公開", billing_interval: "month", trial_days: 0, amount: 2980 });
  const seeded = await seedActive(api, world, tokens, tenant, plan.id, 1);
  const token = await seeded.consumer(1);
  const subId = (await api.call<{ data: MeSubscription[] }>("GET", "/v1/me/subscriptions", { token })).body.data[0]!.id;

  for (const [planId, reason] of [
    [otherProduct.id, "different_product"],
    [hidden.body.id, "plan_unavailable"],
    [crypto.randomUUID(), "plan_unavailable"],
    [plan.id, "same_plan"],
  ] as const) {
    const res = await api.call("PUT", `/v1/me/subscriptions/${subId}/scheduled-plan-change`, { token, body: { plan_id: planId } });
    expect(res.status, reason).toBe(422);
    expect(res.problem?.code).toBe("plan_change_not_allowed");
    expect(res.problem?.errors?.[0]).toEqual({ field: "plan_id", code: reason });
  }
  await createProduct(api, tenant); // 別の商品があっても影響しない
});
