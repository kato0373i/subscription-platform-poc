// Orchestration（申込・更新・回収）の E2E の準備（orchestration.md §11b.1）。契約は本物の申込 API で作る。
import { randomUUID } from "node:crypto";
import { expect } from "@playwright/test";
import type { MeOrchestrationComponents } from "@sp/api-client";
import type { Api, ApiResult } from "../fixtures/api.ts";
import type { Db } from "../fixtures/db.ts";
import type { Wait } from "../fixtures/wait.ts";
import type { TenantWorld, World } from "../fixtures/world.ts";

export type CheckoutSubscription = MeOrchestrationComponents["schemas"]["CheckoutSubscription"];
export type Checkout201 = { subscription: CheckoutSubscription; invoice: { id: string; total: number; tax: number; status: string } | null };

export interface Consumer {
  token: string;
  sub: string;
  email: string;
  customerAccountId: string;
}

export interface CheckoutOptions {
  coupon?: string;
  expectTrial?: boolean;
  key?: string;
  paymentMethodId?: string;
}

/** POST /v1/me/subscriptions。毎回新しい Idempotency-Key（opts.key で固定できる）。 */
export async function checkout(api: Api, consumer: Consumer, tenant: TenantWorld, planId: string, pmId: string, o: CheckoutOptions = {}): Promise<ApiResult<Checkout201>> {
  return api.call<Checkout201>("POST", "/v1/me/subscriptions", {
    token: consumer.token,
    idempotencyKey: o.key ?? randomUUID(),
    body: { tenant_id: tenant.id, plan_id: planId, payment_method_id: o.paymentMethodId ?? pmId, coupon_code: o.coupon, expect_trial: o.expectTrial },
  });
}

/** 新しい契約者・支払方法を作って申込を成功させる（201）。 */
export async function subscribe(api: Api, world: World, tenant: TenantWorld, planId: string, scenario = "ok", o: CheckoutOptions = {}) {
  const consumer = (await world.createConsumer()) as Consumer;
  const pm = await world.createPaymentMethod(consumer, scenario);
  const res = await checkout(api, consumer, tenant, planId, pm.id, o);
  expect(res.status, JSON.stringify(res.body)).toBe(201);
  return { consumer, pm, subscription: res.body.subscription, invoice: res.body.invoice };
}

/** StubAdapter の支払方法の結果を remaining 回だけ上書きする（payments.md §11.2 の setBehavior。0 で解除）。 */
export async function setBehavior(api: Api, pmId: string, scenario: string, remaining: number): Promise<void> {
  const res = await api.call("PUT", `/v1/dev/payments/stub/payment-methods/${pmId}/behavior`, { body: { scenario, remaining } });
  expect(res.status, JSON.stringify(res.body)).toBe(204);
}

export async function meSubscription(api: Api, consumer: Consumer, id: string) {
  const res = await api.call<{ id: string; status: string; entitled: boolean; next_renewal_date: string | null; plan_id: string; current_period: { start: string; end: string } }>(
    "GET",
    `/v1/me/subscriptions/${id}`,
    { token: consumer.token },
  );
  expect(res.status, JSON.stringify(res.body)).toBe(200);
  return res.body;
}

/** 契約の状態が status になるまで待つ（非同期の結果の反映）。 */
export async function untilStatus(api: Api, wait: Wait, consumer: Consumer, id: string, status: string) {
  return wait.until(
    async () => {
      const s = await meSubscription(api, consumer, id);
      return s.status === status ? s : undefined;
    },
    { message: `契約 ${id} が ${status} になる` },
  );
}

// ---- DB の共通の確認（§11b.1） ----

/** どの請求も成功した試行・stub_charges の成功が 1 以下。 */
export async function expectNoDoubleCharge(db: Db, tenantId: string) {
  const rows = await db.query<{ payable_id: string; n: number }>(
    "SELECT payable_id, count(*)::int AS n FROM payments.payment_attempts WHERE tenant_id = $1 AND status = 'succeeded' GROUP BY payable_id HAVING count(*) > 1",
    [tenantId],
  );
  expect(rows, "成功した試行が 2 件以上の請求").toEqual([]);
  const charges = await db.query(
    `SELECT a.payable_id, count(*)::int AS n FROM payments.stub_charges c JOIN payments.payment_attempts a ON c.order_ref = a.provider_order_ref
     WHERE a.tenant_id = $1 AND c.true_status = 'succeeded' GROUP BY a.payable_id HAVING count(*) > 1`,
    [tenantId],
  );
  expect(charges, "代行側で 2 回以上成功した請求").toEqual([]);
}

/** (subscription_id, period_start) ごとに請求 1 件。 */
export async function expectOneInvoicePerPeriod(db: Db, tenantId: string) {
  const rows = await db.query(
    "SELECT subscription_id, period_start FROM billing.invoices WHERE tenant_id = $1 GROUP BY subscription_id, period_start HAVING count(*) > 1",
    [tenantId],
  );
  expect(rows).toEqual([]);
}

/** 自分のテナントの Outbox が送信済みになる。 */
export async function expectOutboxDrained(db: Db, wait: Wait, tenantId: string) {
  await wait.until(
    async () => {
      let pending = 0;
      for (const s of ["subscription", "billing", "payments", "catalog", "tenant", "orchestration"]) {
        pending += await db.count(`SELECT count(*) FROM ${s}.outbox_events WHERE tenant_id = $1 AND published_at IS NULL`, [tenantId]);
      }
      return pending === 0;
    },
    { message: "Outbox の送信" },
  );
}

/** failed・非終端のプロセスがない（待ちを期待するシナリオでは呼ばない）。 */
export async function expectNoStuckProcess(db: Db, wait: Wait, tenantId: string) {
  await wait.until(async () => (await db.count("SELECT count(*) FROM orchestration.billing_cycle_processes WHERE tenant_id = $1 AND state <> 'completed'", [tenantId])) === 0, {
    message: "プロセスが閉じる",
  });
}

export async function expectInvariants(db: Db, wait: Wait, tenantId: string) {
  await expectOutboxDrained(db, wait, tenantId);
  await expectNoDoubleCharge(db, tenantId);
  await expectOneInvoicePerPeriod(db, tenantId);
}

/** 日付の演算（YYYY-MM-DD）。 */
export function addDays(date: string, days: number): string {
  const d = new Date(`${date}T00:00:00Z`);
  d.setUTCDate(d.getUTCDate() + days);
  return d.toISOString().slice(0, 10);
}

/** 1 か月後の同じ日（D は 15 日に揃えるので月末の補正はない）。 */
export function addMonths(date: string, months: number): string {
  // 月末に丸める（1/29 + 1 か月は平年なら 2/28。Date の setUTCMonth は 3/1 にあふれるので使わない。業務の更新日の規則と同じ）
  const [y, m, day] = date.split("-").map(Number) as [number, number, number];
  const total = y * 12 + (m - 1) + months;
  const ny = Math.floor(total / 12);
  const nm = (total % 12) + 1;
  const last = new Date(Date.UTC(ny, nm, 0)).getUTCDate();
  return `${ny}-${String(nm).padStart(2, "0")}-${String(Math.min(day, last)).padStart(2, "0")}`;
}

/** JST の日時の ISO 文字列。 */
export function jst(date: string, hhmm: string): string {
  return `${date}T${hhmm}:00+09:00`;
}

/** clock テストの基準日 D：現在の dev clock より後の、次の 15 日の 09:00 JST に進める（§11b.1）。 */
export async function startAtNext15(clock: { now(): Promise<{ today: string }>; setTo(t: string): Promise<unknown> }): Promise<string> {
  const { today } = await clock.now();
  const d = new Date(`${today}T00:00:00Z`);
  if (d.getUTCDate() >= 15) d.setUTCMonth(d.getUTCMonth() + 1);
  d.setUTCDate(15);
  const D = d.toISOString().slice(0, 10);
  await clock.setTo(jst(D, "09:00"));
  return D;
}

export const JOB = {
  dispatch: "orchestration-renewal-dispatch",
  collectionDue: "billing-collection-due",
  bulk: "orchestration-bulk-cancellation",
  resolveUnknown: "payments-resolve-unknown",
  sweeper: "orchestration-process-sweeper",
} as const;
