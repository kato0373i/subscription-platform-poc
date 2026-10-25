// バックオフ・72 時間の打ち切り・課金が配信を待たないこと（integration.md §8.1・§8.2）：AC-INT-012・013・021。
// 本番のバックオフ値（1 分, 5 分, 30 分, 2 時間, …）のまま dev clock で時刻を進める。sink の失敗は回数で指示する（WS-6）。
import { test, expect } from "../../fixtures/index.ts";
import { consoleOf, createPlan } from "../../helpers/catalog.ts";
import { bodyOf, expectNoMoreRequests, header, sinkEndpoint, untilDelivery } from "../../helpers/integration.ts";
import { JOB, addMonths, jst, meSubscription, startAtNext15, subscribe } from "../../helpers/orchestration.ts";

test("AC-INT-012: 最初の 4 回は 500 → 1 分・5 分・30 分・2 時間後に再試行し、5 回目（2 時間 36 分）で succeeded。本文と SP-Event-Id はすべて同じ", async ({ api, world, sink, clock, db, wait }) => {
  await startAtNext15(clock);
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const s = await sinkEndpoint(api, sink, tenant, ["subscription.started"]);
  await sink.setBehavior(s.sinkId, [{ status: 500, times: 4 }]);
  await subscribe(api, world, tenant, plan.id);
  await sink.waitForRequests(s.sinkId, 1);
  let n = 1;
  for (const step of ["1m", "5m", "30m", "2h"]) {
    await untilDelivery(db, wait, s.endpoint.id, (d) => d.attempt_count === n, `${n} 回目の記録`);
    await clock.advance(step);
    n++;
    await sink.waitForRequests(s.sinkId, n);
  }
  await untilDelivery(db, wait, s.endpoint.id, (d) => d.status === "succeeded" && d.attempt_count === 5, "5 回目で succeeded");
  const recs = await sink.requests(s.sinkId);
  expect(recs.map((r) => r.response.status)).toEqual([500, 500, 500, 500, 200]);
  expect(new Set(recs.map((r) => r.body_base64)).size).toBe(1);
  expect(new Set(recs.map((r) => header(r, "SP-Event-Id"))).size).toBe(1);
  expect(recs.map((r) => header(r, "SP-Delivery-Attempt"))).toEqual(["1", "2", "3", "4", "5"]);
  const [d] = await db.query<{ first: string; last: string }>(
    "SELECT first_attempted_at AS first, last_attempted_at AS last FROM integration.webhook_deliveries WHERE endpoint_id = $1",
    [s.endpoint.id],
  );
  const elapsedMin = (Date.parse(d!.last) - Date.parse(d!.first)) / 60_000;
  expect(Math.round(elapsedMin)).toBe(156); // 2 時間 36 分
});

test("AC-INT-013: 常に 500 なら 72 時間を超えた予定で failed になり、以後は送らない。オーナー宛ての webhook_delivery_failed が 1 行", async ({ api, world, sink, clock, db, wait, mail }) => {
  await startAtNext15(clock);
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const s = await sinkEndpoint(api, sink, tenant, ["subscription.started"]);
  await sink.setBehavior(s.sinkId, [{ status: 500 }]);
  await subscribe(api, world, tenant, plan.id);
  await sink.waitForRequests(s.sinkId, 1);
  await untilDelivery(db, wait, s.endpoint.id, (d) => d.attempt_count === 1, "1 回目の記録");
  // 1 回目の 73 時間後に 2 回目を送り、次の予定（+5 分）が打ち切り期限（最初の試行 + 72 時間）を超えるので failed
  await clock.advance("73h");
  await untilDelivery(db, wait, s.endpoint.id, (d) => d.status === "failed", "failed になる");
  const recs = await sink.waitForRequests(s.sinkId, 2);
  await clock.advance("13h");
  await expectNoMoreRequests(sink, s.sinkId, recs.at(-1)!.seq);
  const notes = await mail.notifications({ templateKey: "webhook_delivery_failed" });
  const mine = notes.filter((m) => m.dedupe_key.startsWith(`webhook_delivery_failed:${s.endpoint.id}:`));
  expect(mine).toHaveLength(1);
  const sent = await mail.waitForSent(mine[0]!.dedupe_key);
  expect(sent.variables).toMatchObject({ tenant_name: tenant.name, endpoint_url: s.url, failed_count: 1 });
  const list = await consoleOf(api, tenant)<{ data: { status: string; deadline_at: string; attempt_count: number }[] }>("GET", "/v1/console/webhook-deliveries", undefined, {
    status: "failed",
  });
  expect(list.body.data).toHaveLength(1);
  expect(list.body.data[0]!.attempt_count).toBe(2);
});

test("AC-INT-021: 10 個の送信先がすべて応答しなくても、更新課金のジョブは配信を待たずに終わり、更新は確定する", async ({ api, world, sink, clock, db, wait }) => {
  test.setTimeout(240_000);
  const D = await startAtNext15(clock);
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const sinks = [];
  for (let i = 0; i < 10; i++) {
    const s = await sinkEndpoint(api, sink, tenant, ["subscription.renewed"]);
    await sink.setBehavior(s.sinkId, [{ hang: true }]);
    sinks.push(s);
  }
  const { consumer, subscription } = await subscribe(api, world, tenant, plan.id);
  const renewal = addMonths(D, 1);
  await clock.setTo(jst(renewal, "00:30"));
  const started = Date.now();
  await clock.runJob(JOB.dispatch, { asOf: renewal, force: true });
  expect(Date.now() - started).toBeLessThan(10_000);
  await wait.until(async () => (await db.count("SELECT count(*) FROM billing.invoices WHERE subscription_id = $1 AND kind = 'renewal' AND status = 'paid'", [subscription.id])) === 1, {
    message: "更新の請求が paid",
  });
  await wait.until(async () => (await meSubscription(api, consumer, subscription.id)).next_renewal_date === addMonths(D, 2), { message: "更新が確定する" });
  // 配信は timeout として記録される（課金とは独立に進む）
  await wait.until(
    async () =>
      (await db.count(
        "SELECT count(*) FROM integration.webhook_delivery_attempts a JOIN integration.webhook_deliveries d ON d.id = a.delivery_id WHERE d.tenant_id = $1 AND a.error_kind = 'timeout'",
        [tenant.id],
      )) >= 1,
    { message: "timeout の試行", timeout: 60_000 },
  );
  expect(bodyOf((await sink.waitForRequests(sinks[0]!.sinkId, 1, { timeout: 60_000 }))[0]!).type).toBe("subscription.renewed");
  // 後片付け：応答しない送信先を削除し、以後の時刻を進めるテストの配信の枠を使わせない
  for (const s of sinks) await consoleOf(api, tenant)("DELETE", `/v1/console/webhook-endpoints/${s.endpoint.id}`);
});
