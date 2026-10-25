// billing.md §7.4（GET / PUT /v1/console/dunning-policy）：AC-BIL-032・033（HTTP の 400 の 1 件）・034
import { test, expect } from "../../fixtures/index.ts";
import { consoleOf } from "../../helpers/catalog.ts";
import type { DunningPolicy } from "../../helpers/billing.ts";

test("AC-BIL-032: 方針が未設定なら [1,3,7]・is_default=true。PUT すると保存され、同じ値の PUT も同じ結果", async ({ api, world }) => {
  const tenant = await world.createTenant();
  const call = consoleOf(api, tenant);
  const got = await call<DunningPolicy>("GET", "/v1/console/dunning-policy");
  expect(got.status).toBe(200);
  expect(got.body).toEqual({ retry_offsets_days: [1, 3, 7], is_default: true, version: 0, updated_at: null }); // version は If-Match 用（business-console FR-015）

  for (let i = 0; i < 2; i++) {
    const put = await call<DunningPolicy>("PUT", "/v1/console/dunning-policy", { retry_offsets_days: [2, 5] });
    expect(put.status, JSON.stringify(put.body)).toBe(200);
    expect(put.body).toMatchObject({ retry_offsets_days: [2, 5], is_default: false });
    expect(put.body.updated_at).not.toBeNull();
  }
  expect((await call<DunningPolicy>("GET", "/v1/console/dunning-policy")).body.retry_offsets_days).toEqual([2, 5]);
  // 0 回（即 uncollectible）も設定できる
  expect((await call<DunningPolicy>("PUT", "/v1/console/dunning-policy", { retry_offsets_days: [] })).body.retry_offsets_days).toEqual([]);
  // 他の事業者には影響しない
  const other = await world.createTenant();
  expect((await consoleOf(api, other)<DunningPolicy>("GET", "/v1/console/dunning-policy")).body.is_default).toBe(true);
});

test("AC-BIL-033: 昇順でない方針は 400 validation_failed（errors[].code = not_increasing）", async ({ api, world }) => {
  const tenant = await world.createTenant();
  const res = await consoleOf(api, tenant)("PUT", "/v1/console/dunning-policy", { retry_offsets_days: [1, 1] });
  expect(res.status).toBe(400);
  expect(res.problem?.code).toBe("validation_failed");
  expect(res.problem?.errors).toEqual([{ field: "retry_offsets_days", code: "not_increasing" }]);
});

test("AC-BIL-034: viewer は GET できるが PUT は 403", async ({ api, world }) => {
  const tenant = await world.createTenant();
  const viewer = await world.createStaff(tenant, "viewer");
  const call = consoleOf(api, tenant, viewer.token);
  expect((await call("GET", "/v1/console/dunning-policy")).status).toBe(200);
  const res = await call("PUT", "/v1/console/dunning-policy", { retry_offsets_days: [1] });
  expect(res.status).toBe(403);
  expect(res.problem?.code).toBe("forbidden");
});
