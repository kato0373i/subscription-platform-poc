// catalog.md §7.5（クーポンの検証とプレビュー）：AC-CAT-017、AC-CAT-019 の時刻を進めない部分
import { test, expect } from "../../fixtures/index.ts";
import { consoleOf, createCoupon, createPlan, type CouponPreview } from "../../helpers/catalog.ts";

test("AC-CAT-017: 980 円のプランに「初回 50%」は 490 円引き・初回 490 円・1 回", async ({ api, world }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const c = await createCoupon(api, tenant, { type: "percent", value: 50, cycles: 1 });
  const res = await api.call<CouponPreview>("GET", `/v1/checkout/plans/${plan.id}/coupons/${c.code.toLowerCase()}`);
  expect(res.status, JSON.stringify(res.body)).toBe(200);
  expect(res.body).toEqual({
    code: c.code,
    discount: { type: "percent", value: 50, duration_cycles: 1 },
    preview: { list_amount: 980, discount_amount: 490, first_amount: 490, discounted_cycles: 1, applies_from: "first_charge" },
  });
  expect(res.headers["cache-control"]).toBe("no-store");
});

test("AC-CAT-019（一部）: 対象外のプランは coupon_not_applicable、無効化・存在しないコードは coupon_not_found", async ({ api, world, db }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant);
  const other = await createPlan(api, tenant);
  const selected = await createCoupon(api, tenant, { planIds: [other.id] });
  const disabled = await createCoupon(api, tenant);
  await consoleOf(api, tenant)("PATCH", `/v1/console/coupons/${disabled.id}`, { status: "inactive" });

  for (const [code, want] of [
    [selected.code, "coupon_not_applicable"],
    [disabled.code, "coupon_not_found"],
    ["NOSUCHCODE", "coupon_not_found"],
  ] as const) {
    const res = await api.call("GET", `/v1/checkout/plans/${plan.id}/coupons/${code}`);
    expect(res.status, code).toBe(422);
    expect(res.problem?.code).toBe(want);
  }
  expect(await db.count("SELECT coalesce(sum(redeemed_count), 0) FROM catalog.coupons WHERE tenant_id = $1", [tenant.id])).toBe(0);
  // 上限到達（coupon_exhausted）と申込 API での確認は orchestration の実装後
});

test("ログイン状態のプレビュー：有効なトークンなら通り、不正なトークンは 401。非公開のプランは 410", async ({ api, world }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant);
  const c = await createCoupon(api, tenant);
  const consumer = await world.createConsumer();
  const ok = await api.call("GET", `/v1/checkout/plans/${plan.id}/coupons/${c.code}`, { token: consumer.token });
  expect(ok.status).toBe(200);
  const bad = await api.call("GET", `/v1/checkout/plans/${plan.id}/coupons/${c.code}`, { token: "not-a-jwt" });
  expect(bad.status).toBe(401);
  // 使用済み（coupon_already_used）のログイン状態のプレビューは、申込で確定させる必要があるので orchestration の実装後（AC-CAT-020）

  await consoleOf(api, tenant)("POST", `/v1/console/plans/${plan.id}/unpublish`);
  const gone = await api.call("GET", `/v1/checkout/plans/${plan.id}/coupons/${c.code}`);
  expect(gone.status).toBe(410);
});
