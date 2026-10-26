// operator-console のブラウザのテスト（時刻を進めるもの）：FR-004・FR-005（停止と一括解約）・FR-006（結果が不明な決済の確定）・
// FR-007（照合の不整合を対応済みに）・FR-010（Webhook の再送）。
import { test, expect } from "../../fixtures/index.ts";
import { createPlan } from "../../helpers/catalog.ts";
import { sinkEndpoint, untilDelivery } from "../../helpers/integration.ts";
import { OPS, signInAsOperator } from "../../helpers/ops.ts";
import { JOB, startAtNext15, subscribe } from "../../helpers/orchestration.ts";

test("FR-004・FR-005: 事業者名の確認つきで停止すると、一括解約のジョブの件数が更新され、終わると「完了」になる", async ({ page, api, world, clock }) => {
  await startAtNext15(clock);
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  await subscribe(api, world, tenant, plan.id);
  await subscribe(api, world, tenant, plan.id);
  await signInAsOperator(page, "suspend");
  await page.goto(`${OPS}/tenants/${tenant.id}`);
  await expect(page.getByTestId("tenant-status")).toHaveText("有効");

  await page.getByRole("button", { name: "停止する" }).click();
  const dialog = page.getByRole("dialog", { name: "事業者を停止" });
  await expect(dialog).toContainText("一括解約が始まります");
  await dialog.getByLabel("停止の理由").fill("規約違反（E2E）");
  await dialog.getByLabel(/確認のため/).fill(`${tenant.name} 違う`);
  await dialog.getByRole("button", { name: "停止する" }).click();
  await expect(dialog.getByText("事業者名が一致しません")).toBeVisible();
  await expect(page.getByTestId("tenant-status")).toHaveText("有効");

  await dialog.getByLabel(/確認のため/).fill(tenant.name);
  await dialog.getByRole("button", { name: "停止する" }).click();
  await expect(page.getByTestId("tenant-status")).toHaveText("停止中");
  await expect(page.getByText("規約違反（E2E）")).toBeVisible();

  // 一括解約のジョブ（毎分）を手で回す。画面は実行中のあいだ 5 秒ごとに読み直す
  const job = page.getByTestId("bulk-job");
  await expect(async () => {
    await clock.runJob(JOB.bulk, { force: true });
    await expect(job).toContainText("完了", { timeout: 6_000 });
  }).toPass({ timeout: 60_000 });
  await expect(page.getByTestId("bulk-processed")).toHaveText("2 件");
  await expect(job).toContainText("期間の終了で解約");
});

test("FR-006: 結果が不明なまま 30 秒以上たった決済を、根拠のメモとともに失敗として確定すると、一覧から消える", async ({ page, api, world, clock, db, wait }) => {
  await startAtNext15(clock);
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  expect((await world.seedSubscriptions(tenant, plan.id, 1, "timeout_lost")).failed).toBe(1);
  await wait.until(async () => (await db.count("SELECT count(*) FROM payments.payment_attempts WHERE tenant_id = $1 AND status = 'unknown'", [tenant.id])) === 1, {
    message: "結果不明の試行",
  });
  await clock.advance("1m");
  await signInAsOperator(page, "resolve");
  await page.getByRole("link", { name: "結果が不明な決済" }).click();
  const row = page.getByRole("row").filter({ hasText: tenant.name });
  await expect(row).toHaveCount(1);
  await expect(row).toContainText("結果不明");
  await expect(row).toContainText("980円");
  await row.click();

  await page.getByRole("button", { name: "手動で確定" }).click();
  const dialog = page.getByRole("dialog", { name: "決済を手動で確定" });
  await dialog.getByLabel("失敗（決済されていない）").check();
  await dialog.getByLabel("失敗の理由").click();
  await page.getByRole("option", { name: /^not_processed/ }).click();
  await dialog.getByLabel("根拠のメモ").fill("fincode の管理画面で未処理を確認");
  await dialog.getByRole("button", { name: "確定する" }).click();
  await expect(dialog.getByText("決済代行で確認した結果であることを確かめてください")).toBeVisible();
  await dialog.getByLabel("決済代行の管理画面などで確認した結果です").check();
  await dialog.getByRole("button", { name: "確定する" }).click();
  await expect(page.getByTestId("attempt-status")).toHaveText("失敗");
  await expect(page.getByText("fincode の管理画面で未処理を確認")).toBeVisible();
  await expect(page.getByText("手動", { exact: true })).toBeVisible();

  await page.getByRole("link", { name: "結果が不明な決済" }).first().click();
  await expect(page.getByRole("heading", { name: "結果が不明な決済" })).toBeVisible();
  await expect(page.getByRole("row").filter({ hasText: tenant.name })).toHaveCount(0);
});

test("FR-007: 照合の不整合を、メモを入力して対応済みにすると、状態が変わりメモと運営者が表示される。2 回目の対応済みは何もしない", async ({ page, api, world, clock, db, wait, tokens }) => {
  const D = await startAtNext15(clock);
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  expect((await world.seedSubscriptions(tenant, plan.id, 1, "timeout_lost")).failed).toBe(1);
  const [attempt] = await wait.until(
    async () => {
      const r = await db.query<{ id: string; provider_order_ref: string }>("SELECT id, provider_order_ref FROM payments.payment_attempts WHERE tenant_id = $1 AND status = 'unknown'", [tenant.id]);
      return r.length === 1 ? r : undefined;
    },
    { message: "結果不明の試行" },
  );
  // 決済代行に記録のない試行を、自社だけ成功として確定する（翌日の照合で local_succeeded_provider_not になる）
  const op = await tokens.operator();
  const forced = await api.call("POST", `/v1/ops/payment-attempts/${attempt!.id}/resolve`, { token: op, body: { outcome: "succeeded", note: "E2E：不整合を作る", force: true } });
  expect(forced.status, JSON.stringify(forced.body)).toBe(200);
  await clock.advance("24h");
  await clock.runJob("payments-reconcile-daily", { force: true });
  const [disc] = await wait.until(
    async () => {
      const r = await db.query<{ id: string; kind: string }>("SELECT id, kind FROM payments.reconciliation_discrepancies WHERE attempt_id = $1", [attempt!.id]);
      return r.length === 1 ? r : undefined;
    },
    { message: "照合の不整合" },
  );
  expect(disc!.kind).toBe("local_succeeded_provider_not");

  const me = await signInAsOperator(page, "reconcile");
  await page.goto(`${OPS}/payments/reconciliation?status=open&business_date=${D}`);
  const row = page.getByRole("row").filter({ hasText: attempt!.provider_order_ref });
  await expect(row).toContainText("自社は成功・決済代行に記録なし");
  await expect(row).toContainText(tenant.name);
  await row.click();
  const drawer = page.getByRole("dialog", { name: "照合の不整合" });
  await expect(drawer.getByRole("link", { name: "試行の詳細を開く" })).toHaveAttribute("href", `/ops/payments/attempts/${attempt!.id}`);
  await drawer.getByLabel("対応のメモ").fill("代行の管理画面で返金済みを確認");
  await drawer.getByRole("button", { name: "対応済みにする" }).click();
  await expect(page.getByText("対応済みにしました")).toBeVisible();
  await expect(page.getByRole("row").filter({ hasText: attempt!.provider_order_ref })).toHaveCount(0);

  // 対応済みで絞ると出て、開くとメモと運営者がわかる（監査ログから読む。ADR-O02）
  await page.getByText("対応済み", { exact: true }).first().click();
  await page.getByRole("row").filter({ hasText: attempt!.provider_order_ref }).click();
  await expect(page.getByTestId("ack-note")).toContainText("代行の管理画面で返金済みを確認");
  await expect(page.getByTestId("ack-note")).toContainText(me.sub);

  // API：2 回目は 200 で何もしない（監査は 1 件だけが状態を変える）。スタッフは 403、メモなしは 400
  const again = await api.call<{ status: string }>("POST", `/v1/ops/reconciliation-reports/${disc!.id}/acknowledge`, { token: op, body: { note: "もう一度" } });
  expect(again.status).toBe(200);
  expect(again.body.status).toBe("acknowledged");
  expect((await api.call("POST", `/v1/ops/reconciliation-reports/${disc!.id}/acknowledge`, { token: tenant.ownerToken, body: { note: "x" } })).status).toBe(403);
  expect((await api.call("POST", `/v1/ops/reconciliation-reports/${disc!.id}/acknowledge`, { token: op, body: { note: "" } })).status).toBe(400);
  expect((await api.call("POST", `/v1/ops/reconciliation-reports/${crypto.randomUUID()}/acknowledge`, { token: op, body: { note: "x" } })).status).toBe(404);
  const audits = await db.query<{ after: { note?: string } }>(
    "SELECT after FROM audit.audit_records WHERE action = 'reconciliation.acknowledge' AND target_id = $1 AND outcome = 'succeeded' ORDER BY occurred_at",
    [disc!.id],
  );
  expect(audits[0]!.after.note).toBe("代行の管理画面で返金済みを確認");
});

test("FR-010: 失敗した Webhook の配信を再送すると、手動の新しい配信が作られる", async ({ page, api, world, clock, sink, db, wait }) => {
  await startAtNext15(clock);
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const s = await sinkEndpoint(api, sink, tenant, ["subscription.started"]);
  await sink.setBehavior(s.sinkId, [{ status: 500 }]);
  await subscribe(api, world, tenant, plan.id);
  await untilDelivery(db, wait, s.endpoint.id, (d) => d.attempt_count === 1, "1 回目の記録");
  await clock.advance("73h");
  const failed = await untilDelivery(db, wait, s.endpoint.id, (d) => d.status === "failed", "failed になる");
  await sink.setBehavior(s.sinkId, []);

  await signInAsOperator(page, "redeliver");
  await page.goto(`${OPS}/tenants/${tenant.id}`);
  await page.getByRole("link", { name: "Webhook の配信" }).click();
  const row = page.getByRole("row").filter({ hasText: failed.id.slice(0, 8) });
  await expect(row).toContainText("失敗");
  await row.getByRole("button", { name: "再送" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "再送する" }).click();
  await expect(page.getByText("再送の配信を作りました")).toBeVisible();

  await page.getByText("すべて", { exact: true }).click();
  await expect(page.getByRole("row").filter({ hasText: "手動の再送" })).toHaveCount(1);
  await untilDelivery(db, wait, s.endpoint.id, (d) => d.status === "succeeded", "手動の配信が succeeded");
});
