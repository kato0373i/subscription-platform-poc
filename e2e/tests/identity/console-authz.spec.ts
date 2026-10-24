// identity.md §3.4・§7.2（console 共通の認可）：AC-IDN-010（一部）・013・015、E15
import { test, expect } from "../../fixtures/index.ts";

test("AC-IDN-010（一部）: admin は staff.manage・apikey.manage の操作ができない", async ({ api, world }) => {
  const tenant = await world.createTenant();
  const admin = await world.createStaff(tenant, "admin");
  const list = await api.call<{ data: { id: string; subject: string }[] }>("GET", "/v1/console/staff", { token: admin.token, tenantId: tenant.id });
  expect(list.status).toBe(200); // console.read は可
  const owner = list.body.data.find((s) => s.subject === tenant.ownerSub)!;

  const patch = await api.call("PATCH", `/v1/console/staff/${owner.id}`, { token: admin.token, tenantId: tenant.id, body: { role: "viewer" } });
  expect(patch.status).toBe(403);
  expect(patch.problem?.code).toBe("forbidden");
  const key = await api.call("POST", "/v1/console/api-keys", { token: admin.token, tenantId: tenant.id, body: { name: "x" } });
  expect(key.status).toBe(403);
  // webhook-endpoints（webhook.manage）と catalog.write の確認は integration・catalog の実装後（AC-IDN-009・010 の残り）
});

test("viewer は閲覧だけできる", async ({ api, world }) => {
  const tenant = await world.createTenant();
  const viewer = await world.createStaff(tenant, "viewer");
  expect((await api.call("GET", "/v1/console/staff", { token: viewer.token, tenantId: tenant.id })).status).toBe(200);
  expect((await api.call("GET", "/v1/console/api-keys", { token: viewer.token, tenantId: tenant.id })).status).toBe(200);
  const key = await api.call("POST", "/v1/console/api-keys", { token: viewer.token, tenantId: tenant.id, body: { name: "x" } });
  expect(key.status).toBe(403);
});

test("AC-IDN-013・E15: 所属していないテナントを X-Tenant-Id に指定すると 403、存在しないテナントも 403", async ({ api, world }) => {
  const a = await world.createTenant();
  const b = await world.createTenant();
  const res = await api.call("GET", "/v1/console/staff", { token: a.ownerToken, tenantId: b.id });
  expect(res.status).toBe(403);
  expect(res.problem?.code).toBe("forbidden");
  const none = await api.call("GET", "/v1/console/staff", { token: a.ownerToken, tenantId: crypto.randomUUID() });
  expect(none.status).toBe(403);
});

test("X-Tenant-Id がない・UUID でないと 400", async ({ api, world }) => {
  const a = await world.createTenant();
  const missing = await api.call("GET", "/v1/console/staff", { token: a.ownerToken });
  expect(missing.status).toBe(400);
  expect(missing.problem?.errors?.[0]?.field).toBe("X-Tenant-Id");
  const bad = await api.call("GET", "/v1/console/staff", { token: a.ownerToken, tenantId: "tenant-a" });
  expect(bad.status).toBe(400);
});

test("AC-IDN-015: 停止中のテナントの owner は閲覧できるが、書き込みは tenant_not_active", async ({ api, tokens, world }) => {
  const tenant = await world.createTenant({ apiKey: true });
  await api.call("POST", `/v1/ops/tenants/${tenant.id}/suspend`, { token: await tokens.operator(), body: { reason: "停止" } });
  expect((await api.call("GET", "/v1/console/staff", { token: tenant.ownerToken, tenantId: tenant.id })).status).toBe(200);
  expect((await api.call("GET", "/v1/console/api-keys", { token: tenant.ownerToken, tenantId: tenant.id })).status).toBe(200);
  const res = await api.call("POST", "/v1/console/api-keys", { token: tenant.ownerToken, tenantId: tenant.id, body: { name: "x" } });
  expect(res.status).toBe(403);
  expect(res.problem?.code).toBe("tenant_not_active");
});

test("pending_review のテナントでも console の書き込みはできる", async ({ api, world }) => {
  const tenant = await world.createTenant({ approve: false });
  const res = await api.call("POST", "/v1/console/api-keys", { token: tenant.ownerToken, tenantId: tenant.id, body: { name: "審査中" } });
  expect(res.status).toBe(201);
});
