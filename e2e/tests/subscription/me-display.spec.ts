// consumer-mypage §5.1：me の契約の表示用の値（display）と、プラン変更の候補（plan-change-options）。
import { test, expect } from "../../fixtures/index.ts";
import { consoleOf, createPlan } from "../../helpers/catalog.ts";
import { expectIsolated } from "../../helpers/isolation.ts";
import { subscribe } from "../../helpers/orchestration.ts";

type Option = { plan_id: string; name: string; billing_interval: string; amount: number; effective_date: string };

test("display に事業者名・商品名・プラン名・契約時の価格が入り、価格の改定の後も変わらない", async ({ api, world }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const { consumer, subscription } = await subscribe(api, world, tenant, plan.id);
  const get = () => api.call<{ display: Record<string, unknown> | null }>("GET", `/v1/me/subscriptions/${subscription.id}`, { token: consumer.token });
  expect((await get()).body.display).toEqual({ tenant_name: tenant.name, product_name: "商品", plan_name: "月額プラン", unit_amount: 980, amount: 980 });
  expect((await consoleOf(api, tenant)("POST", `/v1/console/plans/${plan.id}/price-versions`, { amount: 1280 })).status).toBe(201);
  expect((await get()).body.display).toMatchObject({ unit_amount: 980, amount: 980 }); // 据え置き（上位 FR-004）
  const list = await api.call<{ data: { id: string; display: { plan_name: string } | null }[] }>("GET", "/v1/me/subscriptions", { token: consumer.token });
  expect(list.body.data.map((s) => s.display?.plan_name)).toEqual(["月額プラン"]);
});

test("plan-change-options は同じ商品の公開中のプランのうち現在以外を、適用日（次の更新日）つきで返す。他人の契約は 404", { tag: "@isolation:meListPlanChangeOptions" }, async ({ api, world }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const call = consoleOf(api, tenant);
  const mk = async (name: string, amount: number, publish: boolean, product = plan.product_id) => {
    const p = await call<{ id: string }>("POST", "/v1/console/plans", { product_id: product, name, billing_interval: "month", trial_days: 0, amount });
    if (publish) expect((await call("POST", `/v1/console/plans/${p.body.id}/publish`)).status).toBe(200);
    return p.body.id;
  };
  const pro = await mk("プロ", 1980, true);
  await mk("下書き", 2980, false);
  const other = await createPlan(api, tenant, { amount: 500 }); // 別の商品
  void other;
  const { consumer, subscription } = await subscribe(api, world, tenant, plan.id);
  const opts = await api.call<{ data: Option[] }>("GET", `/v1/me/subscriptions/${subscription.id}/plan-change-options`, { token: consumer.token });
  expect(opts.status, JSON.stringify(opts.body)).toBe(200);
  const sub = await api.call<{ next_renewal_date: string }>("GET", `/v1/me/subscriptions/${subscription.id}`, { token: consumer.token });
  expect(opts.body.data).toEqual([{ plan_id: pro, name: "プロ", billing_interval: "month", amount: 1980, effective_date: sub.body.next_renewal_date }]);

  const stranger = await world.createConsumer();
  await expectIsolated((as) =>
    api.call("GET", `/v1/me/subscriptions/${subscription.id}/plan-change-options`, { token: as === "owner" ? consumer.token : stranger.token }),
  );

  // 解約を予約すると候補は空
  expect((await api.call("PUT", `/v1/me/subscriptions/${subscription.id}/scheduled-cancellation`, { token: consumer.token, body: {} })).status).toBe(200);
  const none = await api.call<{ data: Option[] }>("GET", `/v1/me/subscriptions/${subscription.id}/plan-change-options`, { token: consumer.token });
  expect(none.body.data).toEqual([]);
});
