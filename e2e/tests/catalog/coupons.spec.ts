// catalog.md §7.4（クーポンの作成・無効化）：AC-CAT-016
import { test, expect } from "../../fixtures/index.ts";
import { businessNow, consoleOf, createCoupon, createPlan, type Coupon, type List } from "../../helpers/catalog.ts";

test("AC-CAT-016: コードは大文字に正規化され、同じ事業者では重複できない。別の事業者なら作れる", async ({ api, world }) => {
  const a = await world.createTenant();
  const b = await world.createTenant();
  const created = await createCoupon(api, a, { code: "WELCOME" });
  expect(created).toMatchObject({ code: "WELCOME", status: "active", redeemed_count: 0, applies_to: "all_plans", plan_ids: [] });
  const dup = await consoleOf(api, a)("POST", "/v1/console/coupons", {
    code: "welcome",
    name: "x",
    discount: { type: "percent", value: 10, duration_cycles: 1 },
    applies_to: "all_plans",
  });
  expect(dup.status).toBe(409);
  expect(dup.problem?.code).toBe("coupon_code_taken");
  expect((await createCoupon(api, b, { code: "welcome" })).code).toBe("WELCOME");
});

test("対象プラン・期限・上限つきで作れ、他の事業者のプランは指定できない", async ({ api, world }) => {
  const a = await world.createTenant();
  const b = await world.createTenant();
  const plan = await createPlan(api, a);
  const planOfB = await createPlan(api, b);
  const expires = new Date((await businessNow(api)).getTime() + 7 * 86_400_000).toISOString();
  const c = await createCoupon(api, a, { planIds: [plan.id], expiresAt: expires, max: 100, type: "amount", value: 300, cycles: 3 });
  expect(c).toMatchObject({ applies_to: "selected_plans", plan_ids: [plan.id], max_redemptions: 100, discount: { type: "amount", value: 300, duration_cycles: 3 } });
  expect(new Date(c.expires_at!).toISOString()).toBe(expires);

  const res = await consoleOf(api, a)("POST", "/v1/console/coupons", {
    code: "OTHERPLAN",
    name: "x",
    discount: { type: "percent", value: 10, duration_cycles: 1 },
    applies_to: "selected_plans",
    plan_ids: [planOfB.id],
  });
  expect(res.status).toBe(404);
});

test("検証の誤り（過去の期限・空の対象プラン・範囲外の割引）は 400", async ({ api, world }) => {
  const tenant = await world.createTenant();
  const past = new Date((await businessNow(api)).getTime() - 60_000).toISOString();
  const base = { code: "BAD1", name: "x", discount: { type: "percent", value: 10, duration_cycles: 1 }, applies_to: "all_plans" };
  for (const [field, body] of [
    ["expires_at", { ...base, expires_at: past }],
    ["plan_ids", { ...base, applies_to: "selected_plans" }],
    ["discount.value", { ...base, discount: { type: "percent", value: 101, duration_cycles: 1 } }],
    ["discount.duration_cycles", { ...base, discount: { type: "amount", value: 100, duration_cycles: 25 } }],
  ] as const) {
    const res = await consoleOf(api, tenant)("POST", "/v1/console/coupons", body);
    expect(res.status, field).toBe(400);
    expect(res.problem?.errors?.[0]?.field).toBe(field);
  }
});

test("無効化は戻せず、割引条件は変更できない", async ({ api, world }) => {
  const tenant = await world.createTenant();
  const c = await createCoupon(api, tenant);
  const call = consoleOf(api, tenant);
  const off = await call<Coupon>("PATCH", `/v1/console/coupons/${c.id}`, { status: "inactive" });
  expect(off.body.status).toBe("inactive");
  expect((await call("PATCH", `/v1/console/coupons/${c.id}`, { status: "active" })).status).toBe(400);
  expect((await call("PATCH", `/v1/console/coupons/${c.id}`, { discount: { type: "percent", value: 1, duration_cycles: 1 } })).status).toBe(400);
  const list = await call<List<Coupon>>("GET", "/v1/console/coupons", undefined, { status: "inactive" });
  expect(list.body.data.map((x) => x.id)).toEqual([c.id]);
});
