// identity.md §3.3・§7.2（API キー）：AC-IDN-016・019・020、C5・C6
import { createHash } from "node:crypto";
import type { ConsoleTenantComponents } from "@sp/api-client";
import { test, expect } from "../../fixtures/index.ts";

type ApiKey = ConsoleTenantComponents["schemas"]["ApiKey"];
type Issued = { id: string; name: string; key_prefix: string; api_key: string; created_at: string };

test("AC-IDN-016: 平文は発行の応答に 1 回だけ。一覧に無く、DB には SHA-256 だけが残る", async ({ api, world, db }) => {
  const tenant = await world.createTenant();
  const res = await api.call<Issued>("POST", "/v1/console/api-keys", { token: tenant.ownerToken, tenantId: tenant.id, body: { name: "本番" } });
  expect(res.status, JSON.stringify(res.body)).toBe(201);
  expect(res.body.api_key).toMatch(/^sk_[0-9A-Za-z]{12}_[0-9A-Za-z]{43}$/);
  expect(res.body.key_prefix).toBe(res.body.api_key.slice(0, 15));
  expect(res.headers["cache-control"]).toBe("no-store");

  const list = await api.call<{ data: ApiKey[] }>("GET", "/v1/console/api-keys", { token: tenant.ownerToken, tenantId: tenant.id });
  expect(JSON.stringify(list.body)).not.toContain(res.body.api_key);
  expect(list.body.data.find((k) => k.id === res.body.id)).toMatchObject({ name: "本番", status: "active", revoked_at: null });

  const raw = res.body.api_key;
  expect(await db.count("SELECT count(*) FROM tenant.api_keys k WHERE position($1 in (k.*)::text) > 0", [raw])).toBe(0);
  const rows = await db.query<{ key_hash: Buffer }>("SELECT key_hash FROM tenant.api_keys WHERE id = $1", [res.body.id]);
  expect(rows[0]!.key_hash.toString("hex")).toBe(createHash("sha256").update(raw).digest("hex"));
  // 監査記録 api_key.create（key_prefix・key_name があり、平文・hash が無い。AC-IDN-036）の確認は audit モジュールの実装後
});

test("AC-IDN-019: 失効したキーは 401。失効前に発行した別のキーは使える（ローテーション）", { tag: "@isolation:consoleRevokeApiKey" }, async ({ api, world }) => {
  const tenant = await world.createTenant();
  const other = await world.createTenant();
  const issue = async (name: string) =>
    (await api.call<Issued>("POST", "/v1/console/api-keys", { token: tenant.ownerToken, tenantId: tenant.id, body: { name } })).body;
  const oldKey = await issue("旧");
  const newKey = await issue("新");

  // 他テナントの owner からは失効できない（404）
  const cross = await api.call("DELETE", `/v1/console/api-keys/${oldKey.id}`, { token: other.ownerToken, tenantId: other.id });
  expect(cross.status).toBe(404);
  expect(cross.problem?.code).toBe("not_found");

  expect((await api.call("DELETE", `/v1/console/api-keys/${oldKey.id}`, { token: tenant.ownerToken, tenantId: tenant.id })).status).toBe(204);
  // 失効済みへの再実行も 204
  expect((await api.call("DELETE", `/v1/console/api-keys/${oldKey.id}`, { token: tenant.ownerToken, tenantId: tenant.id })).status).toBe(204);

  // 事業者 API（/v1/tenant/*）は subscription の実装前なので、キーの認証は console のルートで確かめる：
  // 有効なキーは「正しい資格情報だが主体が違う」403、失効したキーは「無効な資格情報」401（platform.md §7.1 の認証の順序）
  const revoked = await api.call("GET", "/v1/console/staff", { token: oldKey.api_key, tenantId: tenant.id });
  expect(revoked.status).toBe(401);
  expect(revoked.problem?.code).toBe("unauthenticated");
  const valid = await api.call("GET", "/v1/console/staff", { token: newKey.api_key, tenantId: tenant.id });
  expect(valid.status).toBe(403);

  const list = await api.call<{ data: ApiKey[] }>("GET", "/v1/console/api-keys", { token: tenant.ownerToken, tenantId: tenant.id, query: { status: "revoked" } });
  expect(list.body.data.map((k) => k.id)).toEqual([oldKey.id]);
  expect(list.body.data[0]!.revoked_at).not.toBeNull();
});

test("AC-IDN-020: 有効なキーは 10 本まで。1 本失効させると発行できる", async ({ api, world }) => {
  const tenant = await world.createTenant();
  const ids: string[] = [];
  for (let i = 1; i <= 10; i++) {
    const res = await api.call<Issued>("POST", "/v1/console/api-keys", { token: tenant.ownerToken, tenantId: tenant.id, body: { name: `k${i}` } });
    expect(res.status, `${i} 本目`).toBe(201);
    ids.push(res.body.id);
  }
  const over = await api.call("POST", "/v1/console/api-keys", { token: tenant.ownerToken, tenantId: tenant.id, body: { name: "k11" } });
  expect(over.status).toBe(409);
  expect(over.problem?.code).toBe("api_key_limit_exceeded");

  await api.call("DELETE", `/v1/console/api-keys/${ids[0]}`, { token: tenant.ownerToken, tenantId: tenant.id });
  const ok = await api.call("POST", "/v1/console/api-keys", { token: tenant.ownerToken, tenantId: tenant.id, body: { name: "k11" } });
  expect(ok.status).toBe(201);

  const active = await api.call<{ data: ApiKey[]; has_more: boolean }>("GET", "/v1/console/api-keys", {
    token: tenant.ownerToken,
    tenantId: tenant.id,
    query: { status: "active", limit: 100 },
  });
  expect(active.body.data).toHaveLength(10);
});

test("E14: キーの name の境界値", async ({ api, world }) => {
  const tenant = await world.createTenant();
  for (const [name, status] of [["", 400], ["n".repeat(101), 400], ["n".repeat(100), 201]] as const) {
    const res = await api.call("POST", "/v1/console/api-keys", { token: tenant.ownerToken, tenantId: tenant.id, body: { name } });
    expect(res.status, `name ${name.length} 文字`).toBe(status);
  }
});
