// E2E-014 課金バッチの再実行で二重課金しない（orchestration.md §11b.3）：AC-ORC-041、FR-020・NFR-014。
// worker-billing を途中で止める手順（procs fixture）は、プロセスを止める仕組みを E2E に用意したときに足す。
// 投入の途中停止からの再開は結合テスト（TestRenewalDispatch_ResumeFromCursor）で確かめている。
import { test, expect } from "../../fixtures/index.ts";
import { createPlan } from "../../helpers/catalog.ts";
import { JOB, addMonths, expectInvariants, jst, startAtNext15, subscribe, untilStatus } from "../../helpers/orchestration.ts";

type Runs = { runs: { id: string; seq: number; trigger: string; status: string }[]; outcomes: Record<string, number> };

test("E2E-014: 更新の後に rerun を 2 回しても、各契約の更新の請求と成功した試行は 1 件", async ({ api, world, db, wait, clock, tokens }) => {
  const D = await startAtNext15(clock);
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const subs = [];
  for (let i = 0; i < 3; i++) subs.push(await subscribe(api, world, tenant, plan.id));
  const renewal = addMonths(D, 1);
  await clock.setTo(jst(renewal, "00:30"));
  await clock.runJob(JOB.dispatch, { asOf: renewal, force: true });
  // 請求の paid の後、Orchestration が invoice_paid を受けて ConfirmRenewal・プロセスを閉じるまで待つ
  for (const s of subs) {
    await untilStatus(api, wait, s.consumer, s.subscription.id, "active");
    await wait.until(async () => (await db.count("SELECT count(*) FROM orchestration.billing_cycle_processes WHERE subscription_id = $1 AND kind = 'renewal' AND outcome = 'paid'", [s.subscription.id])) === 1, {
      message: "更新のプロセスが paid で閉じる",
    });
  }

  const op = await tokens.operator();
  for (let i = 0; i < 2; i++) {
    const res = await api.call<{ billing_run_id: string }>("POST", `/v1/ops/billing-runs/${renewal}/rerun`, { token: op, body: { tenant_id: tenant.id } });
    expect(res.status, JSON.stringify(res.body)).toBe(202);
    await wait.until(
      async () => {
        const g = await api.call<Runs>("GET", `/v1/ops/billing-runs/${renewal}`, { token: op });
        return g.body.runs.find((r) => r.id === res.body.billing_run_id && r.status === "dispatched");
      },
      { message: "rerun の投入が終わる" },
    );
  }
  const runs = await wait.until(
    async () => {
      const r = await api.call<Runs>("GET", `/v1/ops/billing-runs/${renewal}`, { token: op });
      return r.body.outcomes.paid >= 3 ? r : undefined;
    },
    { message: "rerun で投入した更新も閉じる" },
  );
  expect(runs.body.runs.filter((r) => r.trigger === "rerun").length).toBeGreaterThanOrEqual(2);
  for (const s of subs) {
    await untilStatus(api, wait, s.consumer, s.subscription.id, "active");
    expect(await db.count("SELECT count(*) FROM billing.invoices WHERE subscription_id = $1 AND kind = 'renewal'", [s.subscription.id])).toBe(1);
    expect(
      await db.count("SELECT count(*) FROM payments.payment_attempts a JOIN billing.invoices i ON i.id = a.payable_id WHERE i.subscription_id = $1 AND i.kind = 'renewal' AND a.status = 'succeeded'", [s.subscription.id]),
    ).toBe(1);
  }
  await expectInvariants(db, wait, tenant.id);
});
