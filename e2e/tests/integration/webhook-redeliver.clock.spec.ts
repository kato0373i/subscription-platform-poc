// 打ち切られた配信の手動再送（integration.md §7.1・§7.2）：AC-INT-014、NFR-014。
import { test, expect } from "../../fixtures/index.ts";
import { consoleOf, createPlan } from "../../helpers/catalog.ts";
import { bodyOf, type Delivery, header, sinkEndpoint, untilDelivery } from "../../helpers/integration.ts";
import { startAtNext15, subscribe } from "../../helpers/orchestration.ts";

test("AC-INT-014: failed の配信を owner が再送すると、manual の新しい配信が同じ evt_ の ID で届く。元の配信は failed のまま。ops からも再送できる", async ({ api, world, sink, clock, db, wait, tokens }) => {
  await startAtNext15(clock);
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const s = await sinkEndpoint(api, sink, tenant, ["subscription.started"]);
  await sink.setBehavior(s.sinkId, [{ status: 500 }]);
  await subscribe(api, world, tenant, plan.id);
  const [first] = await sink.waitForRequests(s.sinkId, 1);
  await untilDelivery(db, wait, s.endpoint.id, (d) => d.attempt_count === 1, "1 回目の記録");
  await clock.advance("73h");
  const failed = await untilDelivery(db, wait, s.endpoint.id, (d) => d.status === "failed", "failed になる");
  await sink.waitForRequests(s.sinkId, 2);
  await sink.setBehavior(s.sinkId, []); // 送信先が回復した

  const res = await consoleOf(api, tenant)<Delivery>("POST", `/v1/console/webhook-deliveries/${failed.id}/redeliver`);
  expect(res.status, JSON.stringify(res.body)).toBe(202);
  expect(res.body).toMatchObject({ trigger: "manual", redelivery_of: failed.id, status: "pending", event: { id: bodyOf(first!).id } });
  const recs = await sink.waitForRequests(s.sinkId, 3);
  expect(recs[2]!.response.status).toBe(200);
  expect(header(recs[2]!, "SP-Event-Id")).toBe(bodyOf(first!).id);
  expect(recs[2]!.body_base64).toBe(first!.body_base64);
  await untilDelivery(db, wait, s.endpoint.id, (d) => d.status === "succeeded", "manual の配信が succeeded");
  const [orig] = await db.query<{ status: string }>("SELECT status FROM integration.webhook_deliveries WHERE id = $1", [failed.id]);
  expect(orig!.status).toBe("failed");

  // ops の配信履歴と再送（監査には対象の事業者が入る）
  const op = await tokens.operator();
  const list = await api.call<{ data: Delivery[] }>("GET", "/v1/ops/webhook-deliveries", { token: op, query: { tenant_id: tenant.id } });
  expect(list.status).toBe(200);
  expect(list.body.data.map((d) => d.trigger).sort()).toEqual(["auto", "manual"]);
  const opsRedo = await api.call<Delivery>("POST", `/v1/ops/webhook-deliveries/${failed.id}/redeliver`, { token: op });
  expect(opsRedo.status, JSON.stringify(opsRedo.body)).toBe(202);
  await sink.waitForRequests(s.sinkId, 4);
  await wait.until(
    async () => (await db.count("SELECT count(*) FROM audit.audit_records WHERE tenant_id = $1 AND action = 'webhook_delivery.redeliver' AND actor_type = 'operator'", [tenant.id])) === 1,
    { message: "ops の再送の監査" },
  );
});
