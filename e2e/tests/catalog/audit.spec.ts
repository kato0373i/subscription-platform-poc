// catalog.md §8（操作者の記録）：AC-CAT-032 のうち EventEnvelope の actor
import { test, expect } from "../../fixtures/index.ts";
import { consoleOf, createPlan, type PriceVersion } from "../../helpers/catalog.ts";

test("AC-CAT-032（actor）: admin の価格改定のイベントは actor = {staff, 操作者}", async ({ api, world, db }) => {
  const tenant = await world.createTenant();
  const admin = await world.createStaff(tenant, "admin");
  const plan = await createPlan(api, tenant, { amount: 980 });
  const res = await consoleOf(api, tenant, admin.token)<PriceVersion>("POST", `/v1/console/plans/${plan.id}/price-versions`, { amount: 1280 });
  expect(res.status).toBe(201);
  const rows = await db.query<{ actor: { type: string; id: string }; payload: { amount: number } }>(
    "SELECT actor, payload FROM catalog.outbox_events WHERE aggregate_id = $1 AND event_type = 'catalog.price_version_added' AND payload->>'version_no' = '2'",
    [plan.id],
  );
  expect(rows).toHaveLength(1);
  expect(rows[0]!.actor).toEqual({ type: "staff", id: admin.sub });
  expect(rows[0]!.payload.amount).toBe(1280);
  // 監査記録（plan.price_version_add、改定前後の amount 980→1,280）の DB 確認は audit モジュールの実装後
});
