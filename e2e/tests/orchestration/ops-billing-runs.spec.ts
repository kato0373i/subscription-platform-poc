// orchestration.md §7.6・§7.7（課金バッチの再実行と照会）：AC-ORC-043。
import { test, expect } from "../../fixtures/index.ts";
import { addDays } from "../../helpers/orchestration.ts";

test("AC-ORC-043: 未来の日付・不正な形式は 400、operator 以外は 403。過去の日付の rerun は 202 で投入の記録が見える", async ({ api, tokens, wait }) => {
  const op = await tokens.operator();
  const today = ((await api.dev.getClock()).body as { today: string }).today;
  const future = await api.call("POST", `/v1/ops/billing-runs/${addDays(today, 1)}/rerun`, { token: op, body: {} });
  expect(future.status).toBe(400);
  expect(future.problem?.code).toBe("validation_failed");
  expect((await api.call("POST", "/v1/ops/billing-runs/2026-13-40/rerun", { token: op, body: {} })).status).toBe(400);
  const consumer = await tokens.issue({ pool: "consumer" });
  expect((await api.call("POST", `/v1/ops/billing-runs/${today}/rerun`, { token: consumer.token, body: {} })).status).toBe(403);

  const date = addDays(today, -400); // 他のテストの課金日と重ならない過去の日
  const res = await api.call<{ billing_run_id: string; seq: number; trigger: string; status: string }>("POST", `/v1/ops/billing-runs/${date}/rerun`, { token: op, body: {} });
  expect(res.status, JSON.stringify(res.body)).toBe(202);
  expect(res.body).toMatchObject({ trigger: "rerun", status: "dispatching" });
  const done = await wait.until(
    async () => {
      const g = await api.call<{ runs: { id: string; status: string; enqueued_count: number }[]; outcomes: Record<string, number> }>("GET", `/v1/ops/billing-runs/${date}`, { token: op });
      return g.body.runs.find((r) => r.id === res.body.billing_run_id && r.status === "dispatched") ? g.body : undefined;
    },
    { message: "rerun の投入が終わる" },
  );
  expect(Object.keys(done.outcomes).sort()).toEqual(["ended", "failed", "in_progress", "paid", "payment_failed"]);
});
