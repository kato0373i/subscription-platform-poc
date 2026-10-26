// billing.md §7.3：AC-BIL-038。更新課金で支払いに失敗した open・scheduled の請求を運営者が再決済すると、202 で collection_seq+1 の
// collection_due（reason=ops）が出る。リース中にもう一度呼ぶと同じ collection_seq を返す（P-X-1）。
import { test, expect } from "../../fixtures/index.ts";
import { createPlan } from "../../helpers/catalog.ts";
import { JOB, addMonths, jst, setBehavior, startAtNext15, subscribe, untilStatus } from "../../helpers/orchestration.ts";

type Collect = { invoice_id: string; status: string; collection_seq: number; requested_at: string };

test("AC-BIL-038: 支払い失敗の scheduled の請求を ops で再決済すると 202・collection_seq+1（reason=ops）、同時の 2 回目も同じ collection_seq", async ({ api, world, db, wait, clock, tokens }) => {
  const D = await startAtNext15(clock);
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const { consumer, pm, subscription } = await subscribe(api, world, tenant, plan.id);
  await setBehavior(api, pm.id, "decline", 1); // 更新の 1 回目だけ拒否される
  const renewal = addMonths(D, 1);
  await clock.setTo(jst(renewal, "00:30"));
  await clock.runJob(JOB.dispatch, { asOf: renewal, force: true });
  await untilStatus(api, wait, consumer, subscription.id, "past_due");
  const inv = await wait.until(
    async () => {
      const [r] = await db.query<{ id: string; status: string; collection_mode: string; collection_seq: number }>(
        "SELECT id, status, collection_mode, collection_seq FROM billing.invoices WHERE subscription_id = $1 AND kind = 'renewal'",
        [subscription.id],
      );
      return r?.status === "open" ? r : undefined;
    },
    { message: "更新の請求が open（結果反映済み）" },
  );
  expect(inv).toMatchObject({ collection_mode: "scheduled", collection_seq: 1 });

  const op = await tokens.operator();
  const call = () => api.call<Collect>("POST", `/v1/ops/invoices/${inv.id}/collect`, { token: op, body: { note: "AC-BIL-038" } });
  const [a, b] = await Promise.all([call(), call()]);
  for (const r of [a, b]) expect(r.status, JSON.stringify(r.body)).toBe(202);
  expect(a.body.collection_seq).toBe(2);
  expect(b.body.collection_seq).toBe(2);

  const due = await wait.until(
    async () => {
      const rows = await db.query<{ payload: { collection_seq: number; reason: string } }>(
        "SELECT payload FROM billing.outbox_events WHERE aggregate_id = $1 AND event_type = 'billing.invoice_collection_due' ORDER BY occurred_at",
        [inv.id],
      );
      return rows.length >= 2 ? rows : undefined;
    },
    { message: "ops の collection_due" },
  );
  expect(due.map((r) => [r.payload.collection_seq, r.payload.reason])).toEqual([
    [1, "initial"], // 請求の最初の回収
    [2, "ops"],
  ]);
  // 拒否の上書きは 1 回だけなので、再決済で支払われて契約が戻る
  await untilStatus(api, wait, consumer, subscription.id, "active");
  expect(await db.count("SELECT count(*) FROM billing.invoices WHERE id = $1 AND status = 'paid'", [inv.id])).toBe(1);
});
