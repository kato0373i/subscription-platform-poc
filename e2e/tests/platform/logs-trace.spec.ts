// platform.md §8.6（ログと調査）：AC-PLT-073・078。logs fixture の相関 ID で、申込（api）→ 購読（worker-billing など）→
// Webhook の配信（worker-integration）までのログを 1 つの相関 ID で追える。
import { test, expect } from "../../fixtures/index.ts";
import { devlogs } from "../../fixtures/logs.ts";
import { createPlan } from "../../helpers/catalog.ts";
import { sinkEndpoint } from "../../helpers/integration.ts";
import { subscribe } from "../../helpers/orchestration.ts";

test("AC-PLT-073・078: 申込から Webhook の配信までのログが 1 つの相関 ID でそろい、契約と請求の状態の変化が残る", async ({ api, world, sink, logs, wait }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const s = await sinkEndpoint(api, sink, tenant, ["subscription.started"]);
  const { subscription, invoice } = await subscribe(api, world, tenant, plan.id);
  await sink.waitForRequests(s.sinkId, 1);

  // 配信の結果のログ（worker-integration）がファイルに書かれるまで待つ
  const lines = await wait.until(
    async () => {
      const ls = await logs.query();
      return ls.some((l) => l.service === "worker-integration" && l.msg === "http.outbound.completed") ? ls : undefined;
    },
    { message: "worker-integration の http.outbound.completed" },
  );
  // AC-PLT-073：3 つの service の行が ts の昇順で並び、主要なログの種類を含む
  const services = new Set(lines.map((l) => l.service));
  for (const svc of ["api", "worker-billing", "worker-integration"]) expect(services, svc).toContain(svc);
  const ts = lines.map((l) => Date.parse(l.ts));
  expect(ts).toEqual([...ts].sort((a, b) => a - b));
  const msgs = new Set(lines.map((l) => l.msg));
  for (const m of ["http.request.completed", "event.published", "event.consumed", "domain.state_changed", "http.outbound.completed"]) expect(msgs, m).toContain(m);
  const delivery = lines.find((l) => l.service === "worker-integration" && l.msg === "http.outbound.completed")!;
  expect(delivery).toMatchObject({ target: "webhook", status: 200 });
  const timeline = await devlogs(["--corr", logs.correlationId, "--format=timeline"]);
  expect(timeline.split("\n")[0]).toContain(logs.correlationId);

  // AC-PLT-078：契約は pending_initial → active、請求は paid になる
  const changes = await logs.query({ msg: "domain.state_changed" });
  expect(changes).toContainEqual(expect.objectContaining({ aggregate: "subscription", id: subscription.id, from: "pending_initial", to: "active" }));
  expect(changes).toContainEqual(expect.objectContaining({ aggregate: "invoice", id: (invoice as { id: string }).id, to: "paid" }));
});
