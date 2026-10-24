// subscription.md §7.2（console の契約の照会）：AC-SUB-034 のうち一覧の絞り込み、テナント分離
import { test, expect } from "../../fixtures/index.ts";
import { consoleOf, createPlan } from "../../helpers/catalog.ts";
import { expectIsolated } from "../../helpers/isolation.ts";
import { seedActive, type Subscription } from "../../helpers/subscription.ts";

test("console は自テナントの契約を status・plan_id・customer_id で絞り込める。他テナントの契約は 404", { tag: "@isolation:consoleGetSubscription" }, async ({ api, world, tokens }) => {
  const a = await world.createTenant();
  const b = await world.createTenant();
  const planA = await createPlan(api, a, { amount: 980 });
  const planA2 = await createPlan(api, a, { amount: 1980 });
  const seededA = await seedActive(api, world, tokens, a, planA.id, 2);
  await seedActive(api, world, tokens, a, planA2.id, 1);
  const planB = await createPlan(api, b);
  await seedActive(api, world, tokens, b, planB.id, 1);

  const all = await seededA.subscriptions();
  expect(all).toHaveLength(3);
  for (const s of all) {
    expect(s.customer_id).toMatch(/^cus_[0-9A-Za-z]{24}$/);
    expect(s).not.toHaveProperty("payment_method_id");
    expect(s).not.toHaveProperty("tenant_id");
  }
  const call = consoleOf(api, a);
  const byPlan = await call<{ data: Subscription[] }>("GET", "/v1/console/subscriptions", undefined, { plan_id: planA.id });
  expect(byPlan.body.data).toHaveLength(2);
  const byCustomer = await call<{ data: Subscription[] }>("GET", "/v1/console/subscriptions", undefined, { customer_id: all[0]!.customer_id });
  expect(byCustomer.body.data.map((s) => s.id)).toEqual([all[0]!.id]);
  const pastDue = await call<{ data: Subscription[] }>("GET", "/v1/console/subscriptions", undefined, { status: "past_due" });
  expect(pastDue.body.data).toEqual([]);
  const page = await call<{ data: Subscription[]; has_more: boolean; next_cursor: string }>("GET", "/v1/console/subscriptions", undefined, { limit: 2 });
  expect(page.body.has_more).toBe(true);
  const rest = await call<{ data: Subscription[] }>("GET", "/v1/console/subscriptions", undefined, { starting_after: page.body.next_cursor });
  expect(rest.body.data).toHaveLength(1);

  await expectIsolated((as) => consoleOf(api, as === "owner" ? a : b)("GET", `/v1/console/subscriptions/${all[0]!.id}`));
  // past_due・canceled を混ぜた絞り込み（AC-SUB-034 の残り）は、更新・決済を通す orchestration の実装後
});
