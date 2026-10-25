// 再試行待ちの配信への再送の拒否（integration.md §7.1・§9 E6）：AC-INT-015。
import { test, expect } from "../../fixtures/index.ts";
import { consoleOf, createPlan } from "../../helpers/catalog.ts";
import { type Delivery, sinkEndpoint, untilDelivery } from "../../helpers/integration.ts";
import { subscribe } from "../../helpers/orchestration.ts";

test("AC-INT-015: 常に 500 の送信先で pending（再試行待ち）の配信を再送すると 409 webhook_delivery_in_progress", async ({ api, world, sink, db, wait }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const s = await sinkEndpoint(api, sink, tenant, ["subscription.started"]);
  await sink.setBehavior(s.sinkId, [{ status: 500 }]);
  await subscribe(api, world, tenant, plan.id);
  const d = await untilDelivery(db, wait, s.endpoint.id, (r) => r.attempt_count === 1, "1 回目が失敗して再試行待ち");
  expect(d.status).toBe("pending");
  const call = consoleOf(api, tenant);
  const res = await call("POST", `/v1/console/webhook-deliveries/${d.id}/redeliver`);
  expect(res.status).toBe(409);
  expect(res.problem?.code).toBe("webhook_delivery_in_progress");

  const got = await call<Delivery & { attempts: { attempt_no: number; status_code: number; error_kind: string; response_excerpt: string }[] }>(
    "GET",
    `/v1/console/webhook-deliveries/${d.id}`,
  );
  expect(got.body).toMatchObject({ status: "pending", attempt_count: 1, last_response: { status_code: 500, error_kind: "http_status" } });
  expect(got.body.attempts).toEqual([expect.objectContaining({ attempt_no: 1, status_code: 500, error_kind: "http_status" })]);
  const list = await call<{ data: Delivery[] }>("GET", "/v1/console/webhook-deliveries", undefined, { status: "pending", endpoint_id: s.endpoint.id });
  expect(list.body.data.map((x) => x.id)).toEqual([d.id]);

  // 無効にした送信先への再送は 409 webhook_endpoint_unavailable（pending は次の予定時刻に送らずに abandoned になる）
  expect((await call("PATCH", `/v1/console/webhook-endpoints/${s.endpoint.id}`, { enabled: false })).status).toBe(200);
  const unavailable = await call("POST", `/v1/console/webhook-deliveries/${d.id}/redeliver`);
  expect(unavailable.problem?.code).toBe("webhook_endpoint_unavailable");
});
