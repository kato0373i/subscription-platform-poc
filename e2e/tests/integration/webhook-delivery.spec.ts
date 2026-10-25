// 配信ループの即時の配信・遅い送信先の隔離（integration.md §8.1）：AC-INT-036・018。
import { test, expect } from "../../fixtures/index.ts";
import { createPlan } from "../../helpers/catalog.ts";
import { bodyOf, sinkEndpoint, untilDelivery } from "../../helpers/integration.ts";
import { subscribe } from "../../helpers/orchestration.ts";

test("AC-INT-036: 外部イベントの作成から 2 秒以内に sink に届く（申込の subscription.started・invoice.paid）", async ({ api, world, sink, db }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const s = await sinkEndpoint(api, sink, tenant);
  const { subscription } = await subscribe(api, world, tenant, plan.id);
  const recs = await sink.waitForRequests(s.sinkId, 2);
  expect(recs.map((r) => bodyOf(r).type).sort()).toEqual(["invoice.paid", "subscription.started"]);
  expect(bodyOf(recs.find((r) => bodyOf(r).type === "subscription.started")!).data.object.id).toBe(subscription.id);
  // 外部イベントの作成（受信処理のコミット）から最初の試行まで（どちらも Clock の時刻）
  const lags = await db.query<{ lag_ms: number }>(
    `SELECT (extract(epoch FROM a.requested_at - e.created_at) * 1000)::int AS lag_ms
       FROM integration.webhook_delivery_attempts a JOIN integration.webhook_deliveries d ON d.id = a.delivery_id
       JOIN integration.webhook_events e ON e.id = d.webhook_event_id
      WHERE d.endpoint_id = $1 AND a.attempt_no = 1`,
    [s.endpoint.id],
  );
  expect(lags).toHaveLength(2);
  for (const l of lags) expect(l.lag_ms).toBeLessThan(2_000);
});

test("AC-INT-018・E10: 応答しない送信先 X は timeout の失敗になり、正常な送信先 Y は X を待たずに届く", async ({ api, world, sink, db, wait }) => {
  test.setTimeout(60_000);
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const x = await sinkEndpoint(api, sink, tenant, ["subscription.started"]);
  await sink.setBehavior(x.sinkId, [{ hang: true }]);
  const y = await sinkEndpoint(api, sink, tenant, ["subscription.started"]);
  await subscribe(api, world, tenant, plan.id);
  const started = Date.now();
  await sink.waitForRequests(y.sinkId, 1);
  expect(Date.now() - started).toBeLessThan(5_000);
  const d = await wait.until(
    async () => (await db.query<{ error_kind: string; duration_ms: number }>(
      "SELECT a.error_kind, a.duration_ms FROM integration.webhook_delivery_attempts a JOIN integration.webhook_deliveries d ON d.id = a.delivery_id WHERE d.endpoint_id = $1",
      [x.endpoint.id],
    ))[0],
    { message: "X の試行が記録される", timeout: 30_000 },
  );
  expect(d.error_kind).toBe("timeout");
  expect(d.duration_ms).toBeGreaterThanOrEqual(9_000);
  await untilDelivery(db, wait, x.endpoint.id, (r) => r.status === "pending" && r.attempt_count === 1, "X は再試行待ち");
});
