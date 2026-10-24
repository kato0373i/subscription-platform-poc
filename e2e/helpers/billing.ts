// billing の E2E で使う準備。請求は dev の一括シード（段階 2 の後半。billing.IssueInvoice → payments.Collect → RecordPaymentOutcome）で作る。
import type { ConsoleBillingComponents, MeBillingComponents } from "@sp/api-client";

export type MeInvoice = MeBillingComponents["schemas"]["MeInvoice"];
export type Invoice = ConsoleBillingComponents["schemas"]["Invoice"];
export type DunningPolicy = ConsoleBillingComponents["schemas"]["DunningPolicy"];
