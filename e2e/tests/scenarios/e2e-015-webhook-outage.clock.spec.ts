// E2E-015 Webhook の送信先の障害から回復（orchestration.md §11b.3、integration.md §8.2）：FR-022・NFR-005。
import { test, expect } from "../../fixtures/index.ts";
import { consoleOf, createPlan } from "../../helpers/catalog.ts";
import { bodyOf, type Delivery, sinkEndpoint } from "../../helpers/integration.ts";
import { checkout, expectInvariants, startAtNext15, type Consumer } from "../../helpers/orchestration.ts";

test("E2E-015: 送信先が 500 を返している間も申込は 201。1 分・5 分・30 分の再試行は失敗し、回復後の 2 時間後の試行で succeeded", async ({ api, world, sink, clock, db, wait }) => {
  await startAtNext15(clock);
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const s = await sinkEndpoint(api, sink, tenant, ["subscription.started"]);
  await sink.setBehavior(s.sinkId, [{ status: 500 }]); // ① 送信先の障害

  // ② 申込は障害の影響を受けない。課金側の処理は申込の直後に完了している
  const consumer = (await world.createConsumer()) as Consumer;
  const pm = await world.createPaymentMethod(consumer, "ok");
  const res = await checkout(api, consumer, tenant, plan.id, pm.id);
  expect(res.status, JSON.stringify(res.body)).toBe(201);
  const sub = res.body.subscription;
  expect(sub.status).toBe("active");
  expect(await db.count("SELECT count(*) FROM billing.invoices WHERE subscription_id = $1 AND status = 'paid'", [sub.id])).toBe(1);
  expect(await db.count("SELECT count(*) FROM orchestration.checkout_sagas WHERE subscription_id = $1 AND status = 'completed'", [sub.id])).toBe(1);

  // ③ 本番のバックオフ値どおりに時刻を進め、その間は pending で attempt_count が増える
  const call = consoleOf(api, tenant);
  const delivery = async () => (await call<{ data: Delivery[] }>("GET", "/v1/console/webhook-deliveries", undefined, { endpoint_id: s.endpoint.id })).body.data[0];
  let n = 1;
  await sink.waitForRequests(s.sinkId, n);
  for (const step of ["1m", "5m", "30m"]) {
    await wait.until(async () => (await delivery())?.attempt_count === n, { message: `${n} 回目の記録` });
    expect((await delivery())!.status).toBe("pending");
    await clock.advance(step);
    n++;
    await sink.waitForRequests(s.sinkId, n);
  }
  await wait.until(async () => (await delivery())?.attempt_count === 4, { message: "4 回目の記録" });

  // ④ 回復 → 2 時間後の試行で succeeded
  await sink.setBehavior(s.sinkId, []);
  await clock.advance("2h");
  const done = await wait.until(async () => {
    const d = await delivery();
    return d?.status === "succeeded" ? d : undefined;
  }, { message: "succeeded になる" });
  expect(done.attempt_count).toBe(5);
  const attempts = await db.query<{ status_code: number; error_kind: string | null }>(
    "SELECT a.status_code, a.error_kind FROM integration.webhook_delivery_attempts a WHERE a.delivery_id = $1 ORDER BY attempt_no",
    [done.id],
  );
  expect(attempts.filter((a) => a.error_kind === "http_status").length).toBeGreaterThanOrEqual(3);
  expect(attempts.at(-1)).toEqual({ status_code: 200, error_kind: null });
  const recs = await sink.requests(s.sinkId);
  expect(recs.length).toBeGreaterThanOrEqual(1);
  expect(new Set(recs.map((r) => bodyOf(r).id)).size).toBe(1);
  expect(bodyOf(recs.at(-1)!).type).toBe("subscription.started");
  await expectInvariants(db, wait, tenant.id);
});
