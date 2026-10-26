// API の型（@sp/api-client の生成物の別名）。
import type { OpsAuditComponents, OpsOrchestrationComponents, OpsPaymentsComponents, OpsTenantComponents, ConsoleIntegrationComponents } from "@sp/api-client";

export type Tenant = OpsTenantComponents["schemas"]["Tenant"];
export type Attempt = OpsPaymentsComponents["schemas"]["Attempt"];
export type BulkJob = OpsOrchestrationComponents["schemas"]["BulkJob"];
export type AuditRecord = OpsAuditComponents["schemas"]["AuditRecord"];
export type Delivery = ConsoleIntegrationComponents["schemas"]["Delivery"];
export interface Discrepancy {
  id: string;
  business_date: string;
  provider: string;
  kind: string;
  attempt_id: string | null;
  tenant_id: string | null;
  provider_order_ref: string;
  detail: Record<string, unknown>;
  status: "open" | "acknowledged";
  detected_at: string;
}
export interface BillingRuns {
  run_date: string;
  runs: { id: string; seq: number; trigger: string; status: string; enqueued_count: number; resume_count: number; started_at: string; finished_at: string | null }[];
  outcomes: { paid: number; payment_failed: number; ended: number; in_progress: number; failed: number };
}
export interface Page<T> {
  data: T[];
  has_more: boolean;
  next_cursor: string | null;
}
