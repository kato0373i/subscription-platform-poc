// payments.md §7.9（POST /webhooks/{provider}）：AC-PAY-032
import { createHmac, randomUUID } from "node:crypto";
import { test, expect } from "../../fixtures/index.ts";

const secret = process.env.STUB_WEBHOOK_SECRET ?? "local-stub-webhook-secret";

function sign(body: string, key = secret, at = Math.floor(Date.now() / 1000)): string {
  const mac = createHmac("sha256", key).update(`${at}.${body}`).digest("hex");
  return `t=${at},v1=${mac}`;
}

test("AC-PAY-032: 署名が不正な通知は 401 で保存されない。正しい通知は保存されて 200（重複も 200）", async ({ request, db }) => {
  const eventId = `evt_e2e_${randomUUID().slice(0, 8)}`;
  const body = JSON.stringify({ event_id: eventId, order_ref: `stub_${randomUUID()}`, status: "succeeded", occurred_at: new Date().toISOString() });
  const bad = await request.post("/webhooks/stub", { data: body, headers: { "content-type": "application/json", "X-Stub-Signature": sign(body, "wrong-secret") } });
  expect(bad.status()).toBe(401);
  expect(await db.count("SELECT count(*) FROM payments.provider_events WHERE provider_event_id = $1", [eventId])).toBe(0);

  for (let i = 0; i < 2; i++) {
    const ok = await request.post("/webhooks/stub", { data: body, headers: { "content-type": "application/json", "X-Stub-Signature": sign(body) } });
    expect(ok.status()).toBe(200);
  }
  expect(await db.count("SELECT count(*) FROM payments.provider_events WHERE provider_event_id = $1", [eventId])).toBe(1);
  expect((await request.post("/webhooks/nosuch", { data: body, headers: { "X-Stub-Signature": sign(body) } })).status()).toBe(404);
});
