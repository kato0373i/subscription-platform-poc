// AC-PLT-077：Fluent Bit → MiniStack の CloudWatch Logs に、本番と同じ名前のロググループ（/sp/local/<service>）で届く
// MiniStack の Logs Insights は filter が効かず（条件に関係なく行を返す）、fields も @ の項目だけなので、
// 取り出した @message を手元で照合する（P-OBS-10。調査は task logs で行う）。
import { test, expect } from "../../fixtures/index.ts";
import { devlogs } from "../../fixtures/logs.ts";

test("相関 ID を持つ http.request.completed が /sp/local/api に届く", async ({ api, logs, wait }) => {
  test.setTimeout(60_000);
  await api.call("GET", "/v1/dev/clock");
  const row = await wait.until(async () => {
    const out = await devlogs(["cwl", "fields @timestamp, @log, @message | sort @timestamp desc | limit 10000", "--since", "10m"]);
    return out.split("\n").find((l) => l.includes(`"correlation_id":"${logs.correlationId}"`) && l.includes("http.request.completed"));
  }, { timeout: 30_000, interval: 2_000, message: "CloudWatch Logs に届く" });
  expect(row).toContain("@log=/sp/local/api");
});
