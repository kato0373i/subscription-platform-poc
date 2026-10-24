// OpenAPI（api/openapi）から生成した型（`task generate` で作り直す）。手書きの型を作らない。
export type { paths as DevPaths, components as DevComponents } from "./gen/dev.ts";
export type { paths as OpsTenantPaths, components as OpsTenantComponents } from "./gen/ops-tenant.ts";
export type { paths as ConsoleTenantPaths, components as ConsoleTenantComponents } from "./gen/console-tenant.ts";
export type { paths as MeCustomerPaths, components as MeCustomerComponents } from "./gen/me-customer.ts";
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
