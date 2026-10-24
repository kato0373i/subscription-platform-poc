// わざと失敗するテスト（task e2e:selftest だけが流す。AC-PLT-074）。失敗したテストにログが添付されることを確かめる。
import { test, expect } from "../fixtures/index.ts";

test("selftest: わざと失敗してログを添付させる", async ({ api }) => {
  await api.call("GET", "/v1/dev/clock");
  await api.call("GET", "/v1/me/selftest-not-found");
  expect(1, "わざと失敗させる").toBe(2);
});
