// console-dashboard FR-001・FR-002・FR-003・FR-005・FR-009（API）：申込が KGI・KPI に反映される、年払いの月額換算、目標の設定と権限、事業者の分離。
import { randomUUID } from "node:crypto";
import { test, expect } from "../../fixtures/index.ts";
import { createPlan } from "../../helpers/catalog.ts";
import { subscribe } from "../../helpers/orchestration.ts";
import { overview, untilOverview } from "../../helpers/reporting.ts";

test("FR-001・FR-003: 申込が MRR・新規契約数・入金額・ARPU に反映される。年払いは 12 で割って四捨五入（9,800 円 → 817 円）", async ({ api, world, wait }) => {
  const tenant = await world.createTenant();
  const monthly = await createPlan(api, tenant, { amount: 980 });
  const yearly = await createPlan(api, tenant, { amount: 9800, interval: "year" });
  await subscribe(api, world, tenant, monthly.id);
  await subscribe(api, world, tenant, monthly.id);
  await subscribe(api, world, tenant, yearly.id);

  const ov = await untilOverview(api, wait, tenant, (o) => o.kpis.current.new_subscriptions === 3 && o.kpis.current.revenue.total === 11760, "3 件の申込が集計される");
  expect(ov.is_current).toBe(true);
  expect(ov.as_of).not.toBeNull();
  expect(ov.kgi).toMatchObject({ mrr: 2777, mrr_start: 0, arr: 2777 * 12, net_new_mrr: 2777, breakdown: { new: 2777, expansion: 0, contraction: 0, churned: 0 }, goal: null });
  const k = ov.kpis.current;
  expect(k).toMatchObject({ new_mrr: 2777, paying: 3, trialing: 0, past_due: 0, arpu: 926, revenue: { total: 11760, initial: 11760, renewal: 0 } });
  expect(k.churn_rate).toEqual({ numerator: 0, denominator: 0, value: null }); // 期首の有料契約が 0
  expect(ov.trend).toHaveLength(12);
  expect(ov.trend[11]).toMatchObject({ month: ov.month, mrr: 2777, new_subscriptions: 3, revenue: 11760 });
  expect(ov.daily.at(-1)).toMatchObject({ date: ov.today, mrr: 2777 });
  expect(ov.attention).toEqual({ past_due: 0, trials_ending_7d: 0 });
});

test("FR-009: 他の事業者の申込は集計に含まれない", async ({ api, world, wait }) => {
  const a = await world.createTenant();
  const b = await world.createTenant();
  const plan = await createPlan(api, a, { amount: 980 });
  await subscribe(api, world, a, plan.id);
  await untilOverview(api, wait, a, (o) => o.kgi.mrr === 980, "A の申込が集計される");
  const other = await overview(api, b);
  expect(other.status).toBe(200);
  expect(other.body.kgi.mrr).toBe(0);
  expect(other.body.kpis.current.new_subscriptions).toBe(0);
  expect(other.body.as_of).toBeNull();
});

test("FR-002: 目標は owner・admin が設定でき、達成率が出る。viewer は 403、範囲外は 400、古い If-Match は 409、削除できる。変更は監査に残る", async ({ api, world, wait, db }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 1960 });
  await subscribe(api, world, tenant, plan.id);
  const { month } = await untilOverview(api, wait, tenant, (o) => o.kgi.mrr === 1960, "申込が集計される");
  const admin = await world.createStaff(tenant, "admin");
  const viewer = await world.createStaff(tenant, "viewer");
  const put = (token: string, amount: number, ifMatch?: string) =>
    api.call<{ amount: number; version: number }>("PUT", `/v1/console/mrr-goals/${month}`, {
      token, tenantId: tenant.id, idempotencyKey: randomUUID(), body: { amount }, headers: ifMatch === undefined ? {} : { "If-Match": ifMatch },
    });

  const set = await put(admin.token, 10000, "0");
  expect(set.status, JSON.stringify(set.body)).toBe(200);
  expect(set.body).toEqual({ amount: 10000, version: 1 });
  const ov = (await overview(api, tenant)).body;
  expect(ov.kgi.goal).toEqual({ amount: 10000, version: 1, achievement_rate: 0.196 });

  expect((await put(viewer.token, 5000)).status).toBe(403);
  for (const bad of [0, 1_000_000_001]) {
    const r = await put(tenant.ownerToken, bad);
    expect(r.status).toBe(400);
    expect(r.problem?.errors).toEqual([{ field: "amount", code: "out_of_range" }]);
  }
  const stale = await put(tenant.ownerToken, 20000, "0");
  expect(stale.status).toBe(409);
  expect(stale.problem?.code).toBe("conflict");
  expect((await put(tenant.ownerToken, 20000, "1")).body).toEqual({ amount: 20000, version: 2 });

  const del = await api.call("DELETE", `/v1/console/mrr-goals/${month}`, { token: tenant.ownerToken, tenantId: tenant.id });
  expect(del.status).toBe(204);
  expect((await overview(api, tenant)).body.kgi.goal).toBeNull();
  const actions = await db.query<{ action: string }>("SELECT action FROM audit.audit_records WHERE tenant_id = $1 AND action LIKE 'mrr_goal.%' ORDER BY action", [tenant.id]);
  expect(actions.map((a) => a.action)).toEqual(["mrr_goal.delete", "mrr_goal.update", "mrr_goal.update"]);
});

test("FR-005: 未来の月・25 か月以上前・形式の誤った月は 400", async ({ api, world }) => {
  const tenant = await world.createTenant();
  const { body } = await overview(api, tenant);
  const [y, m] = body.month.split("-").map(Number) as [number, number];
  const shift = (n: number) => {
    const d = new Date(Date.UTC(y, m - 1 + n, 1));
    return `${d.getUTCFullYear()}-${String(d.getUTCMonth() + 1).padStart(2, "0")}`;
  };
  expect((await overview(api, tenant, shift(-24))).status).toBe(200);
  for (const bad of [shift(1), shift(-25), "2026-13"]) {
    const r = await overview(api, tenant, bad);
    expect(r.status, bad).toBe(400);
  }
  const prev = await overview(api, tenant, shift(-1));
  expect(prev.body).toMatchObject({ month: shift(-1), is_current: false });
});
