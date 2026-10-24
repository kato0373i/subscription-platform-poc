// AC-PLT-046・047：3 プロセスが起動し、テストイベントが Outbox → EventBridge（または direct）→ SQS → 購読者まで届く
import { test, expect } from "../../fixtures/index.ts";

test("テストイベントが購読者まで届く", async ({ api, wait }) => {
  for (const port of [8080, 8081, 8082]) {
    const r = await api.call("GET", `http://localhost:${port}/readyz`);
    expect(r.status, `:${port}/readyz`).toBe(200);
  }
  const res = await api.call<{ event_id: string }>("POST", "/v1/dev/smoke");
  expect(res.status).toBe(202);
  const st = await wait.until(async () => {
    const r = await api.call<{ published: boolean; consumed: boolean }>("GET", `/v1/dev/smoke/${res.body.event_id}`);
    return r.body.consumed ? r.body : undefined;
  }, { message: "購読者が処理する" });
  expect(st.published).toBe(true);
});
