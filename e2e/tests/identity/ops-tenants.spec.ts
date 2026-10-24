// identity.md §7.1（ops の事業者管理）：AC-IDN-001〜004・006、O6、E14
import type { OpsTenantComponents } from "@sp/api-client";
import { test, expect } from "../../fixtures/index.ts";

type Tenant = OpsTenantComponents["schemas"]["Tenant"];

const shop = () => `shop_${crypto.randomUUID().replaceAll("-", "")}`;

test("AC-IDN-001: 登録すると pending_review で作られ、イベントは出ない", async ({ api, tokens, db }) => {
  const op = await tokens.operator();
  const res = await api.call<Tenant>("POST", "/v1/ops/tenants", { token: op, body: { name: "  テスト事業者  " } });
  expect(res.status, JSON.stringify(res.body)).toBe(201);
  expect(res.body).toMatchObject({ name: "テスト事業者", status: "pending_review", fincode_shop_id: null, approved_at: null });
  expect(await db.count("SELECT count(*) FROM tenant.outbox_events WHERE aggregate_id = $1", [res.body.id])).toBe(0);

  const got = await api.call<Tenant>("GET", `/v1/ops/tenants/${res.body.id}`, { token: op });
  expect(got.status).toBe(200);
  expect(got.body.id).toBe(res.body.id);
});

test("AC-IDN-002・003: 承認で active になり tenant.approved が 1 件。同じ shop ID の再実行は 200、別の shop ID は 409", async ({ api, tokens, db }) => {
  const op = await tokens.operator();
  const created = await api.call<Tenant>("POST", "/v1/ops/tenants", { token: op, body: { name: "承認テスト" } });
  const id = created.body.id;
  const s = shop();
  const approved = await api.call<Tenant>("POST", `/v1/ops/tenants/${id}/approve`, { token: op, body: { fincode_shop_id: s } });
  expect(approved.status, JSON.stringify(approved.body)).toBe(200);
  expect(approved.body).toMatchObject({ status: "active", fincode_shop_id: s });
  expect(approved.body.approved_at).not.toBeNull();

  const again = await api.call<Tenant>("POST", `/v1/ops/tenants/${id}/approve`, { token: op, body: { fincode_shop_id: s } });
  expect(again.status).toBe(200);
  const events = await db.query<{ tenant_id: string; payload: { tenant_id: string; fincode_shop_id: string } }>(
    "SELECT tenant_id, payload FROM tenant.outbox_events WHERE aggregate_id = $1 AND event_type = 'tenant.approved'",
    [id],
  );
  expect(events).toHaveLength(1);
  expect(events[0]!.tenant_id).toBe(id);
  expect(events[0]!.payload).toMatchObject({ tenant_id: id, fincode_shop_id: s });

  const other = await api.call("POST", `/v1/ops/tenants/${id}/approve`, { token: op, body: { fincode_shop_id: shop() } });
  expect(other.status).toBe(409);
  expect(other.problem?.code).toBe("tenant_state_conflict");
});

test("AC-IDN-004: 別のテナントが使っている shop ID では承認できない", async ({ api, tokens }) => {
  const op = await tokens.operator();
  const s = shop();
  const a = await api.call<Tenant>("POST", "/v1/ops/tenants", { token: op, body: { name: "A" } });
  const b = await api.call<Tenant>("POST", "/v1/ops/tenants", { token: op, body: { name: "B" } });
  expect((await api.call("POST", `/v1/ops/tenants/${a.body.id}/approve`, { token: op, body: { fincode_shop_id: s } })).status).toBe(200);
  const res = await api.call("POST", `/v1/ops/tenants/${b.body.id}/approve`, { token: op, body: { fincode_shop_id: s } });
  expect(res.status).toBe(409);
  expect(res.problem?.code).toBe("fincode_shop_id_taken");
  const got = await api.call<Tenant>("GET", `/v1/ops/tenants/${b.body.id}`, { token: op });
  expect(got.body.status).toBe("pending_review");
});

test("AC-IDN-006: 停止で suspended になり tenant.suspended が 1 件。再実行は 200 で reason は変わらない", async ({ api, tokens, db, world }) => {
  const op = await tokens.operator();
  const tenant = await world.createTenant();
  const res = await api.call<Tenant>("POST", `/v1/ops/tenants/${tenant.id}/suspend`, { token: op, body: { reason: "規約違反" } });
  expect(res.status, JSON.stringify(res.body)).toBe(200);
  expect(res.body).toMatchObject({ status: "suspended", suspend_reason: "規約違反" });

  const again = await api.call<Tenant>("POST", `/v1/ops/tenants/${tenant.id}/suspend`, { token: op, body: { reason: "別の理由" } });
  expect(again.status).toBe(200);
  expect(again.body.suspend_reason).toBe("規約違反");
  const events = await db.query<{ payload: { tenant_id: string; reason: string } }>(
    "SELECT payload FROM tenant.outbox_events WHERE aggregate_id = $1 AND event_type = 'tenant.suspended'",
    [tenant.id],
  );
  expect(events).toHaveLength(1);
  expect(events[0]!.payload).toMatchObject({ tenant_id: tenant.id, reason: "規約違反" });

  // 停止は終端：承認し直せない
  const approve = await api.call("POST", `/v1/ops/tenants/${tenant.id}/approve`, { token: op, body: { fincode_shop_id: shop() } });
  expect(approve.problem?.code).toBe("tenant_state_conflict");
  // pending_review は停止できない
  const pending = await api.call<Tenant>("POST", "/v1/ops/tenants", { token: op, body: { name: "保留" } });
  const sp = await api.call("POST", `/v1/ops/tenants/${pending.body.id}/suspend`, { token: op, body: { reason: "x" } });
  expect(sp.problem?.code).toBe("tenant_state_conflict");
});

test("O6: 停止中のテナントにはスタッフを作れず、同じ subject は 409", async ({ api, tokens, world }) => {
  const op = await tokens.operator();
  const tenant = await world.createTenant();
  const dup = await api.call("POST", `/v1/ops/tenants/${tenant.id}/staff`, {
    token: op,
    body: { subject: tenant.ownerSub, email: `other-${tenant.ownerSub}@example.com`, display_name: "重複", role: "admin" },
  });
  expect(dup.status).toBe(409);
  expect(dup.problem?.code).toBe("staff_already_exists");

  await api.call("POST", `/v1/ops/tenants/${tenant.id}/suspend`, { token: op, body: { reason: "停止" } });
  const res = await api.call("POST", `/v1/ops/tenants/${tenant.id}/staff`, {
    token: op,
    body: { subject: "new-staff", email: "new-staff@example.com", display_name: "新規", role: "viewer" },
  });
  expect(res.status).toBe(403);
  expect(res.problem?.code).toBe("tenant_not_active");
});

test("O2: 状態で絞り込み、カーソルで次のページを取れる", async ({ api, tokens }) => {
  const op = await tokens.operator();
  for (let i = 0; i < 3; i++) await api.call("POST", "/v1/ops/tenants", { token: op, body: { name: `一覧 ${i}` } });
  const page1 = await api.call<{ data: Tenant[]; has_more: boolean; next_cursor: string | null }>("GET", "/v1/ops/tenants", {
    token: op,
    query: { status: "pending_review", limit: 2 },
  });
  expect(page1.status).toBe(200);
  expect(page1.body.data).toHaveLength(2);
  expect(page1.body.has_more).toBe(true);
  expect(page1.body.data.every((t) => t.status === "pending_review")).toBe(true);
  const page2 = await api.call<{ data: Tenant[] }>("GET", "/v1/ops/tenants", {
    token: op,
    query: { status: "pending_review", limit: 2, starting_after: page1.body.next_cursor! },
  });
  expect(page2.status).toBe(200);
  const ids = new Set(page1.body.data.map((t) => t.id));
  expect(page2.body.data.some((t) => ids.has(t.id))).toBe(false);

  const bad = await api.call("GET", "/v1/ops/tenants", { token: op, query: { starting_after: "not-a-cursor" } });
  expect(bad.problem?.code).toBe("validation_failed");
});

test("E14: name・reason・shop ID の境界値と、運営者以外の拒否", async ({ api, tokens, world }) => {
  const op = await tokens.operator();
  for (const [name, status] of [["", 400], ["あ".repeat(101), 400], ["あ".repeat(100), 201], ["a", 201]] as const) {
    const res = await api.call("POST", "/v1/ops/tenants", { token: op, body: { name } });
    expect(res.status, `name ${name.length} 文字`).toBe(status);
    if (status === 400) expect(res.problem?.errors?.[0]?.field).toBe("name");
  }
  const tenant = await world.createTenant();
  for (const reason of ["", "x".repeat(501)]) {
    const res = await api.call("POST", `/v1/ops/tenants/${tenant.id}/suspend`, { token: op, body: { reason } });
    expect(res.status).toBe(400);
  }
  const pending = await api.call<Tenant>("POST", "/v1/ops/tenants", { token: op, body: { name: "shop" } });
  const bad = await api.call("POST", `/v1/ops/tenants/${pending.body.id}/approve`, { token: op, body: { fincode_shop_id: "has space" } });
  expect(bad.problem?.errors?.[0]).toMatchObject({ field: "fincode_shop_id" });
  expect((await api.call("GET", `/v1/ops/tenants/${crypto.randomUUID()}`, { token: op })).status).toBe(404);

  // operators グループのない運営者プールの主体・スタッフは拒否される
  const noGroup = await tokens.issue({ pool: "operator", groups: ["viewers"] });
  expect((await api.call("GET", "/v1/ops/tenants", { token: noGroup.token })).status).toBe(403);
  expect((await api.call("GET", "/v1/ops/tenants", { token: tenant.ownerToken })).status).toBe(403);
});
