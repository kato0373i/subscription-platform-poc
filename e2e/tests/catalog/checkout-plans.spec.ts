// catalog.md §7.5（申込ページ用の公開 API）：AC-CAT-009（公開 API の部分）・010・011
import { test, expect } from "../../fixtures/index.ts";
import { consoleOf, createPlan, type CheckoutPlan } from "../../helpers/catalog.ts";

test("公開中のプランは認証なしで取れ、60 秒キャッシュできる", async ({ api, world }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 1980, trialDays: 7, interval: "year" });
  const res = await api.call<CheckoutPlan>("GET", `/v1/checkout/plans/${plan.id}`);
  expect(res.status, JSON.stringify(res.body)).toBe(200);
  expect(res.body).toMatchObject({
    plan_id: plan.id,
    product: { id: plan.product_id, name: "商品" },
    billing_interval: "year",
    trial_days: 7,
    amount: 1980,
    tax_rate: "0.100",
    price_version_id: plan.current_price.price_version_id,
  });
  expect(res.headers["cache-control"]).toBe("public, max-age=60");
});

test("AC-CAT-009: 非公開にしたプランは 410 plan_unavailable", async ({ api, world }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant);
  await consoleOf(api, tenant)("POST", `/v1/console/plans/${plan.id}/unpublish`);
  const res = await api.call("GET", `/v1/checkout/plans/${plan.id}`);
  expect(res.status).toBe(410);
  expect(res.problem?.code).toBe("plan_unavailable");
  expect(res.problem?.detail).toBe("現在お申し込みいただけません");
  // 申込 API の 422 plan_unavailable は orchestration の実装後（docs/poc/impl/catalog.md）
});

test("AC-CAT-010: 事業者を停止すると、公開中のプランも 410", async ({ api, tokens, world }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant);
  await api.call("POST", `/v1/ops/tenants/${tenant.id}/suspend`, { token: await tokens.operator(), body: { reason: "停止" } });
  const res = await api.call("GET", `/v1/checkout/plans/${plan.id}`);
  expect(res.status).toBe(410);
  expect(res.problem?.code).toBe("plan_unavailable");
});

test("AC-CAT-011: draft のプランと存在しないプランは 404", async ({ api, world }) => {
  const tenant = await world.createTenant();
  const draft = await createPlan(api, tenant, { publish: false });
  for (const id of [draft.id, crypto.randomUUID()]) {
    const res = await api.call("GET", `/v1/checkout/plans/${id}`);
    expect(res.status).toBe(404);
    expect(res.problem?.code).toBe("not_found");
  }
});
