// subscription.md §7（マイページの契約）：AC-SUB-035 のうち自分の契約の一覧・他人の契約の 404
import { test, expect } from "../../fixtures/index.ts";
import { createPlan } from "../../helpers/catalog.ts";
import { expectIsolated } from "../../helpers/isolation.ts";
import { seedActive, type MeSubscription } from "../../helpers/subscription.ts";

test("自分の契約だけが一覧に出て、詳細は他人からは 404", { tag: "@isolation:meGetSubscription" }, async ({ api, world, tokens }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const seeded = await seedActive(api, world, tokens, tenant, plan.id, 2);
  const [mine, others] = [await seeded.consumer(1), await seeded.consumer(2)];

  const list = await api.call<{ data: MeSubscription[]; has_more: boolean }>("GET", "/v1/me/subscriptions", { token: mine });
  expect(list.status, JSON.stringify(list.body)).toBe(200);
  expect(list.body.data).toHaveLength(1);
  const sub = list.body.data[0]!;
  expect(sub).toMatchObject({
    tenant_id: tenant.id,
    plan_id: plan.id,
    status: "active",
    entitled: true,
    interval: "month",
    quantity: 1,
    next_renewal_date: seeded.firstRenewalDate,
    scheduled_plan_change: null,
    scheduled_cancellation: null,
    renewal_in_progress: false,
    applied_discount: null,
  });
  expect(sub).not.toHaveProperty("customer_id");
  expect(sub.current_period.start < sub.current_period.end).toBe(true);

  await expectIsolated((as) => api.call("GET", `/v1/me/subscriptions/${sub.id}`, { token: as === "owner" ? mine : others }));
  const filtered = await api.call<{ data: MeSubscription[] }>("GET", "/v1/me/subscriptions", { token: mine, query: { status: "canceled" } });
  expect(filtered.body.data).toEqual([]);
});
