// payments.md §8.1（結果不明の照合 resolve_unknown）：AC-PAY-016・017。時刻を進めて、照会の猶予（not_found は 10 分）を確かめる。
// 結果不明はブレーカー（決済代行ごと・連続 5 回で開く）の失敗に数えるので、1 テスト 1 件にする。
import type { Api } from "../../fixtures/api.ts";
import type { Db } from "../../fixtures/db.ts";
import { test, expect } from "../../fixtures/index.ts";
import type { World } from "../../fixtures/world.ts";
import { createPlan } from "../../helpers/catalog.ts";
import { JOB, checkout, startAtNext15, untilStatus, type Consumer } from "../../helpers/orchestration.ts";

type AttemptRow = { id: string; status: string; failure_code: string | null; failure_retryable: boolean | null; resolution: string | null };

async function unknownCheckout(api: Api, world: World, scenario: string) {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const consumer = (await world.createConsumer()) as Consumer;
  const pm = await world.createPaymentMethod(consumer, scenario);
  const res = await checkout(api, consumer, tenant, plan.id, pm.id);
  expect(res.status, JSON.stringify(res.body)).toBe(202);
  return { tenant, consumer, subscriptionId: (res.body as { subscription: { id: string } }).subscription.id };
}

const attemptOf = async (db: Db, tenantId: string) =>
  (await db.query<AttemptRow>("SELECT id, status, failure_code, failure_retryable, resolution FROM payments.payment_attempts WHERE tenant_id = $1", [tenantId]))[0]!;

test("AC-PAY-016: timeout_fail の申込は 202、resolve_unknown の後に契約は signup_failed・試行は failed card_declined", async ({ api, world, db, wait, clock }) => {
  await startAtNext15(clock);
  const { tenant, consumer, subscriptionId } = await unknownCheckout(api, world, "timeout_fail");
  expect((await attemptOf(db, tenant.id)).status).toBe("unknown");
  await clock.advance("1m");
  await clock.runJob(JOB.resolveUnknown, { force: true });
  expect(await attemptOf(db, tenant.id)).toMatchObject({ status: "failed", failure_code: "card_declined", resolution: "reconcile" });
  await untilStatus(api, wait, consumer, subscriptionId, "signup_failed");
});

test("AC-PAY-017: timeout_lost は 9 分後の照会では unknown のまま、さらに 2 分後（10 分超）に failed not_processed・retryable=true", async ({ api, world, db, clock }) => {
  await startAtNext15(clock);
  const { tenant } = await unknownCheckout(api, world, "timeout_lost");
  await clock.advance("9m");
  await clock.runJob(JOB.resolveUnknown, { force: true });
  expect((await attemptOf(db, tenant.id)).status).toBe("unknown");
  await clock.advance("2m");
  await clock.runJob(JOB.resolveUnknown, { force: true });
  expect(await attemptOf(db, tenant.id)).toMatchObject({ status: "failed", failure_code: "not_processed", failure_retryable: true, resolution: "reconcile" });
});
