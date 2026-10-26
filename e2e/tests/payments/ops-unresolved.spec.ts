// operator-console §5.1（確定していない試行の一覧）：結果不明の試行が出る・確定すると消える・運営者以外は 403。
import type { OpsPaymentsComponents } from "@sp/api-client";
import { test, expect } from "../../fixtures/index.ts";
import { createPlan } from "../../helpers/catalog.ts";

type Attempt = OpsPaymentsComponents["schemas"]["Attempt"];
type Page = { data: Attempt[]; has_more: boolean; next_cursor: string | null };

test("operator-console FR-006: 結果不明の試行は一覧に出て、手動で確定すると消える。経過時間・状態で絞り込める", async ({ api, world, tokens, db, wait }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  // 結果不明が出ると事業者の回路が開くので、1 事業者に 1 件だけ作る
  expect((await world.seedSubscriptions(tenant, plan.id, 1, "timeout_lost")).failed).toBe(1);
  const [row] = await wait.until(
    async () => {
      const r = await db.query<{ id: string }>("SELECT id FROM payments.payment_attempts WHERE tenant_id = $1 AND status = 'unknown'", [tenant.id]);
      return r.length === 1 ? r : undefined;
    },
    { message: "結果不明の試行" },
  );
  const op = await tokens.operator();
  // status は複数の値を繰り返しのクエリで渡す（style: form, explode: true）
  const list = (q: Record<string, string | number | string[]>) => {
    const p = new URLSearchParams();
    for (const [k, v] of Object.entries(q)) for (const x of [v].flat()) p.append(k, String(x));
    return api.call<Page>("GET", `/v1/ops/payment-attempts?${p}`, { token: op });
  };
  // この事業者の試行だけを、カーソルでたどって集める（他のテストの試行も一覧に出る）
  const mine = async (q: Record<string, string | number | string[]>) => {
    const found: Attempt[] = [];
    let cursor: string | undefined;
    for (let i = 0; i < 50; i++) {
      const r = await list({ ...q, limit: 100, ...(cursor ? { starting_after: cursor } : {}) });
      expect(r.status, JSON.stringify(r.body)).toBe(200);
      found.push(...r.body.data.filter((a) => a.tenant_id === tenant.id));
      if (!r.body.has_more) break;
      cursor = r.body.next_cursor!;
    }
    return found;
  };

  // 既定（30 秒以上たったもの）には、作ったばかりの試行は出ない
  expect(await mine({})).toEqual([]);
  // 経過時間 0 なら出る。状態で絞り込める
  const got = await mine({ older_than_seconds: 0 });
  expect(got.map((a) => a.id)).toEqual([row!.id]);
  expect(got[0]).toMatchObject({ status: "unknown", amount: 980, provider: "stub", finalized_at: null });
  expect((await mine({ older_than_seconds: 0, status: ["unknown"] })).map((a) => a.id)).toEqual([row!.id]);
  expect(await mine({ older_than_seconds: 0, status: ["processing", "pending_async"] })).toEqual([]);

  const resolved = await api.call<Attempt>("POST", `/v1/ops/payment-attempts/${row!.id}/resolve`, {
    token: op,
    body: { outcome: "failed", failure_code: "not_processed", retryable: true, note: "代行に注文がないことを確認" },
  });
  expect(resolved.status, JSON.stringify(resolved.body)).toBe(200);
  expect(await mine({ older_than_seconds: 0 })).toEqual([]);

  expect((await list({ status: ["succeeded"] })).status).toBe(400);
});

test("operator-console NFR-001: 確定していない試行の一覧は、契約者・スタッフのトークンでは 403", async ({ api, world, tokens }) => {
  const tenant = await world.createTenant();
  const consumer = await tokens.issue({ pool: "consumer" });
  expect((await api.call("GET", "/v1/ops/payment-attempts", { token: consumer.token })).status).toBe(403);
  expect((await api.call("GET", "/v1/ops/payment-attempts", { token: tenant.ownerToken })).status).toBe(403);
});
