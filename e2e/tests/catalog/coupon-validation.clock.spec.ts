// catalog.md §3.4（期限切れのクーポン）：AC-CAT-019 の期限切れ。dev clock を進めるので clock project で流す
import { test, expect } from "../../fixtures/index.ts";
import { createCoupon, createPlan } from "../../helpers/catalog.ts";

test("AC-CAT-019（期限切れ）: 期限を過ぎたクーポンは coupon_expired（ちょうどの時刻も期限切れ）", async ({ api, world, clock, db }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant);
  const now = new Date((await clock.now()).now);
  const expires = new Date(now.getTime() + 3600_000);
  const c = await createCoupon(api, tenant, { expiresAt: expires.toISOString() });
  expect((await api.call("GET", `/v1/checkout/plans/${plan.id}/coupons/${c.code}`)).status).toBe(200);

  await clock.setTo(expires);
  const res = await api.call("GET", `/v1/checkout/plans/${plan.id}/coupons/${c.code}`);
  expect(res.status).toBe(422);
  expect(res.problem?.code).toBe("coupon_expired");
  expect(await db.count("SELECT redeemed_count FROM catalog.coupons WHERE id = $1", [c.id])).toBe(0);
});
