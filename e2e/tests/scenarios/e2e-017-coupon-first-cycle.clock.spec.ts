// E2E-017 クーポン（初回 50%）（orchestration.md §11b.3）：AC-BIL-002、FR-005・D-10、AC-CAT-020。
import { test, expect } from "../../fixtures/index.ts";
import { createCoupon, createPlan } from "../../helpers/catalog.ts";
import { JOB, addMonths, checkout, expectInvariants, jst, startAtNext15, subscribe, type Consumer } from "../../helpers/orchestration.ts";

test("E2E-017: 初回 50% のクーポンで初回 490 円、更新は 980 円。同じクーポンは使用済み", async ({ api, world, db, wait, clock }) => {
  const D = await startAtNext15(clock);
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const coupon = await createCoupon(api, tenant, { value: 50, cycles: 1 });
  const { consumer, pm, subscription: sub, invoice } = await subscribe(api, world, tenant, plan.id, "ok", { coupon: coupon.code });
  expect(invoice).toMatchObject({ total: 490, status: "paid" });
  const renewal = addMonths(D, 1);
  await clock.setTo(jst(renewal, "00:30"));
  await clock.runJob(JOB.dispatch, { asOf: renewal, force: true });
  const list = await wait.until(
    async () => {
      const r = await api.call<{ data: { total: number; status: string }[] }>("GET", `/v1/me/subscriptions/${sub.id}/invoices`, { token: consumer.token });
      return r.body.data.length === 2 && r.body.data[0]!.status === "paid" ? r.body.data : undefined;
    },
    { message: "更新の請求" },
  );
  expect(list.map((i) => i.total)).toEqual([980, 490]);
  const again = await checkout(api, consumer as Consumer, tenant, plan.id, pm.id, { coupon: coupon.code });
  expect(again.problem?.code).toBe("coupon_already_used");
  expect(await db.count("SELECT count(*) FROM billing.invoice_lines WHERE invoice_id = $1 AND kind = 'discount' AND amount = 490", [invoice!.id])).toBe(1);
  expect(await db.count("SELECT count(*) FROM billing.invoice_lines l JOIN billing.invoices i ON i.id = l.invoice_id WHERE i.subscription_id = $1 AND i.kind = 'renewal' AND l.kind = 'discount'", [sub.id])).toBe(0);
  expect(await db.count("SELECT count(*) FROM catalog.coupon_redemptions WHERE coupon_id = $1 AND status = 'confirmed'", [coupon.id])).toBe(1);
  await expectInvariants(db, wait, tenant.id);
});
