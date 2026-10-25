// console-dashboard FR-001・FR-003（clock）：トライアルの転換と不成立、支払い遅延の回復と不成立、解約の予約による解約が、
// 解約率・収益の解約率・トライアル転換率・支払い回復率・純増 MRR の内訳に定義どおり反映される（要件 §5）。
import { test, expect } from "../../fixtures/index.ts";
import { consoleOf, createPlan } from "../../helpers/catalog.ts";
import { JOB, addDays, addMonths, jst, setBehavior, startAtNext15, subscribe, untilStatus } from "../../helpers/orchestration.ts";
import { monthOf, overview, untilOverview } from "../../helpers/reporting.ts";

test("FR-003: 転換・不成立・回復・支払い失敗の解約・顧客の解約が KPI に反映される", async ({ api, world, wait, clock }) => {
  const D = await startAtNext15(clock);
  const tenant = await world.createTenant();
  expect((await consoleOf(api, tenant)("PUT", "/v1/console/dunning-policy", { retry_offsets_days: [1] })).status).toBe(200);
  const trialPlan = await createPlan(api, tenant, { amount: 980, trialDays: 14 });
  const plan = await createPlan(api, tenant, { amount: 980 });

  const a = await subscribe(api, world, tenant, trialPlan.id); // トライアル → 有料化
  const b = await subscribe(api, world, tenant, trialPlan.id); // トライアル中に解約（不成立）
  const c = await subscribe(api, world, tenant, plan.id); // 更新の失敗 → 回復
  const e = await subscribe(api, world, tenant, plan.id); // 更新の失敗 → 支払い失敗で解約
  const f = await subscribe(api, world, tenant, plan.id); // 解約の予約 → 更新日に解約
  const M = monthOf(D);
  await untilOverview(api, wait, tenant, (o) => o.kpis.current.new_subscriptions === 5 && o.kgi.mrr === 2940, "5 件の申込（有料 3 件）", M);

  await clock.setTo(jst(addDays(D, 5), "09:00"));
  for (const s of [b, f]) {
    expect((await api.call("PUT", `/v1/me/subscriptions/${s.subscription.id}/scheduled-cancellation`, { token: s.consumer.token, body: {} })).status).toBe(200);
  }
  const trialDone = addDays(D, 14);
  await clock.setTo(jst(trialDone, "00:30"));
  await clock.runJob(JOB.dispatch, { asOf: trialDone, force: true });
  await untilStatus(api, wait, a.consumer, a.subscription.id, "active");
  await untilStatus(api, wait, b.consumer, b.subscription.id, "canceled");

  const renewal = addMonths(D, 1);
  await setBehavior(api, c.pm.id, "decline", 1);
  await setBehavior(api, e.pm.id, "decline", 99);
  await clock.setTo(jst(renewal, "00:30"));
  await clock.runJob(JOB.dispatch, { asOf: renewal, force: true });
  await untilStatus(api, wait, c.consumer, c.subscription.id, "past_due");
  await untilStatus(api, wait, e.consumer, e.subscription.id, "past_due");
  await untilStatus(api, wait, f.consumer, f.subscription.id, "canceled");
  const N = monthOf(renewal);
  await untilOverview(api, wait, tenant, (o) => o.attention.past_due === 2, "支払い遅延 2 件", N);

  await clock.setTo(jst(addDays(renewal, 1), "10:00"));
  await clock.runJob(JOB.collectionDue, { force: true });
  await untilStatus(api, wait, c.consumer, c.subscription.id, "active");
  await untilStatus(api, wait, e.consumer, e.subscription.id, "canceled");

  // 更新月（N）：期首は C・E・F（と、トライアルが M のうちに終わっていれば A）
  const convertedInM = monthOf(trialDone) === M;
  const startPaying = convertedInM ? 4 : 3;
  const ov = await untilOverview(api, wait, tenant, (o) => o.kpis.current.recovery.denominator === 2 && o.kgi.mrr === 1960, "回復と支払い失敗の解約", N);
  const k = ov.kpis.current;
  expect(ov.kgi.mrr).toBe(1960); // A と C
  expect(ov.kgi.breakdown.churned).toBe(1960); // E と F
  expect(k.canceled).toEqual({ total: 2, customer: 1, dunning: 1, tenant_suspended: 0 });
  expect(k.churn_rate).toEqual({ numerator: 2, denominator: startPaying, value: 2 / startPaying });
  expect(k.revenue_churn_rate).toEqual({ numerator: 1960, denominator: startPaying * 980, value: 1960 / (startPaying * 980) });
  expect(k.recovery).toEqual({ numerator: 1, denominator: 2, value: 0.5 });
  expect(k.revenue.renewal).toBe(convertedInM ? 980 : 1960); // C の回収（と、N に入った A の初回課金）
  expect(ov.attention.past_due).toBe(0);

  // 2 か月の合計：新規 5 件・トライアル転換 1 / 2・初回の入金 2,940 円
  const m = (await overview(api, tenant, M)).body.kpis.current;
  const sum = (f: (x: typeof k) => number) => f(m) + f(k);
  expect(sum((x) => x.new_subscriptions)).toBe(5);
  expect(sum((x) => x.trial_conversion.numerator)).toBe(1);
  expect(sum((x) => x.trial_conversion.denominator)).toBe(2);
  expect(sum((x) => x.revenue.initial)).toBe(2940);
  expect(sum((x) => x.canceled.total)).toBe(3); // B・E・F
  expect(ov.kgi.mrr_start).toBe(startPaying * 980);
  expect(ov.kgi.net_new_mrr).toBe(ov.kgi.mrr - ov.kgi.mrr_start);
});
