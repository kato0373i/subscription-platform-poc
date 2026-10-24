// subscription の E2E で使う準備。申込 API（orchestration）の実装前なので、契約は dev の一括シード（段階 1。P-IMP-14）で作る。
import { expect } from "@playwright/test";
import type { ConsoleSubscriptionComponents, MeSubscriptionComponents } from "@sp/api-client";
import type { Api } from "../fixtures/api.ts";
import type { Tokens } from "../fixtures/tokens.ts";
import type { TenantWorld, World } from "../fixtures/world.ts";
import { consoleOf } from "./catalog.ts";

export type MeSubscription = MeSubscriptionComponents["schemas"]["MeSubscription"];
export type Subscription = ConsoleSubscriptionComponents["schemas"]["Subscription"];

export interface Seeded {
  seedId: string;
  firstRenewalDate: string;
  /** 契約者 n（1 始まり）のトークン。シードの合成 subject（seed:{seed_id}:{n}）で発行する。 */
  consumer(n: number): Promise<string>;
  /** console から見た契約（作成順）。 */
  subscriptions(): Promise<Subscription[]>;
}

/** 公開済みの月額・トライアルなしのプランに、count 件の active な契約を作る。 */
export async function seedActive(api: Api, world: World, tokens: Tokens, tenant: TenantWorld, planId: string, count = 1): Promise<Seeded> {
  const seeded = await world.seedSubscriptions(tenant, planId, count);
  return {
    ...seeded,
    consumer: async (n) => (await tokens.issue({ pool: "consumer", sub: `seed:${seeded.seedId}:${n}`, email: `seed-${seeded.seedId}-${n}@example.invalid` })).token,
    subscriptions: async () => {
      const res = await consoleOf(api, tenant)<{ data: Subscription[] }>("GET", "/v1/console/subscriptions", undefined, { limit: 100 });
      expect(res.status, JSON.stringify(res.body)).toBe(200);
      return res.body.data.reverse();
    },
  };
}
