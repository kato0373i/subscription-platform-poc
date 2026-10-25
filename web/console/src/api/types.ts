// API の型（@sp/api-client の生成物の別名）。
import type { ConsoleBillingComponents, ConsoleCatalogComponents, ConsoleIntegrationComponents, ConsoleSubscriptionComponents, ConsoleTenantComponents } from "@sp/api-client";

export type Product = ConsoleCatalogComponents["schemas"]["Product"];
export type Plan = ConsoleCatalogComponents["schemas"]["Plan"];
export type PriceVersion = ConsoleCatalogComponents["schemas"]["PriceVersion"];
export type Coupon = ConsoleCatalogComponents["schemas"]["Coupon"];
export type Subscription = ConsoleSubscriptionComponents["schemas"]["Subscription"];
export type Invoice = ConsoleBillingComponents["schemas"]["Invoice"];
export type DunningPolicy = ConsoleBillingComponents["schemas"]["DunningPolicy"];
export type Endpoint = ConsoleIntegrationComponents["schemas"]["Endpoint"];
export type Delivery = ConsoleIntegrationComponents["schemas"]["Delivery"];
export type Attempt = ConsoleIntegrationComponents["schemas"]["Attempt"];
export type Staff = ConsoleTenantComponents["schemas"]["Staff"];
export type ApiKey = ConsoleTenantComponents["schemas"]["ApiKey"];
export interface Page<T> {
  data: T[];
  has_more: boolean;
  next_cursor: string | null;
}

export const intervalLabel: Record<string, string> = { month: "月", year: "年" };
