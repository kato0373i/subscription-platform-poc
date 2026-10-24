// catalog.md §7.1（console でのテナント分離）：AC-CAT-034
import { test, expect } from "../../fixtures/index.ts";
import { expectIsolated } from "../../helpers/isolation.ts";
import { consoleOf, createCoupon, createPlan } from "../../helpers/catalog.ts";

test(
  "AC-CAT-034: 他の事業者の plan・product・coupon は GET・PATCH・publish などすべて 404",
  {
    tag: [
      "@isolation:consoleGetProduct",
      "@isolation:consoleUpdateProduct",
      "@isolation:consoleGetPlan",
      "@isolation:consoleUpdatePlan",
      "@isolation:consoleAddPriceVersion",
      "@isolation:consoleListPriceVersions",
      "@isolation:consolePublishPlan",
      "@isolation:consoleUnpublishPlan",
      "@isolation:consoleGetCoupon",
      "@isolation:consoleUpdateCoupon",
    ],
  },
  async ({ api, world }) => {
    const a = await world.createTenant();
    const b = await world.createTenant();
    const plan = await createPlan(api, b);
    const coupon = await createCoupon(api, b);
    const asB = consoleOf(api, b);
    const asA = consoleOf(api, a);
    const both = (method: string, path: string, body?: unknown) => expectIsolated((as) => (as === "owner" ? asB(method, path, body) : asA(method, path, body)));

    await both("GET", `/v1/console/products/${plan.product_id}`);
    await both("PATCH", `/v1/console/products/${plan.product_id}`, { description: "x" });
    await both("GET", `/v1/console/plans/${plan.id}`);
    await both("PATCH", `/v1/console/plans/${plan.id}`, { trial_days: 3 });
    await both("GET", `/v1/console/plans/${plan.id}/price-versions`);
    await both("GET", `/v1/console/coupons/${coupon.id}`);
    await both("PATCH", `/v1/console/coupons/${coupon.id}`, { name: "変更" });
    // 状態を変える操作は、先に他の事業者で試して 404、その後に自分で成功させる
    for (const [path, body] of [
      [`/v1/console/plans/${plan.id}/unpublish`, undefined],
      [`/v1/console/plans/${plan.id}/publish`, undefined],
      [`/v1/console/plans/${plan.id}/price-versions`, { amount: 1234 }],
    ] as const) {
      const other = await asA("POST", path, body);
      expect(other.status, path).toBe(404);
      expect(other.problem?.code).toBe("not_found");
      expect((await asB("POST", path, body)).status, path).toBeLessThan(300);
    }
  },
);
