// 申込でのクーポンの予約・確定・解放（catalog.md §11、orchestration.md §4.1 手順 6）：AC-CAT-024・025・031。
import { test, expect } from "../../fixtures/index.ts";
import { createCoupon, createPlan } from "../../helpers/catalog.ts";
import { checkout, type Consumer } from "../../helpers/orchestration.ts";

test("AC-CAT-024・025: 拒否された申込のクーポンは解放され、同じ契約者が成功するカードで再申込すると割引付きで成立する", async ({ api, world, db }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const coupon = await createCoupon(api, tenant, { value: 50, max: 10 });
  const consumer = (await world.createConsumer()) as Consumer;
  const bad = await world.createPaymentMethod(consumer, "decline");
  const declined = await checkout(api, consumer, tenant, plan.id, bad.id, { coupon: coupon.code });
  expect(declined.problem?.code).toBe("payment_declined");
  expect(await db.count("SELECT count(*) FROM catalog.coupon_redemptions WHERE coupon_id = $1 AND status = 'released'", [coupon.id])).toBe(1);
  const good = await world.createPaymentMethod(consumer, "ok");
  const ok = await checkout(api, consumer, tenant, plan.id, good.id, { coupon: coupon.code });
  expect(ok.status, JSON.stringify(ok.body)).toBe(201);
  expect(ok.body.invoice).toMatchObject({ total: 490, status: "paid" });
});

test("AC-CAT-031: 残り 1 枚のクーポンに 2 人が同時に申し込むと、1 人だけ割引付きで成立し、もう 1 人は 422 coupon_exhausted", async ({ api, world, db }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const coupon = await createCoupon(api, tenant, { value: 50, max: 1 });
  const people = await Promise.all([0, 1].map(async () => {
    const c = (await world.createConsumer()) as Consumer;
    return { c, pm: await world.createPaymentMethod(c, "ok") };
  }));
  const res = await Promise.all(people.map((p) => checkout(api, p.c, tenant, plan.id, p.pm.id, { coupon: coupon.code })));
  expect(res.map((r) => r.status).sort()).toEqual([201, 422]);
  expect(res.find((r) => r.status === 422)?.problem?.code).toBe("coupon_exhausted");
  expect(res.find((r) => r.status === 201)?.body.invoice?.total).toBe(490);
  expect(await db.count("SELECT count(*) FROM catalog.coupon_redemptions WHERE coupon_id = $1 AND status = 'confirmed'", [coupon.id])).toBe(1);
});
