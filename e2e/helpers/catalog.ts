// catalog の E2E で使う準備（商品・プラン・クーポンを console API で作る）。
import { expect } from "@playwright/test";
import type { CheckoutCatalogComponents, ConsoleCatalogComponents } from "@sp/api-client";
import type { Api } from "../fixtures/api.ts";
import type { TenantWorld } from "../fixtures/world.ts";

export type Plan = ConsoleCatalogComponents["schemas"]["Plan"];
export type Product = ConsoleCatalogComponents["schemas"]["Product"];
export type Coupon = ConsoleCatalogComponents["schemas"]["Coupon"];
export type PriceVersion = ConsoleCatalogComponents["schemas"]["PriceVersion"];
export type CheckoutPlan = CheckoutCatalogComponents["schemas"]["CheckoutPlan"];
export type CouponPreview = CheckoutCatalogComponents["schemas"]["CouponPreview"];
export type List<T> = { data: T[]; has_more: boolean; next_cursor: string | null };

export interface PlanOptions {
  amount?: number;
  interval?: "month" | "year";
  trialDays?: number;
  publish?: boolean;
  token?: string;
}

/** console 用の呼び出し（owner のトークンと X-Tenant-Id）。 */
export function consoleOf(api: Api, tenant: TenantWorld, token = tenant.ownerToken) {
  return <T = unknown>(method: string, path: string, body?: unknown, query?: Record<string, string | number | undefined>) =>
    api.call<T>(method, path, { token, tenantId: tenant.id, body, query });
}

export async function createProduct(api: Api, tenant: TenantWorld, name = "商品"): Promise<Product> {
  const res = await consoleOf(api, tenant)<Product>("POST", "/v1/console/products", { name });
  expect(res.status, JSON.stringify(res.body)).toBe(201);
  return res.body;
}

/** 商品とプランを作る。publish: true（既定）なら公開する（事業者が active であること）。 */
export async function createPlan(api: Api, tenant: TenantWorld, o: PlanOptions = {}): Promise<Plan> {
  const call = consoleOf(api, tenant, o.token);
  const product = await createProduct(api, tenant);
  const res = await call<Plan>("POST", "/v1/console/plans", {
    product_id: product.id,
    name: "月額プラン",
    billing_interval: o.interval ?? "month",
    trial_days: o.trialDays ?? 0,
    amount: o.amount ?? 980,
  });
  expect(res.status, JSON.stringify(res.body)).toBe(201);
  if (o.publish === false) return res.body;
  const pub = await call<Plan>("POST", `/v1/console/plans/${res.body.id}/publish`);
  expect(pub.status, JSON.stringify(pub.body)).toBe(200);
  return pub.body;
}

export interface CouponOptions {
  code?: string;
  type?: "amount" | "percent";
  value?: number;
  cycles?: number;
  planIds?: string[];
  expiresAt?: string;
  max?: number;
}

export async function createCoupon(api: Api, tenant: TenantWorld, o: CouponOptions = {}): Promise<Coupon> {
  const res = await consoleOf(api, tenant)<Coupon>("POST", "/v1/console/coupons", {
    code: o.code ?? `C${crypto.randomUUID().replaceAll("-", "").slice(0, 12)}`,
    name: "テストクーポン",
    discount: { type: o.type ?? "percent", value: o.value ?? 50, duration_cycles: o.cycles ?? 1 },
    applies_to: o.planIds ? "selected_plans" : "all_plans",
    plan_ids: o.planIds,
    expires_at: o.expiresAt,
    max_redemptions: o.max,
  });
  expect(res.status, JSON.stringify(res.body)).toBe(201);
  return res.body;
}

/** 業務時刻（dev clock）の now。クーポンの期限は業務時刻で判定するので、実時刻ではなくこれを基準にする。 */
export async function businessNow(api: Api): Promise<Date> {
  const res = await api.dev.getClock();
  return new Date((res.body as { now: string }).now);
}
