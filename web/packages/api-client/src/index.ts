// OpenAPI（api/openapi）から生成した型（`task generate` で作り直す）。手書きの型を作らない。
export type { paths as DevPaths, components as DevComponents } from "./gen/dev.ts";
export type { paths as OpsTenantPaths, components as OpsTenantComponents } from "./gen/ops-tenant.ts";
export type { paths as ConsoleTenantPaths, components as ConsoleTenantComponents } from "./gen/console-tenant.ts";
export type { paths as MeCustomerPaths, components as MeCustomerComponents } from "./gen/me-customer.ts";
export type { paths as ConsoleCatalogPaths, components as ConsoleCatalogComponents } from "./gen/console-catalog.ts";
export type { paths as CheckoutCatalogPaths, components as CheckoutCatalogComponents } from "./gen/checkout-catalog.ts";
export type { paths as MeSubscriptionPaths, components as MeSubscriptionComponents } from "./gen/me-subscription.ts";
export type { paths as ConsoleSubscriptionPaths, components as ConsoleSubscriptionComponents } from "./gen/console-subscription.ts";
export type { paths as TenantSubscriptionPaths, components as TenantSubscriptionComponents } from "./gen/tenant-subscription.ts";
export type { ErrorCode } from "./error-codes.ts";
export { errorStatus } from "./error-codes.ts";

/** application/problem+json の本文（api/openapi/common.yaml の Problem）。 */
export interface Problem {
  type: string;
  title: string;
  status: number;
  detail?: string;
  code: string;
  request_id: string;
  correlation_id: string;
  errors?: { field: string; code: string }[];
}
