// API の型（@sp/api-client の生成物の別名）。
import type { MeBillingComponents, MeCustomerComponents, MePaymentsComponents, MeSubscriptionComponents } from "@sp/api-client";

export type Subscription = MeSubscriptionComponents["schemas"]["MeSubscription"];
export type Status = MeSubscriptionComponents["schemas"]["Status"];
export type PaymentMethod = MePaymentsComponents["schemas"]["PaymentMethod"];
export type Invoice = MeBillingComponents["schemas"]["MeInvoice"];
export type Me = MeCustomerComponents["schemas"]["Account"];
export interface PlanOption {
  plan_id: string;
  name: string;
  billing_interval: "month" | "year";
  amount: number;
  effective_date: string;
}
export interface Page<T> {
  data: T[];
  has_more: boolean;
  next_cursor: string | null;
}
export const intervalLabel: Record<string, string> = { month: "月", year: "年" };
