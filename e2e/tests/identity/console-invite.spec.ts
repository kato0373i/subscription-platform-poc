// business-console FR-004・FR-006・FR-007・FR-015：招待（Cognito にアカウントを作る）・招待の再送・所属の一覧・If-Match。
// 招待のメールは MiniStack の SES に届く（Cognito の招待メール）。
import { randomUUID } from "node:crypto";
import type { ConsoleTenantComponents } from "@sp/api-client";
import { test, expect } from "../../fixtures/index.ts";
import { latestMail, tempPasswordOf, uniqueEmail } from "../../helpers/console.ts";
import { createProduct } from "../../helpers/catalog.ts";
import { expectIsolated } from "../../helpers/isolation.ts";

type Staff = ConsoleTenantComponents["schemas"]["Staff"];
type Membership = ConsoleTenantComponents["schemas"]["Membership"];

test("FR-006: owner が招待すると Cognito にアカウントができ、仮パスワードのメールが届く。同じメールアドレスは 409、viewer は 403", async ({ api, world, mail, wait }) => {
  const tenant = await world.createTenant();
  const email = uniqueEmail("inv");
  const res = await api.call<Staff>("POST", "/v1/console/staff", {
    token: tenant.ownerToken, tenantId: tenant.id, idempotencyKey: randomUUID(), body: { email: email.toUpperCase(), display_name: "招待 太郎", role: "admin" },
  });
  expect(res.status, JSON.stringify(res.body)).toBe(201);
  expect(res.body).toMatchObject({ email, role: "admin", status: "invited", invitation_expired: false, last_seen_at: null });
  expect(res.body.subject).toMatch(/^[0-9a-f-]{36}$/); // Cognito の sub
  expect(tempPasswordOf(await latestMail(mail, wait, email, "招待"))).toBeTruthy();

  const dup = await api.call("POST", "/v1/console/staff", {
    token: tenant.ownerToken, tenantId: tenant.id, idempotencyKey: randomUUID(), body: { email, display_name: "二重", role: "viewer" },
  });
  expect(dup.status).toBe(409);
  expect(dup.problem?.code).toBe("staff_already_member");

  const viewer = await world.createStaff(tenant, "viewer");
  const denied = await api.call("POST", "/v1/console/staff", {
    token: viewer.token, tenantId: tenant.id, idempotencyKey: randomUUID(), body: { email: uniqueEmail("inv"), display_name: "x", role: "viewer" },
  });
  expect(denied.status).toBe(403);
});

test("FR-007: 招待中のスタッフに招待を再送できる（メールがもう 1 通届く）。有効なスタッフには 409", async ({ api, world, mail, wait }) => {
  const tenant = await world.createTenant();
  const email = uniqueEmail("resend");
  const inv = await api.call<Staff>("POST", "/v1/console/staff", {
    token: tenant.ownerToken, tenantId: tenant.id, idempotencyKey: randomUUID(), body: { email, display_name: "再送", role: "viewer" },
  });
  expect(inv.status).toBe(201);
  await latestMail(mail, wait, email, "招待");
  const resent = await api.call<Staff>("POST", `/v1/console/staff/${inv.body.id}/resend-invitation`, { token: tenant.ownerToken, tenantId: tenant.id, idempotencyKey: randomUUID() });
  expect(resent.status, JSON.stringify(resent.body)).toBe(202);
  expect(resent.body.status).toBe("invited");
  await latestMail(mail, wait, email, "招待", 1); // 2 通目

  const list = await api.call<{ data: Staff[] }>("GET", "/v1/console/staff", { token: tenant.ownerToken, tenantId: tenant.id });
  const owner = list.body.data.find((s) => s.subject === tenant.ownerSub)!;
  const active = await api.call("POST", `/v1/console/staff/${owner.id}/resend-invitation`, { token: tenant.ownerToken, tenantId: tenant.id, idempotencyKey: randomUUID() });
  expect(active.status).toBe(409);
  expect(active.problem?.code).toBe("staff_not_invited");
});

test("他テナントのスタッフ ID には招待を再送できない（404）", { tag: "@isolation:consoleResendStaffInvitation" }, async ({ api, world }) => {
  const a = await world.createTenant();
  const b = await world.createTenant();
  const inv = await api.call<Staff>("POST", "/v1/console/staff", {
    token: a.ownerToken, tenantId: a.id, idempotencyKey: randomUUID(), body: { email: uniqueEmail("iso"), display_name: "分離", role: "viewer" },
  });
  expect(inv.status).toBe(201);
  await expectIsolated((as) =>
    as === "owner"
      ? api.call("POST", `/v1/console/staff/${inv.body.id}/resend-invitation`, { token: a.ownerToken, tenantId: a.id, idempotencyKey: randomUUID() })
      : api.call("POST", `/v1/console/staff/${inv.body.id}/resend-invitation`, { token: b.ownerToken, tenantId: b.id, idempotencyKey: randomUUID() }),
  );
});

test("FR-004: 所属の一覧は X-Tenant-Id なしで呼べ、所属する事業者・ロール・使える操作を返す", async ({ api, world, tokens }) => {
  const a = await world.createTenant();
  const b = await world.createTenant({ approve: false });
  const staff = await tokens.issue({ pool: "staff" });
  const op = await tokens.operator();
  for (const [t, role] of [[a, "viewer"], [b, "owner"]] as const) {
    const r = await api.call("POST", `/v1/ops/tenants/${t.id}/staff`, { token: op, body: { subject: staff.sub, email: staff.email, display_name: "複数所属", role } });
    expect(r.status, JSON.stringify(r.body)).toBe(201);
  }
  const res = await api.call<{ data: Membership[] }>("GET", "/v1/console/memberships", { token: staff.token });
  expect(res.status, JSON.stringify(res.body)).toBe(200);
  const byId = Object.fromEntries(res.body.data.map((m) => [m.tenant_id, m]));
  expect(Object.keys(byId).sort()).toEqual([a.id, b.id].sort());
  expect(byId[a.id]).toMatchObject({ tenant_name: a.name, tenant_status: "active", role: "viewer", staff_status: "active", permissions: ["console.read"] });
  expect(byId[b.id]!.tenant_status).toBe("pending_review");
  expect(byId[b.id]!.permissions).toContain("staff.manage");

  const none = await tokens.issue({ pool: "staff" });
  const empty = await api.call<{ data: Membership[] }>("GET", "/v1/console/memberships", { token: none.token });
  expect(empty.status).toBe(200);
  expect(empty.body.data).toEqual([]);
});

test("FR-015: 古い version の If-Match での更新は 409 conflict。If-Match なしは従来どおり更新できる", async ({ api, world }) => {
  const tenant = await world.createTenant();
  const product = await createProduct(api, tenant);
  const v = Number(product.version);
  const ok = await api.call<{ version: number }>("PATCH", `/v1/console/products/${product.id}`, {
    token: tenant.ownerToken, tenantId: tenant.id, body: { name: "新しい名前" }, headers: { "If-Match": String(v) },
  });
  expect(ok.status, JSON.stringify(ok.body)).toBe(200);
  expect(Number(ok.body.version)).toBe(v + 1);
  const stale = await api.call("PATCH", `/v1/console/products/${product.id}`, {
    token: tenant.ownerToken, tenantId: tenant.id, body: { name: "古い画面から" }, headers: { "If-Match": String(v) },
  });
  expect(stale.status).toBe(409);
  expect(stale.problem?.code).toBe("conflict");
  const noHeader = await api.call("PATCH", `/v1/console/products/${product.id}`, { token: tenant.ownerToken, tenantId: tenant.id, body: { name: "ヘッダーなし" } });
  expect(noHeader.status).toBe(200);
});
