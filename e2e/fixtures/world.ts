// world fixture：各テストが自分専用のテナント・契約者を作る（P-TST-4）。他のテストのデータを参照しない。後片付けはしない。
// 呼び出す API は identity（ops・console・me）・payments・dev の各 spec で決まったもの。モジュールの実装に合わせて使う。
import { expect } from "@playwright/test";
import type { Api } from "./api.ts";
import type { Tokens } from "./tokens.ts";
import type { Wait } from "./wait.ts";

export interface TenantWorld {
  id: string;
  name: string;
  ownerToken: string;
  ownerSub: string;
  apiKey?: string;
}

export interface ConsumerWorld {
  token: string;
  sub: string;
  email: string;
  customerAccountId: string;
}

export interface World {
  createTenant(opts?: { approve?: boolean; apiKey?: boolean }): Promise<TenantWorld>;
  createStaff(tenant: TenantWorld, role: "owner" | "admin" | "operator" | "viewer"): Promise<{ token: string; sub: string }>;
  createConsumer(): Promise<ConsumerWorld>;
  createPaymentMethod(consumer: ConsumerWorld, scenario?: string): Promise<{ id: string }>;
  seedSubscriptions(tenant: TenantWorld, planId: string, count: number): Promise<{ seedId: string; firstRenewalDate: string }>;
}

let seq = 0;

export function createWorld(api: Api, tokens: Tokens, wait: Wait, testTag: string): World {
  const ops = async () => tokens.operator();
  const createStaff: World["createStaff"] = async (tenant, role) => {
    const t = await tokens.issue({ pool: "staff" });
    const res = await api.call("POST", `/v1/ops/tenants/${tenant.id}/staff`, {
      token: await ops(),
      body: { subject: t.sub, email: t.email, display_name: `${role} ${testTag}`, role },
    });
    expect(res.status, JSON.stringify(res.body)).toBe(201);
    return { token: t.token, sub: t.sub };
  };
  return {
    async createTenant(opts = {}) {
      const name = `tenant ${testTag} ${++seq}`;
      const op = await ops();
      const created = await api.call<{ id: string }>("POST", "/v1/ops/tenants", { token: op, body: { name } });
      expect(created.status, JSON.stringify(created.body)).toBe(201);
      const id = created.body.id;
      if (opts.approve !== false) {
        const approved = await api.call("POST", `/v1/ops/tenants/${id}/approve`, { token: op, body: { fincode_shop_id: `shop_${id.replaceAll("-", "")}` } });
        expect(approved.status, JSON.stringify(approved.body)).toBe(200);
      }
      const tenant: TenantWorld = { id, name, ownerToken: "", ownerSub: "" };
      const owner = await createStaff(tenant, "owner");
      tenant.ownerToken = owner.token;
      tenant.ownerSub = owner.sub;
      if (opts.apiKey) {
        const key = await api.call<{ api_key: string }>("POST", "/v1/console/api-keys", { token: owner.token, tenantId: id, body: { name: `key ${testTag}` } });
        expect(key.status, JSON.stringify(key.body)).toBe(201);
        tenant.apiKey = key.body.api_key;
      }
      return tenant;
    },
    createStaff,
    async createConsumer() {
      const t = await tokens.issue({ pool: "consumer" });
      const me = await api.call<{ id: string }>("GET", "/v1/me", { token: t.token });
      expect(me.status, JSON.stringify(me.body)).toBe(200);
      return { token: t.token, sub: t.sub, email: t.email, customerAccountId: me.body.id };
    },
    async createPaymentMethod(consumer, scenario = "ok") {
      const token = `tok_stub_${scenario}_4242_1230_${Math.random().toString(36).slice(2, 10).padEnd(8, "0")}`;
      const res = await api.call<{ id: string }>("POST", "/v1/me/payment-methods", {
        token: consumer.token,
        idempotencyKey: `pm-${testTag}-${++seq}`,
        body: { method_type: "card", provider: "stub", token },
      });
      expect(res.status, JSON.stringify(res.body)).toBe(201);
      return { id: res.body.id };
    },
    async seedSubscriptions(tenant, planId, count) {
      const res = await api.call<{ seed_id: string; run_id: string }>("POST", "/v1/dev/seed/subscriptions", { body: { tenant_id: tenant.id, plan_id: planId, count } });
      expect(res.status, JSON.stringify(res.body)).toBe(202);
      const done = await wait.until(async () => {
        const r = await api.call<{ status: string; stats: Record<string, unknown> }>("GET", `/v1/dev/job-runs/${res.body.run_id}`);
        if (r.body.status === "failed") throw new Error(`seed failed: ${JSON.stringify(r.body)}`);
        return r.body.status === "succeeded" ? r.body : undefined;
      }, { timeout: 30 * 60_000, interval: 2_000, message: "一括シードの完了" });
      return { seedId: res.body.seed_id, firstRenewalDate: String(done.stats.first_renewal_date) };
    },
  };
}
