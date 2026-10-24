// identity.md §7.2（スタッフ管理）：AC-IDN-011・035、C1〜C3、テナント分離
import type { ConsoleTenantComponents } from "@sp/api-client";
import { test, expect } from "../../fixtures/index.ts";
import { expectIsolated } from "../../helpers/isolation.ts";

type Staff = ConsoleTenantComponents["schemas"]["Staff"];
type StaffList = { data: Staff[]; has_more: boolean; next_cursor: string | null };

test("AC-IDN-011: 最後の owner は降格・削除できない", async ({ api, world }) => {
  const tenant = await world.createTenant();
  const list = await api.call<StaffList>("GET", "/v1/console/staff", { token: tenant.ownerToken, tenantId: tenant.id });
  expect(list.status).toBe(200);
  const owner = list.body.data.find((s) => s.subject === tenant.ownerSub)!;

  const demote = await api.call("PATCH", `/v1/console/staff/${owner.id}`, { token: tenant.ownerToken, tenantId: tenant.id, body: { role: "admin" } });
  expect(demote.status).toBe(409);
  expect(demote.problem?.code).toBe("last_owner_required");
  const del = await api.call("DELETE", `/v1/console/staff/${owner.id}`, { token: tenant.ownerToken, tenantId: tenant.id });
  expect(del.status).toBe(409);
  expect(del.problem?.code).toBe("last_owner_required");

  const after = await api.call<StaffList>("GET", "/v1/console/staff", { token: tenant.ownerToken, tenantId: tenant.id });
  expect(after.body.data.find((s) => s.id === owner.id)?.role).toBe("owner");
});

test("AC-IDN-035: owner が admin を viewer に変える。同じロールの再実行も 200。イベントは出ない", async ({ api, world, db }) => {
  const tenant = await world.createTenant();
  const admin = await world.createStaff(tenant, "admin");
  const list = await api.call<StaffList>("GET", "/v1/console/staff", { token: tenant.ownerToken, tenantId: tenant.id });
  const target = list.body.data.find((s) => s.subject === admin.sub)!;
  expect(target.role).toBe("admin");

  const res = await api.call<Staff>("PATCH", `/v1/console/staff/${target.id}`, { token: tenant.ownerToken, tenantId: tenant.id, body: { role: "viewer" } });
  expect(res.status, JSON.stringify(res.body)).toBe(200);
  expect(res.body.role).toBe("viewer");
  const again = await api.call<Staff>("PATCH", `/v1/console/staff/${target.id}`, { token: tenant.ownerToken, tenantId: tenant.id, body: { role: "viewer" } });
  expect(again.status).toBe(200);
  expect(await db.count("SELECT count(*) FROM tenant.outbox_events WHERE tenant_id = $1 AND event_type <> 'tenant.approved'", [tenant.id])).toBe(0);
  // 監査記録（staff.update_role、email・subject を含まない）の確認は audit モジュールの実装後（docs/poc/impl/identity.md）

  const bad = await api.call("PATCH", `/v1/console/staff/${target.id}`, { token: tenant.ownerToken, tenantId: tenant.id, body: { role: "operator" } });
  expect(bad.status).toBe(400);
});

test("C1・C3: 一覧は作成順。owner が 2 名なら片方を削除でき、削除済みは 404", async ({ api, world }) => {
  const tenant = await world.createTenant();
  const second = await world.createStaff(tenant, "owner");
  const viewer = await world.createStaff(tenant, "viewer");
  const list = await api.call<StaffList>("GET", "/v1/console/staff", { token: tenant.ownerToken, tenantId: tenant.id, query: { limit: 2 } });
  expect(list.body.data.map((s) => s.subject)).toEqual([tenant.ownerSub, second.sub]);
  expect(list.body.has_more).toBe(true);
  const rest = await api.call<StaffList>("GET", "/v1/console/staff", {
    token: tenant.ownerToken,
    tenantId: tenant.id,
    query: { starting_after: list.body.next_cursor! },
  });
  expect(rest.body.data.map((s) => s.subject)).toEqual([viewer.sub]);

  const secondId = list.body.data[1]!.id;
  expect((await api.call("DELETE", `/v1/console/staff/${secondId}`, { token: tenant.ownerToken, tenantId: tenant.id })).status).toBe(204);
  const gone = await api.call("DELETE", `/v1/console/staff/${secondId}`, { token: tenant.ownerToken, tenantId: tenant.id });
  expect(gone.status).toBe(404);
  // 削除されたスタッフはもう console を使えない（所属していない → 403）
  expect((await api.call("GET", "/v1/console/staff", { token: second.token, tenantId: tenant.id })).status).toBe(403);
});

test("AC-IDN-013: 他テナントのスタッフ ID でロールを変更すると 404", { tag: "@isolation:consoleUpdateStaff" }, async ({ api, world }) => {
  const a = await world.createTenant();
  const b = await world.createTenant();
  const target = await world.createStaff(a, "viewer");
  const list = await api.call<StaffList>("GET", "/v1/console/staff", { token: a.ownerToken, tenantId: a.id });
  const id = list.body.data.find((s) => s.subject === target.sub)!.id;
  await expectIsolated((as) =>
    as === "owner"
      ? api.call("PATCH", `/v1/console/staff/${id}`, { token: a.ownerToken, tenantId: a.id, body: { role: "admin" } })
      : api.call("PATCH", `/v1/console/staff/${id}`, { token: b.ownerToken, tenantId: b.id, body: { role: "admin" } }),
  );
});

test("他テナントのスタッフ ID は削除できない（404）", { tag: "@isolation:consoleDeleteStaff" }, async ({ api, world }) => {
  const a = await world.createTenant();
  const b = await world.createTenant();
  const target = await world.createStaff(a, "viewer");
  const list = await api.call<StaffList>("GET", "/v1/console/staff", { token: a.ownerToken, tenantId: a.id });
  const id = list.body.data.find((s) => s.subject === target.sub)!.id;
  // 先に別テナントで試し（404）、その後に自テナントで削除できる（204）
  const other = await api.call("DELETE", `/v1/console/staff/${id}`, { token: b.ownerToken, tenantId: b.id });
  expect(other.status).toBe(404);
  expect(other.problem?.code).toBe("not_found");
  expect((await api.call("DELETE", `/v1/console/staff/${id}`, { token: a.ownerToken, tenantId: a.id })).status).toBe(204);
});
