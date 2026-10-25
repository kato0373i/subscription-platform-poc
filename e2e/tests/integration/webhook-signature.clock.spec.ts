// 移行期間が過ぎた後の署名（integration.md §3.1.2・§4.3）：AC-INT-006。
import { test, expect } from "../../fixtures/index.ts";
import { consoleOf, createPlan } from "../../helpers/catalog.ts";
import { header, sinkEndpoint } from "../../helpers/integration.ts";
import { startAtNext15, subscribe } from "../../helpers/orchestration.ts";
import { parseSignature, sign } from "../../helpers/webhook-signature.ts";

test("AC-INT-006: ローテーションから 24 時間後のイベントは v1 が 1 つで、K2 とだけ一致する", async ({ api, world, sink, clock, db }) => {
  await startAtNext15(clock);
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const s = await sinkEndpoint(api, sink, tenant, ["subscription.started"]);
  const k1 = s.endpoint.secret;
  const rot = await consoleOf(api, tenant)<{ secret: string }>("POST", `/v1/console/webhook-endpoints/${s.endpoint.id}/rotate-secret`, { grace_period_hours: 24 });
  const k2 = rot.body.secret;
  await clock.advance("24h");
  await subscribe(api, world, tenant, plan.id);
  const [rec] = await sink.waitForRequests(s.sinkId, 1);
  const { t, v1 } = parseSignature(header(rec!, "SP-Signature")!);
  const body = Buffer.from(rec!.body_base64, "base64");
  expect(v1).toEqual([sign(k2, t, body)]);
  expect(v1).not.toContain(sign(k1, t, body));

  // 後片付けのジョブで前の鍵の暗号文が消える（§8.5）
  await clock.runJob("integration-webhook-secret-cleanup", { force: true });
  const [row] = await db.query<{ prev: boolean; exp: string | null }>(
    "SELECT secret_previous_ciphertext IS NOT NULL AS prev, previous_expires_at AS exp FROM integration.webhook_endpoints WHERE id = $1",
    [s.endpoint.id],
  );
  expect(row).toEqual({ prev: false, exp: null });
});
