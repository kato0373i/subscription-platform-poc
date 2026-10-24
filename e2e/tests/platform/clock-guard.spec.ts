// AC-PLT-068：*.clock.spec.ts でないファイルから clock を進めると例外になり、dev clock は変わらない
// e2elint:allow-clock（このテストは clock project の外で呼ぶと例外になることを確かめる）
import { test, expect } from "../../fixtures/index.ts";
import { ClockMisuseError } from "../../fixtures/clock.ts";

test("clock.advance・setTo・runJob は clock project の外では例外", async ({ clock, api }) => {
  const before = (await api.dev.getClock()).body as { version: number };
  await expect(clock.advance("1h")).rejects.toBeInstanceOf(ClockMisuseError);
  await expect(clock.setTo(new Date(Date.now() + 3600_000))).rejects.toBeInstanceOf(ClockMisuseError);
  await expect(clock.runJob("platform-noop")).rejects.toBeInstanceOf(ClockMisuseError);
  const after = (await api.dev.getClock()).body as { version: number };
  expect(after.version).toBe(before.version);
});
