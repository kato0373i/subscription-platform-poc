// E2E-014 課金バッチの再実行で二重課金しない（orchestration.md §11b.3）：AC-ORC-041、FR-020・NFR-014。
// 更新の途中で worker-billing を SIGKILL で落とし（procs fixture）、起動し直してから rerun を 2 回流す。
// 停止の位置は非決定的なので、二重課金がないこと・期間ごとの請求が 1 件であることを合否の中心にする。
// 投入の途中停止からの再開（カーソル）は結合テスト（TestRenewalDispatch_ResumeFromCursor）でも確かめている。
import { test, expect } from "../../fixtures/index.ts";

const testInfo = () => test.info();
import { createPlan } from "../../helpers/catalog.ts";
import { JOB, addMonths, expectInvariants, jst, meSubscription, startAtNext15, subscribe } from "../../helpers/orchestration.ts";

type Runs = { runs: { id: string; seq: number; trigger: string; status: string }[]; outcomes: Record<string, number> };

test("E2E-014: 更新の途中で worker-billing が落ちても、起動し直して rerun を 2 回流せば各契約の更新の請求と成功した試行は 1 件", async ({ api, world, db, wait, clock, tokens, procs }) => {
  test.setTimeout(900_000);
  const D = await startAtNext15(clock);
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const subs = [];
  for (let i = 0; i < 3; i++) subs.push(await subscribe(api, world, tenant, plan.id));
  const renewal = addMonths(D, 1);
  const paidRenewals = () => db.count("SELECT count(*) FROM billing.invoices WHERE tenant_id = $1 AND kind = 'renewal' AND status = 'paid'", [tenant.id]);

  // ② 更新を始め、最初の 1 件が paid になった時点で worker-billing を落とす
  await clock.setTo(jst(renewal, "00:30"));
  await clock.runJob(JOB.dispatch, { asOf: renewal, force: true });
  await wait.until(async () => (await paidRenewals()) >= 1, { message: "最初の更新が paid になる", interval: 100 });
  await procs.kill("worker-billing");
  const paidAtKill = await paidRenewals();

  // ③ 起動し直す（受信中だったメッセージは可視性タイムアウトの後に再配信される）
  await procs.up("worker-billing");

  // ④ rerun を 2 回（2 回目は 1 回目が dispatched になってから）。
  // 落ちたプロセスが出していた受信（ロングポーリング）にメッセージが渡ると、可視性タイムアウト（scheduler キューは 300 秒）の後に
  // 再配信されるので、待ち時間はそれより長く取る（落ちたタスクのメッセージが戻る経路も、このテストで通る）
  const op = await tokens.operator();
  for (let i = 0; i < 2; i++) {
    const res = await api.call<{ billing_run_id: string }>("POST", `/v1/ops/billing-runs/${renewal}/rerun`, { token: op, body: { tenant_id: tenant.id } });
    expect(res.status, JSON.stringify(res.body)).toBe(202);
    await wait.until(
      async () => {
        const g = await api.call<Runs>("GET", `/v1/ops/billing-runs/${renewal}`, { token: op });
        return g.body.runs.find((r) => r.id === res.body.billing_run_id && r.status === "dispatched");
      },
      { message: "rerun の投入が終わる", timeout: 330_000, interval: 1_000 },
    );
  }
  const long = { timeout: 180_000, interval: 1_000 };
  for (const s of subs) {
    await wait.until(async () => (await meSubscription(api, s.consumer, s.subscription.id)).next_renewal_date === addMonths(D, 2), { ...long, message: "更新が確定する" });
    await wait.until(
      async () => (await db.count("SELECT count(*) FROM orchestration.billing_cycle_processes WHERE subscription_id = $1 AND kind = 'renewal' AND outcome = 'paid'", [s.subscription.id])) === 1,
      { ...long, message: "更新のプロセスが paid で閉じる" },
    );
  }
  const runs = await api.call<Runs>("GET", `/v1/ops/billing-runs/${renewal}`, { token: op });
  expect(runs.body.runs.filter((r) => r.trigger === "rerun").length).toBeGreaterThanOrEqual(2);
  expect(runs.body.outcomes.paid).toBeGreaterThanOrEqual(3);
  for (const s of subs) {
    expect((await meSubscription(api, s.consumer, s.subscription.id)).status).toBe("active");
    expect(await db.count("SELECT count(*) FROM billing.invoices WHERE subscription_id = $1 AND kind = 'renewal'", [s.subscription.id])).toBe(1);
    expect(
      await db.count("SELECT count(*) FROM payments.payment_attempts a JOIN billing.invoices i ON i.id = a.payable_id WHERE i.subscription_id = $1 AND i.kind = 'renewal' AND a.status = 'succeeded'", [s.subscription.id]),
    ).toBe(1);
  }
  await expectInvariants(db, wait, tenant.id);
  testInfo().annotations.push({ type: "kill", description: `停止した時点の paid は ${paidAtKill} / 3 件` });
});
