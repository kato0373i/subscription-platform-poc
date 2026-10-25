// E2E-016 他のテナントのデータにアクセスできない（請求の履歴を含む）（orchestration.md §11b.3）：AC-ORC-016・033、AC-BIL-031。
import { test, expect } from "../../fixtures/index.ts";
import { consoleOf, createPlan } from "../../helpers/catalog.ts";
import { checkout, type Consumer } from "../../helpers/orchestration.ts";

test(
  "E2E-016: 他の事業者・他の契約者からは契約・請求・支払方法の操作がすべて 404。事業者ごとの契約者 ID と事業者ショップ ID",
  { tag: ["@isolation:meChangeSubscriptionPaymentMethod", "@isolation:meDeletePaymentMethod"] },
  async ({ api, world, db }) => {
    const t1 = await world.createTenant({ apiKey: true });
    const t2 = await world.createTenant({ apiKey: true });
    const p1 = await createPlan(api, t1, { amount: 980 });
    const p2 = await createPlan(api, t2, { amount: 980 });
    const c1 = (await world.createConsumer()) as Consumer;
    const c2 = (await world.createConsumer()) as Consumer;
    const pm = await world.createPaymentMethod(c1, "ok");
    const s1 = await checkout(api, c1, t1, p1.id, pm.id);
    const s2 = await checkout(api, c1, t2, p2.id, pm.id);
    expect(s1.status).toBe(201);
    expect(s2.status).toBe(201);
    const subT1 = s1.body.subscription.id;
    const [links] = await db.query<{ n: number; d: number }>("SELECT count(*)::int AS n, count(DISTINCT id)::int AS d FROM customer.tenant_customer_links WHERE customer_account_id = $1", [c1.customerAccountId]);
    expect(links).toEqual({ n: 2, d: 2 });
    const [cus] = await db.query<{ tenant_customer_id: string }>("SELECT tenant_customer_id FROM subscription.subscriptions WHERE id = $1", [subT1]);
    // AC-ORC-016：決済に事業者ショップ ID（tenants.fincode_shop_id）を渡している（P-PAY-1）。StubAdapter は参照を共有する
    // （RefShared）ので事業者ショップ向けの参照の行は作らない。行の確認は fincode のアダプターの実装時
    expect(
      await db.count(
        "SELECT count(*) FROM payments.payment_attempts a JOIN tenant.tenants t ON t.id = a.tenant_id AND t.fincode_shop_id = a.tenant_shop_id WHERE a.payment_method_id = $1",
        [pm.id],
      ),
    ).toBe(2);

    // ② T2 の API キー
    for (const path of [`/v1/tenant/subscriptions/${subT1}`, `/v1/tenant/customers/${cus!.tenant_customer_id}/entitlements`]) {
      expect((await api.call("GET", path, { token: t2.apiKey })).status, path).toBe(404);
    }
    // ③ T2 のスタッフ
    expect((await consoleOf(api, t2)("GET", `/v1/console/subscriptions/${subT1}`)).status).toBe(404);
    expect((await consoleOf(api, t2)<{ data: unknown[] }>("GET", `/v1/console/subscriptions/${subT1}/invoices`)).body.data).toEqual([]);
    // ④ T1 のスタッフに X-Tenant-Id: T2
    expect([403, 404]).toContain((await api.call("GET", "/v1/console/subscriptions", { token: t1.ownerToken, tenantId: t2.id })).status);
    // ⑤ C2
    expect((await api.call("GET", `/v1/me/subscriptions/${subT1}`, { token: c2.token })).status).toBe(404);
    expect((await api.call<{ data: unknown[] }>("GET", `/v1/me/subscriptions/${subT1}/invoices`, { token: c2.token })).body.data).toEqual([]);
    const pmC2 = await world.createPaymentMethod(c2, "ok");
    const chg = await api.call("PUT", `/v1/me/subscriptions/${subT1}/payment-method`, { token: c2.token, body: { payment_method_id: pmC2.id } });
    expect(chg.status).toBe(404);
    expect(chg.problem?.code).toBe("not_found");
    const del = await api.call("DELETE", `/v1/me/payment-methods/${pm.id}`, { token: c2.token });
    expect(del.status).toBe(404);
    // 本人には見える（isolation の owner 側）
    expect((await api.call("PUT", `/v1/me/subscriptions/${subT1}/payment-method`, { token: c1.token, body: { payment_method_id: pm.id } })).status).toBe(200);
    expect((await api.call("DELETE", `/v1/me/payment-methods/${pm.id}`, { token: c1.token })).status).toBe(409); // 使用中（存在は見える）
    expect(await db.count("SELECT count(*) FROM subscription.subscriptions WHERE customer_account_id = $1 AND payment_method_id = $2", [c1.customerAccountId, pm.id])).toBe(2);
    // ⑥ 本人・T1 のスタッフには初回の請求 1 件だけ
    const mine = await api.call<{ data: { id: string }[] }>("GET", `/v1/me/subscriptions/${subT1}/invoices`, { token: c1.token });
    expect(mine.body.data).toHaveLength(1);
    expect(mine.body.data[0]!.id).toBe(s1.body.invoice!.id);
    expect((await consoleOf(api, t1)<{ data: unknown[] }>("GET", `/v1/console/subscriptions/${subT1}/invoices`)).body.data).toHaveLength(1);
  },
);
