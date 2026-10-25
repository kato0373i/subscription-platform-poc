// E2E-020 更新課金 1 万件のスループット（計測のみ。orchestration.md §11b.4）。pnpm e2e:perf で流す（既定の実行には含めない）。
// 処理時間は合否にしない（参考値）。二重課金がないこと・期間ごとの請求が 1 件であることだけを合否にする。
// 件数は E2E_PERF_COUNT で変えられる（既定 10,000）。dev clock を進めるので clock project と同時に流さない。
import { execSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import { test, expect } from "../../fixtures/index.ts";
import { createPlan } from "../../helpers/catalog.ts";
import { JOB, expectNoDoubleCharge, expectOneInvoicePerPeriod, jst } from "../../helpers/orchestration.ts";

test("E2E-020: 更新課金の処理時間を計測する（合否は二重課金 0 件だけ）", async ({ api, world, db, wait }, testInfo) => {
  const count = Number(process.env.E2E_PERF_COUNT ?? 10_000);
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const seedStart = Date.now();
  const seeded = await world.seedSubscriptions(tenant, plan.id, count);
  expect(seeded.created).toBe(count);
  const date = seeded.firstRenewalDate;
  const set = await api.dev.setClock({ to: jst(date, "00:30") });
  expect(set.status, JSON.stringify(set.body)).toBe(200);
  const after = (set.body as { effective_after?: string }).effective_after;
  if (after) await new Promise((r) => setTimeout(r, Math.max(0, new Date(after).getTime() - Date.now()) + 50));

  const paid = () => db.count("SELECT count(*) FROM billing.invoices WHERE tenant_id = $1 AND kind = 'renewal' AND status = 'paid'", [tenant.id]);
  const t0 = Date.now();
  const run = await api.dev.runJob(JOB.dispatch, { as_of: date, force: true, wait_seconds: 120 });
  expect(run.status, JSON.stringify(run.body)).toBe(200);
  const series: { t: number; paid: number }[] = [];
  await wait.until(
    async () => {
      const n = await paid();
      series.push({ t: Math.round((Date.now() - t0) / 1000), paid: n });
      return n >= count;
    },
    { timeout: 60 * 60_000, interval: 5_000, message: `${count} 件の更新が paid になる` },
  );
  const seconds = (Date.now() - t0) / 1000;
  const seedSeconds = (t0 - seedStart) / 1000;
  let sha = "unknown";
  try {
    sha = execSync("git rev-parse --short HEAD").toString().trim();
  } catch {
    /* git がない環境 */
  }
  const result = { count, seconds, seed_seconds: Math.round(seedSeconds), per_second: Number((count / seconds).toFixed(1)), series, concurrency: { "orc-renewal.fifo": 10, "orc-collection": 5 }, worker_billing_tasks: 1, commit: sha };
  await testInfo.attach("e2e-020-result.json", { body: JSON.stringify(result, null, 2), contentType: "application/json" });
  const summary = `${count} 件 / ${Math.round(seconds)} 秒 / ${result.per_second} 件/秒`;
  testInfo.annotations.push({ type: "result", description: summary });
  // 成功時もレポートの外に残す（HTML レポートの添付は失敗時以外は消えることがある）
  mkdirSync("test-results", { recursive: true });
  writeFileSync("test-results/e2e-020-result.json", JSON.stringify(result, null, 2));
  console.log(`E2E-020: ${summary}`); // eslint-disable-line no-console
  await expectNoDoubleCharge(db, tenant.id);
  await expectOneInvoicePerPeriod(db, tenant.id);
});
