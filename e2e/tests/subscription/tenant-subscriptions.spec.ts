// subscription.md §7.2（事業者 API）：AC-SUB-032・033（active の部分）、AC-IDN-019 の事業者 API での確認
import { test, expect } from "../../fixtures/index.ts";
import { consoleOf, createPlan } from "../../helpers/catalog.ts";
import { seedActive, type Subscription } from "../../helpers/subscription.ts";

test(
  "AC-SUB-032: 事業者 A の API キーで B の契約・cus_ を照会すると 404。自社の契約と利用権は取れる",
  { tag: ["@isolation:tenantGetSubscription", "@isolation:tenantGetEntitlements"] },
  async ({ api, world, tokens }) => {
    const a = await world.createTenant({ apiKey: true });
    const b = await world.createTenant({ apiKey: true });
    const planA = await createPlan(api, a);
    const planB = await createPlan(api, b);
    const subA = (await (await seedActive(api, world, tokens, a, planA.id, 1)).subscriptions())[0]!;
    const subB = (await (await seedActive(api, world, tokens, b, planB.id, 1)).subscriptions())[0]!;
    const asA = (path: string, query?: Record<string, string>) => api.call<unknown>("GET", path, { token: a.apiKey, query });

    const own = await asA(`/v1/tenant/subscriptions/${subA.id}`);
    expect(own.status, JSON.stringify(own.body)).toBe(200);
    expect(own.body).toMatchObject({ id: subA.id, customer_id: subA.customer_id, status: "active", entitled: true });
    expect(own.body).not.toHaveProperty("applied_discount");

    // AC-SUB-033（active の部分）：利用権
    const ent = await asA(`/v1/tenant/customers/${subA.customer_id}/entitlements`);
    expect(ent.status).toBe(200);
    expect(ent.body).toEqual({ customer_id: subA.customer_id, entitled: true, products: [{ product_id: planA.product_id, entitled: true, subscription_ids: [subA.id] }] });
    const byProduct = await asA(`/v1/tenant/customers/${subA.customer_id}/entitlements`, { product_id: crypto.randomUUID() });
    expect(byProduct.body).toMatchObject({ entitled: false, products: [] });

    for (const path of [`/v1/tenant/subscriptions/${subB.id}`, `/v1/tenant/customers/${subB.customer_id}/entitlements`]) {
      const res = await asA(path);
      expect(res.status, path).toBe(404);
      expect(res.problem?.code).toBe("not_found");
    }
    const list = await api.call<{ data: Subscription[] }>("GET", "/v1/tenant/subscriptions", { token: a.apiKey, query: { customer_id: subA.customer_id } });
    expect(list.body.data.map((s) => s.id)).toEqual([subA.id]);
  },
);

test("AC-IDN-019: 失効した API キーで GET /v1/tenant/subscriptions を呼ぶと 401。失効前に発行した別のキーは使える", async ({ api, world }) => {
  const tenant = await world.createTenant({ apiKey: true });
  const call = consoleOf(api, tenant);
  const second = await call<{ id: string; api_key: string }>("POST", "/v1/console/api-keys", { name: "新" });
  const keys = await call<{ data: { id: string; key_prefix: string }[] }>("GET", "/v1/console/api-keys");
  const first = keys.body.data.find((k) => tenant.apiKey!.startsWith(k.key_prefix))!;
  expect((await call("DELETE", `/v1/console/api-keys/${first.id}`)).status).toBe(204);

  const revoked = await api.call("GET", "/v1/tenant/subscriptions", { token: tenant.apiKey });
  expect(revoked.status).toBe(401);
  expect(revoked.problem?.code).toBe("unauthenticated");
  expect((await api.call("GET", "/v1/tenant/subscriptions", { token: second.body.api_key })).status).toBe(200);
});

test("AC-PLT-025（tenant_api の入口）: API キーは 200、staff・consumer のトークンは 403、トークンなしは 401", async ({ api, world }) => {
  const tenant = await world.createTenant({ apiKey: true });
  const consumer = await world.createConsumer();
  expect((await api.call("GET", "/v1/tenant/subscriptions", { token: tenant.apiKey })).status).toBe(200);
  for (const token of [tenant.ownerToken, consumer.token]) {
    const res = await api.call("GET", "/v1/tenant/subscriptions", { token });
    expect(res.status).toBe(403);
    expect(res.problem?.code).toBe("forbidden");
  }
  expect((await api.call("GET", "/v1/tenant/subscriptions")).status).toBe(401);
});
