// テナント分離の HTTP 層の確認（platform.md §10.2）。テストには @isolation:<operationId> のタグを付ける（task lint が宣言と突き合わせる）。
//   test("...", { tag: "@isolation:getSubscription" }, async (...) => { await expectIsolated(...) })
import { expect } from "@playwright/test";
import type { ApiResult } from "../fixtures/api.ts";

/** owner の主体では見え、other の主体では 404 になることを確かめる。 */
export async function expectIsolated(op: (as: "owner" | "other") => Promise<ApiResult>): Promise<void> {
  const own = await op("owner");
  expect(own.status, `owner で見えない: ${JSON.stringify(own.body)}`).toBeLessThan(300);
  const other = await op("other");
  expect(other.status, `別テナントの主体で見えた: ${JSON.stringify(other.body)}`).toBe(404);
  expect(other.problem?.code).toBe("not_found");
}
