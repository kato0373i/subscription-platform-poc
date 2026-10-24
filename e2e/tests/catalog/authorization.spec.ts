// catalog.md §7.1（console の認可）：AC-CAT-012、identity の AC-IDN-009・010 の残り
import { test, expect } from "../../fixtures/index.ts";
import { consoleOf, createPlan, type Plan } from "../../helpers/catalog.ts";

test("AC-CAT-012・AC-IDN-009: viewer は価格改定・公開・プランの変更・クーポンの作成ができず、GET はできる", async ({ api, world, db }) => {
  const tenant = await world.createTenant();
  const viewer = await world.createStaff(tenant, "viewer");
  const plan = await createPlan(api, tenant, { publish: false });
  const asViewer = consoleOf(api, tenant, viewer.token);

  for (const [method, path, body] of [
    ["POST", `/v1/console/plans/${plan.id}/price-versions`, { amount: 1280 }],
    ["POST", `/v1/console/plans/${plan.id}/publish`, undefined],
    ["PATCH", `/v1/console/plans/${plan.id}`, { name: "変更" }],
    ["POST", "/v1/console/coupons", { code: "VIEWER", name: "x", discount: { type: "percent", value: 10, duration_cycles: 1 }, applies_to: "all_plans" }],
  ] as const) {
    const res = await asViewer(method, path, body);
    expect(res.status, `${method} ${path}`).toBe(403);
    expect(res.problem?.code).toBe("forbidden");
  }
  expect((await asViewer<Plan>("GET", `/v1/console/plans/${plan.id}`)).body).toMatchObject({ name: "月額プラン", status: "draft" });
  expect((await asViewer("GET", "/v1/console/coupons")).status).toBe(200);
  expect(await db.count("SELECT count(*) FROM catalog.price_versions WHERE plan_id = $1", [plan.id])).toBe(1);
});

test("AC-IDN-010（catalog.write）: admin はプランの作成・公開・価格改定ができる", async ({ api, world }) => {
  const tenant = await world.createTenant();
  const admin = await world.createStaff(tenant, "admin");
  const plan = await createPlan(api, tenant, { token: admin.token });
  expect(plan.status).toBe("published");
  const res = await consoleOf(api, tenant, admin.token)("POST", `/v1/console/plans/${plan.id}/price-versions`, { amount: 1500 });
  expect(res.status).toBe(201);
});

test("停止中の事業者は閲覧できるが、変更は tenant_not_active", async ({ api, tokens, world }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant);
  await api.call("POST", `/v1/ops/tenants/${tenant.id}/suspend`, { token: await tokens.operator(), body: { reason: "停止" } });
  const call = consoleOf(api, tenant);
  expect((await call("GET", `/v1/console/plans/${plan.id}`)).status).toBe(200);
  const res = await call("POST", `/v1/console/plans/${plan.id}/price-versions`, { amount: 1500 });
  expect(res.status).toBe(403);
  expect(res.problem?.code).toBe("tenant_not_active");
});
