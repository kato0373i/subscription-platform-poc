// AC-PLT-070：wait.until は上限で失敗し、エラーに最後の値が含まれる
import { test, expect } from "../../fixtures/index.ts";

test("wait.until は 15 秒で失敗し、最後の値を添える", async ({ wait }) => {
  test.setTimeout(30_000);
  const started = Date.now();
  const err = await wait.until<true>(() => false, { interval: 1000 }).catch((e: Error) => e);
  expect(err).toBeInstanceOf(Error);
  expect((err as Error).message).toContain("15000ms");
  expect((err as Error).message).toContain("最後の値: false");
  const elapsed = Date.now() - started;
  expect(elapsed).toBeGreaterThanOrEqual(15_000);
  expect(elapsed).toBeLessThan(18_000);
});
