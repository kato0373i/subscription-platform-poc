// payments.md §7.5・§7.6（ops の試行の確認と手動確定）：AC-PAY-023（結果不明の手動確定）・024
// 決済は dev の一括シード（段階 2 の前半。payments.Collect を通る）で作る。
import type { OpsPaymentsComponents } from "@sp/api-client";
import { test, expect } from "../../fixtures/index.ts";
import { createPlan } from "../../helpers/catalog.ts";

type Attempt = OpsPaymentsComponents["schemas"]["Attempt"];

test("AC-PAY-024: succeeded の試行を failed にすると 409、succeeded の指定は 200（冪等）", async ({ api, world, tokens, db }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const seeded = await world.seedSubscriptions(tenant, plan.id, 1);
  expect(seeded.created).toBe(1);
  const [row] = await db.query<{ id: string }>("SELECT id FROM payments.payment_attempts WHERE tenant_id = $1", [tenant.id]);
  const op = await tokens.operator();

  const got = await api.call<Attempt>("GET", `/v1/ops/payment-attempts/${row!.id}`, { token: op });
  expect(got.status).toBe(200);
  expect(got.body).toMatchObject({ status: "succeeded", provider: "stub", amount: 980, collection_seq: 1, resolution: "sync" });
  expect(got.body.idempotency_key).toMatch(/^invoice:[0-9a-f-]{36}:1$/);

  const fail = await api.call("POST", `/v1/ops/payment-attempts/${row!.id}/resolve`, { token: op, body: { outcome: "failed", failure_code: "manual", retryable: false, note: "誤り" } });
  expect(fail.status).toBe(409);
  expect(fail.problem?.code).toBe("attempt_already_finalized");
  const same = await api.call<Attempt>("POST", `/v1/ops/payment-attempts/${row!.id}/resolve`, { token: op, body: { outcome: "succeeded", note: "確認" } });
  expect(same.status).toBe(200);
  expect(same.body.resolution).toBe("sync");
  expect((await api.call("GET", `/v1/ops/payment-attempts/${crypto.randomUUID()}`, { token: op })).status).toBe(404);
});

test("AC-PAY-023: 代行も照会できない結果不明の試行を failed で手動確定すると resolution=manual・resolved_by", async ({ api, world, tokens, db }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const seeded = await world.seedSubscriptions(tenant, plan.id, 1, "timeout_lost");
  expect(seeded.failed).toBe(1);
  const [row] = await db.query<{ id: string; status: string }>("SELECT id, status FROM payments.payment_attempts WHERE tenant_id = $1", [tenant.id]);
  expect(row!.status).toBe("unknown");
  const opToken = await tokens.issue({ pool: "operator" });

  const res = await api.call<Attempt>("POST", `/v1/ops/payment-attempts/${row!.id}/resolve`, {
    token: opToken.token,
    body: { outcome: "failed", failure_code: "card_declined", retryable: true, note: "代行に注文がないことを確認" },
  });
  expect(res.status, JSON.stringify(res.body)).toBe(200);
  expect(res.body).toMatchObject({ status: "failed", failure_code: "card_declined", retryable: true, resolution: "manual", resolved_by: opToken.sub });
  const events = await db.query<{ payload: { resolution: string; resolved_by: string }; actor: { type: string } }>(
    "SELECT payload, actor FROM payments.outbox_events WHERE aggregate_id = $1 AND event_type = 'payments.payment_failed'",
    [row!.id],
  );
  expect(events).toHaveLength(1);
  expect(events[0]!.payload).toMatchObject({ resolution: "manual", resolved_by: opToken.sub });
  expect(events[0]!.actor.type).toBe("operator");
  // 監査記録（payment_attempt.resolve）の DB 確認は audit モジュールの実装後
  const noCode = await api.call("POST", `/v1/ops/payment-attempts/${row!.id}/resolve`, { token: opToken.token, body: { outcome: "failed", note: "x" } });
  expect(noCode.status).toBe(400); // failed には failure_code と retryable が要る
  const flip = await api.call("POST", `/v1/ops/payment-attempts/${row!.id}/resolve`, { token: opToken.token, body: { outcome: "succeeded", note: "x" } });
  expect(flip.status).toBe(409); // 確定済みを別の結果にはできない
});
