// AC-PLT-081：相関 ID の受け取りと返し方（形式に合わない値は捨てて新しく作る）
import { test, expect } from "../../fixtures/index.ts";

test("X-Correlation-Id を受け取り、応答ヘッダーと Problem Details に返す", async ({ request }) => {
  const res = await request.get("/v1/me/nothing-here", { headers: { "X-Correlation-Id": "e2e-x-abc123" } });
  expect(res.status()).toBe(404);
  const body = await res.json();
  expect(res.headers()["x-correlation-id"]).toBe("e2e-x-abc123");
  expect(body.correlation_id).toBe("e2e-x-abc123");
  expect(body.request_id).toBe(res.headers()["x-request-id"]);
  expect(body.code).toBe("not_found");
});

test("長すぎる相関 ID は捨てられ、新しい UUID が返る", async ({ request }) => {
  const res = await request.get("/v1/me/nothing-here", { headers: { "X-Correlation-Id": "a".repeat(200) } });
  const corr = res.headers()["x-correlation-id"];
  expect(corr).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[0-9a-f]{4}-[0-9a-f]{12}$/);
  expect((await res.json()).correlation_id).toBe(corr);
});
