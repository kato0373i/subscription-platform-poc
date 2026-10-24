// catalog.md §7.2・§7.3（商品・プラン・価格改定・公開）：AC-CAT-001・003・004・006・007
import { test, expect } from "../../fixtures/index.ts";
import { consoleOf, createPlan, createProduct, type CheckoutPlan, type List, type Plan, type PriceVersion, type Product } from "../../helpers/catalog.ts";

test("AC-CAT-001: admin が 980 円・月額・トライアル 14 日のプランを作ると draft・v1 で、price_version_added が出る", async ({ api, world, db }) => {
  const tenant = await world.createTenant();
  const admin = await world.createStaff(tenant, "admin");
  const product = await createProduct(api, tenant);
  const res = await consoleOf(api, tenant, admin.token)<Plan>("POST", "/v1/console/plans", {
    product_id: product.id,
    name: "スタンダード",
    billing_interval: "month",
    trial_days: 14,
    amount: 980,
  });
  expect(res.status, JSON.stringify(res.body)).toBe(201);
  expect(res.body).toMatchObject({ status: "draft", pricing_scheme: "fixed", trial_days: 14, published_at: null });
  expect(res.body.current_price).toMatchObject({ version_no: 1, amount: 980, tax_rate: "0.100" });
  const events = await db.query<{ payload: { version_no: number; amount: number } }>(
    "SELECT payload FROM catalog.outbox_events WHERE aggregate_id = $1 AND event_type = 'catalog.price_version_added'",
    [res.body.id],
  );
  expect(events).toHaveLength(1);
  expect(events[0]!.payload).toMatchObject({ version_no: 1, amount: 980 });
});

test("金額の範囲外は 400（out_of_range と許容範囲）", async ({ api, world }) => {
  const tenant = await world.createTenant();
  const product = await createProduct(api, tenant);
  for (const amount of [0, 1_000_001]) {
    const res = await consoleOf(api, tenant)("POST", "/v1/console/plans", { product_id: product.id, name: "p", billing_interval: "month", trial_days: 0, amount });
    expect(res.status).toBe(400);
    expect(res.problem?.errors?.[0]).toEqual({ field: "amount", code: "out_of_range" });
    expect(res.problem?.detail).toContain("1000000");
  }
});

test("AC-CAT-003: 別の事業者の商品ではプランを作れない（404）", async ({ api, world }) => {
  const a = await world.createTenant();
  const b = await world.createTenant();
  const productOfB = await createProduct(api, b);
  const res = await consoleOf(api, a)("POST", "/v1/console/plans", { product_id: productOfB.id, name: "p", billing_interval: "month", trial_days: 0, amount: 980 });
  expect(res.status).toBe(404);
  expect(res.problem?.code).toBe("not_found");
});

test("AC-CAT-004: 審査中の事業者は公開できない（tenant_not_active）", async ({ api, world }) => {
  const tenant = await world.createTenant({ approve: false });
  const plan = await createPlan(api, tenant, { publish: false });
  const res = await consoleOf(api, tenant)("POST", `/v1/console/plans/${plan.id}/publish`);
  expect(res.status).toBe(403);
  expect(res.problem?.code).toBe("tenant_not_active");
  expect(res.problem?.detail).toBe("審査完了後に公開できます");
  const got = await consoleOf(api, tenant)<Plan>("GET", `/v1/console/plans/${plan.id}`);
  expect(got.body.status).toBe("draft");
});

test("AC-CAT-006: 980 円を 1,280 円に改定すると v2 になり、申込ページは v2 を返し、一覧に v1 が残る", async ({ api, world, db }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const call = consoleOf(api, tenant);
  const res = await call<PriceVersion>("POST", `/v1/console/plans/${plan.id}/price-versions`, { amount: 1280 });
  expect(res.status, JSON.stringify(res.body)).toBe(201);
  expect(res.body).toMatchObject({ version_no: 2, amount: 1280, tax_rate: "0.100" });

  const checkout = await api.call<CheckoutPlan>("GET", `/v1/checkout/plans/${plan.id}`);
  expect(checkout.body).toMatchObject({ amount: 1280, price_version_id: res.body.price_version_id });
  const versions = await call<List<PriceVersion>>("GET", `/v1/console/plans/${plan.id}/price-versions`, undefined, { limit: 1 });
  expect(versions.body.data.map((v) => v.version_no)).toEqual([2]);
  const older = await call<List<PriceVersion>>("GET", `/v1/console/plans/${plan.id}/price-versions`, undefined, { starting_after: versions.body.next_cursor! });
  expect(older.body.data).toEqual([expect.objectContaining({ version_no: 1, amount: 980 })]);
  expect(await db.count("SELECT count(*) FROM catalog.outbox_events WHERE aggregate_id = $1 AND event_type = 'catalog.price_version_added'", [plan.id])).toBe(2);
});

test("AC-CAT-007: 同じ金額での改定は 422 price_unchanged で、版は増えない", async ({ api, world, db }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const res = await consoleOf(api, tenant)("POST", `/v1/console/plans/${plan.id}/price-versions`, { amount: 980 });
  expect(res.status).toBe(422);
  expect(res.problem?.code).toBe("price_unchanged");
  expect(await db.count("SELECT count(*) FROM catalog.price_versions WHERE plan_id = $1", [plan.id])).toBe(1);
});

test("公開・非公開は冪等で、draft の非公開は 409。PATCH で interval・amount は変えられない", async ({ api, world, db }) => {
  const tenant = await world.createTenant();
  const call = consoleOf(api, tenant);
  const draft = await createPlan(api, tenant, { publish: false });
  const un = await call("POST", `/v1/console/plans/${draft.id}/unpublish`);
  expect(un.status).toBe(409);
  expect(un.problem?.code).toBe("conflict");

  const plan = await createPlan(api, tenant);
  expect((await call<Plan>("POST", `/v1/console/plans/${plan.id}/publish`)).body.status).toBe("published");
  expect((await call<Plan>("POST", `/v1/console/plans/${plan.id}/unpublish`)).body.status).toBe("unpublished");
  expect((await call<Plan>("POST", `/v1/console/plans/${plan.id}/unpublish`)).status).toBe(200);
  const events = await db.query<{ event_type: string }>(
    "SELECT event_type FROM catalog.outbox_events WHERE aggregate_id = $1 AND event_type IN ('catalog.plan_published', 'catalog.plan_unpublished') ORDER BY occurred_at",
    [plan.id],
  );
  expect(events.map((e) => e.event_type)).toEqual(["catalog.plan_published", "catalog.plan_unpublished"]);

  for (const body of [{ billing_interval: "year" }, { amount: 1 }, { pricing_scheme: "fixed" }]) {
    const res = await call("PATCH", `/v1/console/plans/${plan.id}`, body);
    expect(res.status, JSON.stringify(body)).toBe(400);
  }
  const patched = await call<Plan>("PATCH", `/v1/console/plans/${plan.id}`, { name: "新名称", trial_days: 30 });
  expect(patched.body).toMatchObject({ name: "新名称", trial_days: 30 });
});

test("商品の更新と一覧", async ({ api, world }) => {
  const tenant = await world.createTenant();
  const call = consoleOf(api, tenant);
  const a = await createProduct(api, tenant, "A");
  const b = await createProduct(api, tenant, "B");
  const patched = await call<Product>("PATCH", `/v1/console/products/${a.id}`, { description: "説明" });
  expect(patched.body).toMatchObject({ name: "A", description: "説明" });
  const list = await call<List<Product>>("GET", "/v1/console/products");
  expect(list.body.data.map((p) => p.id)).toEqual([a.id, b.id]);
  const plans = await call<List<Plan>>("GET", "/v1/console/plans", undefined, { product_id: a.id });
  expect(plans.body.data).toEqual([]);
});
