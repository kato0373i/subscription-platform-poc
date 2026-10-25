// 署名とローテーション直後の 2 つの署名（integration.md §4.3）：AC-INT-005。検証は Go と独立した TypeScript の実装（WS-7）。
import { test, expect } from "../../fixtures/index.ts";
import { consoleOf, createPlan } from "../../helpers/catalog.ts";
import { bodyOf, header, sinkEndpoint } from "../../helpers/integration.ts";
import { subscribe } from "../../helpers/orchestration.ts";
import { parseSignature, sign, verifySignature } from "../../helpers/webhook-signature.ts";

test("署名ヘッダーと付随するヘッダー。署名は送った本文そのものに対する HMAC-SHA256", async ({ api, world, sink }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const s = await sinkEndpoint(api, sink, tenant, ["subscription.started"]);
  await subscribe(api, world, tenant, plan.id);
  const [rec] = await sink.waitForRequests(s.sinkId, 1);
  const body = bodyOf(rec!);
  expect(verifySignature(rec!, s.endpoint.secret)).toBe(true);
  expect(parseSignature(header(rec!, "SP-Signature")!).v1).toHaveLength(1);
  expect(header(rec!, "Content-Type")).toBe("application/json");
  expect(header(rec!, "User-Agent")).toBe("SubscriptionPlatform-Webhooks/1.0");
  expect(header(rec!, "SP-Event-Id")).toBe(body.id);
  expect(header(rec!, "SP-Event-Type")).toBe("subscription.started");
  expect(header(rec!, "SP-Delivery-Attempt")).toBe("1");
  expect(header(rec!, "SP-Delivery-Id")).toMatch(/^[0-9a-f-]{36}$/);
  // 本文を 1 バイト変えると一致しない
  const { t, v1 } = parseSignature(header(rec!, "SP-Signature")!);
  const tampered = Buffer.from(rec!.body_base64, "base64");
  tampered[0] = tampered[0]! ^ 1;
  expect(sign(s.endpoint.secret, t, tampered)).not.toBe(v1[0]);
});

test("AC-INT-005: grace 24 時間でローテーションすると v1 が 2 つ付き、K2・K1 の両方と一致する（current が先）", async ({ api, world, sink }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const s = await sinkEndpoint(api, sink, tenant, ["subscription.started"]);
  const k1 = s.endpoint.secret;
  const rot = await consoleOf(api, tenant)<{ secret: string; previous_secret_expires_at: string }>("POST", `/v1/console/webhook-endpoints/${s.endpoint.id}/rotate-secret`, {
    grace_period_hours: 24,
  });
  expect(rot.status, JSON.stringify(rot.body)).toBe(200);
  const k2 = rot.body.secret;
  expect(k2).not.toBe(k1);
  await subscribe(api, world, tenant, plan.id);
  const [rec] = await sink.waitForRequests(s.sinkId, 1);
  const { t, v1 } = parseSignature(header(rec!, "SP-Signature")!);
  const body = Buffer.from(rec!.body_base64, "base64");
  expect(v1).toEqual([sign(k2, t, body), sign(k1, t, body)]);
  expect(verifySignature(rec!, k1)).toBe(true);
  expect(verifySignature(rec!, k2)).toBe(true);

  // E8：grace 0 で回すと previous を持たない
  const rot0 = await consoleOf(api, tenant)<{ secret: string; previous_secret_expires_at: string | null }>("POST", `/v1/console/webhook-endpoints/${s.endpoint.id}/rotate-secret`, {
    grace_period_hours: 0,
  });
  expect(rot0.body.previous_secret_expires_at).toBeNull();
  const bad = await consoleOf(api, tenant)("POST", `/v1/console/webhook-endpoints/${s.endpoint.id}/rotate-secret`, { grace_period_hours: 169 });
  expect(bad.status).toBe(400);
});
