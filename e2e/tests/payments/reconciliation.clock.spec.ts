// payments.md §8.3（日次照合）：AC-PAY-022。前日に「代行は成功・自社は unknown」の試行があるとき、
// reconcile_daily が provider_succeeded_local_not を 1 件だけ記録する（再実行しても 1 件）。
import { test, expect } from "../../fixtures/index.ts";
import { createPlan } from "../../helpers/catalog.ts";
import { checkout, startAtNext15, type Consumer } from "../../helpers/orchestration.ts";

type Report = { id: string; kind: string; attempt_id: string | null; tenant_id: string | null; provider_order_ref: string; status: string };

/**
 * resolve_unknown は SCHEDULER_MODE=internal で実時間の毎分 0 秒に動く（P-IMP-1）。日付を進めてから照合を流すまでのあいだに
 * それが動くと、試行が先に確定して不整合にならない。毎分の境目から離れた時刻（5〜40 秒）まで待ってから進める。
 */
async function awayFromMinuteBoundary() {
  const s = new Date().getSeconds();
  if (s >= 5 && s <= 40) return;
  await new Promise((r) => setTimeout(r, ((s > 40 ? 65 : 5) - s) * 1000));
}

test("AC-PAY-022: 前日の「代行は成功・自社は unknown」を reconcile_daily が provider_succeeded_local_not として 1 件記録し、再実行しても 1 件", async ({ api, world, db, clock, tokens }) => {
  const D = await startAtNext15(clock);
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const consumer = (await world.createConsumer()) as Consumer;
  const pm = await world.createPaymentMethod(consumer, "timeout"); // 代行は課金済み、自社は応答を受け取れず unknown

  await awayFromMinuteBoundary();
  const res = await checkout(api, consumer, tenant, plan.id, pm.id);
  expect(res.status, JSON.stringify(res.body)).toBe(202);
  const [attempt] = await db.query<{ id: string; status: string; provider_order_ref: string }>("SELECT id, status, provider_order_ref FROM payments.payment_attempts WHERE tenant_id = $1", [tenant.id]);
  expect(attempt!.status).toBe("unknown");
  // resolve_unknown を動かさずに日付を進めて、前日（D）の照合を流す
  await clock.advance("24h");
  await clock.runJob("payments-reconcile-daily", { force: true });
  expect((await db.query<{ status: string }>("SELECT status FROM payments.payment_attempts WHERE id = $1", [attempt!.id]))[0]!.status).toBe("unknown");

  const op = await tokens.operator();
  const list = async () => {
    const r = await api.call<{ data: Report[] }>("GET", "/v1/ops/reconciliation-reports", { token: op, query: { business_date: D, limit: 100 } });
    expect(r.status).toBe(200);
    return r.body.data.filter((x) => x.attempt_id === attempt!.id);
  };
  const first = await list();
  expect(first).toHaveLength(1);
  expect(first[0]).toMatchObject({ kind: "provider_succeeded_local_not", tenant_id: tenant.id, provider_order_ref: attempt!.provider_order_ref, status: "open" });

  await clock.runJob("payments-reconcile-daily", { force: true });
  expect((await list()).map((x) => x.id)).toEqual([first[0]!.id]);
});
