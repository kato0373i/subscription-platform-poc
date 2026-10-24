// AC-PLT-076 の platform の範囲：応答に含まれるトークンや、要求に含まれるカード番号・Bearer がログに平文で出ない
// （カード登録・申込・API キーの流れは各ドメインの実装後に、同じ確認を tests/payments などで行う）
import { test, expect } from "../../fixtures/index.ts";
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";

test("トークン・カード番号・Bearer の平文がログに出ない", async ({ api, logs, wait }) => {
  const issued = await api.dev.issueToken({ pool: "consumer", sub: `redaction-${Date.now()}`, email: "taro@example.com" });
  const token = (issued.body as { token: string }).token;
  // 存在しないルートに、カード番号と Bearer を含む本文を送る（本文は LOG_HTTP_BODY=redacted で記録される）
  await api.call("POST", "/v1/me/nothing-here", { token, body: { note: "4242 4242 4242 4242", auth: `Bearer ${token}` } });
  const lines = await wait.until(async () => {
    const l = await logs.query({ msg: "http.request.completed" });
    return l.length >= 2 ? l : undefined;
  });
  const tokenLog = lines.find((l) => l.route === "POST /v1/dev/tokens");
  expect(JSON.stringify(tokenLog?.response_body)).toContain("[REDACTED]");
  const dir = process.env.LOG_DIR ?? "../.local/logs";
  const all = readdirSync(dir)
    .filter((f) => /\.jsonl(\.\d+)?$/.test(f))
    .map((f) => readFileSync(join(dir, f), "utf8"))
    .join("\n");
  expect(all).not.toContain(token);
  expect(all).not.toContain("4242 4242 4242 4242");
  expect(all).not.toContain("taro@example.com");
});
