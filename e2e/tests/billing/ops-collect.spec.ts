// billing.md §7.3（POST /v1/ops/invoices/{id}/collect）：409 の 2 種と 404。
// AC-BIL-038 の 202（scheduled の請求の collection_seq+1）は、scheduled の請求を作る更新課金（orchestration）の実装後に追加する（サービス層は integration で確認済み）。
import { test, expect } from "../../fixtures/index.ts";
import { createPlan } from "../../helpers/catalog.ts";

test("paid・caller_managed の請求は 409 invoice_not_collectible、payment_unknown は 409 collection_in_progress、存在しなければ 404", async ({ api, world, tokens, db, wait }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const op = await tokens.operator();
  await world.seedSubscriptions(tenant, plan.id, 1);
  await world.seedSubscriptions(tenant, plan.id, 1, "timeout_lost");
  const rows = await db.query<{ id: string; status: string }>("SELECT id, status FROM billing.invoices WHERE tenant_id = $1 ORDER BY status", [tenant.id]);
  const paid = rows.find((r) => r.status === "paid")!;
  const unknown = rows.find((r) => r.status === "payment_unknown")!;
  expect(paid).toBeDefined();
  expect(unknown).toBeDefined();

  const r1 = await api.call("POST", `/v1/ops/invoices/${paid.id}/collect`, { token: op, body: { note: "確認" } });
  expect(r1.status).toBe(409);
  expect(r1.problem?.code).toBe("invoice_not_collectible");
  const r2 = await api.call("POST", `/v1/ops/invoices/${unknown.id}/collect`, { token: op });
  expect(r2.status).toBe(409);
  expect(r2.problem?.code).toBe("collection_in_progress");
  expect((await api.call("POST", `/v1/ops/invoices/${crypto.randomUUID()}/collect`, { token: op })).status).toBe(404);

  // 結果不明の試行を ops で failed に確定すると、payments.payment_failed の購読で請求が open に戻る（caller_managed なのでダニングしない）
  const [attempt] = await db.query<{ id: string }>("SELECT id FROM payments.payment_attempts WHERE payable_id = $1", [unknown.id]);
  const resolved = await api.call("POST", `/v1/ops/payment-attempts/${attempt!.id}/resolve`, {
    token: op,
    body: { outcome: "failed", failure_code: "card_declined", retryable: true, note: "代行に注文なし" },
  });
  expect(resolved.status, JSON.stringify(resolved.body)).toBe(200);
  const reopened = await wait.until(async () => {
    const [r] = await db.query<{ status: string; dunning_stage: number; attempt_count: number }>("SELECT status, dunning_stage, attempt_count FROM billing.invoices WHERE id = $1", [unknown.id]);
    return r!.status === "open" ? r : undefined;
  }, { message: "payments.payment_failed の購読で請求に反映される" });
  expect(reopened).toMatchObject({ dunning_stage: 0, attempt_count: 1 });
  const r3 = await api.call("POST", `/v1/ops/invoices/${unknown.id}/collect`, { token: op });
  expect(r3.problem?.code).toBe("invoice_not_collectible"); // caller_managed
});
