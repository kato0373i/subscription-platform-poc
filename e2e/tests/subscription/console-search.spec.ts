// business-console FR-010：契約の一覧をメールアドレスで検索でき、契約者（氏名・メールアドレス）を一緒に返す。
import type { ConsoleSubscriptionComponents } from "@sp/api-client";
import { test, expect } from "../../fixtures/index.ts";
import { createPlan } from "../../helpers/catalog.ts";
import { subscribe } from "../../helpers/orchestration.ts";

type Subscription = ConsoleSubscriptionComponents["schemas"]["Subscription"];

test("FR-010: email で検索すると、その契約者の契約だけが返り、customer にメールアドレスが入る。知らないメールアドレスは空", async ({ api, world }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant);
  const a = await subscribe(api, world, tenant, plan.id);
  await subscribe(api, world, tenant, plan.id);
  const call = (query: Record<string, string>) =>
    api.call<{ data: Subscription[] }>("GET", "/v1/console/subscriptions", { token: tenant.ownerToken, tenantId: tenant.id, query });

  const all = await call({});
  expect(all.body.data).toHaveLength(2);
  const hit = await call({ email: a.consumer.email.toUpperCase() });
  expect(hit.status, JSON.stringify(hit.body)).toBe(200);
  expect(hit.body.data.map((s) => s.id)).toEqual([a.subscription.id]);
  expect(hit.body.data[0]!.customer).toMatchObject({ email: a.consumer.email, withdrawn: false });
  expect(hit.body.data[0]!.customer.id).toBe(hit.body.data[0]!.customer_id);

  const miss = await call({ email: "nobody@example.com" });
  expect(miss.status).toBe(200);
  expect(miss.body.data).toEqual([]);
});
