// AC-PLT-043：X-Correlation-Id 付きの申込から、購読者が出した後続のイベントまで同じ相関 ID が引き継がれ、
// 後続の causation_id は受け取ったイベントの event_id になる（NFR-013・NFR-017。sp_e2e_reader で確認）。
// 同期の申込はイベントをすべて API の中で書くので、結果不明の申込（202）を運営者が失敗として確定し、
// payments.payment_failed → billing（invoice_payment_failed）→ orchestration（signup_failed・invoice_voided）の購読の連鎖を見る。
import { test, expect } from "../../fixtures/index.ts";
import { createPlan } from "../../helpers/catalog.ts";
import { checkout, type Consumer } from "../../helpers/orchestration.ts";

type Row = { id: string; event_type: string; correlation_id: string; causation_id: string | null };

test("AC-PLT-043: 申込の Outbox の行と、購読者が出した後続のイベントがすべて同じ相関 ID を持ち、causation_id は受け取った event_id", async ({ api, world, db, wait, tokens }) => {
  const C = api.correlationId; // api fixture はすべての要求に X-Correlation-Id: C を付ける
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const consumer = (await world.createConsumer()) as Consumer;
  const pm = await world.createPaymentMethod(consumer, "timeout_lost");
  const res = await checkout(api, consumer, tenant, plan.id, pm.id);
  expect(res.status, JSON.stringify(res.body)).toBe(202);

  const [attempt] = await wait.until(
    async () => {
      const r = await db.query<{ id: string }>("SELECT id FROM payments.payment_attempts WHERE tenant_id = $1 AND status = 'unknown'", [tenant.id]);
      return r.length === 1 ? r : undefined;
    },
    { message: "結果不明の試行" },
  );
  const resolved = await api.call("POST", `/v1/ops/payment-attempts/${attempt!.id}/resolve`, {
    token: await tokens.operator(),
    body: { outcome: "failed", failure_code: "card_declined", retryable: false, note: "AC-PLT-043" },
  });
  expect(resolved.status, JSON.stringify(resolved.body)).toBe(200);
  // 連鎖の最後（orchestration の購読者が出す）が送信済みになるまで待つ
  await wait.until(
    async () => (await db.count("SELECT count(*) FROM subscription.outbox_events WHERE tenant_id = $1 AND event_type = 'subscription.signup_failed' AND published_at IS NOT NULL", [tenant.id])) === 1 &&
      (await db.count("SELECT count(*) FROM billing.outbox_events WHERE tenant_id = $1 AND event_type = 'billing.invoice_voided' AND published_at IS NOT NULL", [tenant.id])) === 1,
    { message: "orchestration の購読で signup_failed・invoice_voided" },
  );

  const one = async (schema: string, type: string): Promise<Row> => {
    const rows = await db.query<Row>(`SELECT id::text, event_type, correlation_id, causation_id FROM ${schema}.outbox_events WHERE tenant_id = $1 AND event_type = $2`, [tenant.id, type]);
    expect(rows, `${type} は 1 件`).toHaveLength(1);
    return rows[0]!;
  };
  // 申込で書かれた行
  const issued = await one("billing", "billing.invoice_issued");
  const unknown = await one("payments", "payments.payment_outcome_unknown");
  // 運営者の確定（同じ相関 ID の要求）と、購読者が出した後続のイベント
  const failed = await one("payments", "payments.payment_failed");
  const invFailed = await one("billing", "billing.invoice_payment_failed");
  const signupFailed = await one("subscription", "subscription.signup_failed");
  const voided = await one("billing", "billing.invoice_voided");

  for (const r of [issued, unknown, failed, invFailed, signupFailed, voided]) expect(r.correlation_id, r.event_type).toBe(C);
  expect(invFailed.causation_id).toBe(failed.id); // billing は payments.payment_failed を受けて出した
  expect(signupFailed.causation_id).toBe(invFailed.id); // orchestration は billing.invoice_payment_failed を受けて出した
  expect(voided.causation_id).toBe(invFailed.id);
});
