// 監査の記録（integration.md §3.6・§7.2）：AC-INT-027・028・030・031、FR-025・NFR-014。
import type { OpsAuditComponents } from "@sp/api-client";
import { test, expect } from "../../fixtures/index.ts";
import type { Api } from "../../fixtures/api.ts";
import type { Wait } from "../../fixtures/wait.ts";
import { consoleOf, createPlan } from "../../helpers/catalog.ts";
import { createEndpoint } from "../../helpers/integration.ts";

type AuditRecord = OpsAuditComponents["schemas"]["AuditRecord"];

async function records(api: Api, op: string, query: Record<string, string>) {
  const res = await api.call<{ data: AuditRecord[]; has_more: boolean; next_cursor: string | null }>("GET", "/v1/ops/audit-records", { token: op, query });
  expect(res.status, JSON.stringify(res.body)).toBe(200);
  return res.body;
}

async function untilRecord(api: Api, wait: Wait, op: string, query: Record<string, string>) {
  return wait.until(async () => (await records(api, op, query)).data[0], { message: `監査 ${JSON.stringify(query)}` });
}

test("AC-INT-027: admin のプランの価格改定（1,000 円 → 1,200 円）が actor・plan_id・before/after 付きで残る", async ({ api, world, tokens, wait }) => {
  const tenant = await world.createTenant();
  const admin = await world.createStaff(tenant, "admin");
  const plan = await createPlan(api, tenant, { amount: 1000 });
  const res = await consoleOf(api, tenant, admin.token)("POST", `/v1/console/plans/${plan.id}/price-versions`, { amount: 1200 });
  expect(res.status, JSON.stringify(res.body)).toBe(201);
  const op = await tokens.operator();
  const rec = await untilRecord(api, wait, op, { tenant_id: tenant.id, action: "plan.price_version_add" });
  expect(rec).toMatchObject({
    tenant_id: tenant.id,
    actor: { type: "staff", id: admin.sub, role: "admin" },
    target: { type: "plan", id: plan.id },
    outcome: "succeeded",
    http_method: "POST",
    route: "POST /v1/console/plans/{plan_id}/price-versions",
  });
  expect(rec.before).toMatchObject({ amount: 1000 });
  expect(rec.after).toMatchObject({ amount: 1200 });
  expect(Date.parse(rec.occurred_at)).not.toBeNaN();
  expect(rec.request_id).not.toBe("");
});

test("AC-INT-028: 運営者の事業者停止が tenant.suspend・operator・reason 付きで残る", async ({ api, world, tokens, wait }) => {
  const tenant = await world.createTenant();
  const op = await tokens.operator();
  const res = await api.call("POST", `/v1/ops/tenants/${tenant.id}/suspend`, { token: op, body: { reason: "audit e2e" } });
  expect(res.status, JSON.stringify(res.body)).toBe(200);
  const rec = await untilRecord(api, wait, op, { tenant_id: tenant.id, action: "tenant.suspend" });
  expect(rec.actor.type).toBe("operator");
  expect(rec.after).toMatchObject({ status: "suspended", reason: "audit e2e" });
  expect(rec.target).toEqual({ type: "tenant", id: tenant.id });
});

test("AC-INT-030: 鍵のローテーションの監査に、鍵の平文・暗号文が含まれない", async ({ api, world, tokens, wait, db }) => {
  const tenant = await world.createTenant();
  const ep = await createEndpoint(api, tenant, "https://example.com/audit", ["invoice.paid"]);
  const rot = await consoleOf(api, tenant)<{ secret: string; previous_secret_expires_at: string }>("POST", `/v1/console/webhook-endpoints/${ep.id}/rotate-secret`, {
    grace_period_hours: 24,
  });
  expect(rot.status, JSON.stringify(rot.body)).toBe(200);
  expect(rot.body.secret).toMatch(/^whsec_/);
  const op = await tokens.operator();
  const rec = await untilRecord(api, wait, op, { tenant_id: tenant.id, action: "webhook_endpoint.rotate_secret" });
  expect(rec.target).toEqual({ type: "webhook_endpoint", id: ep.id });
  expect(Date.parse(String((rec.after as { previous_expires_at: string }).previous_expires_at))).toBe(Date.parse(rot.body.previous_secret_expires_at));
  const created = await untilRecord(api, wait, op, { tenant_id: tenant.id, action: "webhook_endpoint.create" });
  const [ct] = await db.query<{ hex: string }>("SELECT encode(secret_current_ciphertext, 'hex') AS hex FROM integration.webhook_endpoints WHERE id = $1", [ep.id]);
  const rows = await db.query<{ row: string }>("SELECT to_jsonb(r)::text AS row FROM audit.audit_records r WHERE tenant_id = $1", [tenant.id]);
  expect(rows.length).toBeGreaterThanOrEqual(2);
  for (const { row } of rows) {
    for (const s of [ep.secret, rot.body.secret, ep.secret.slice(6), rot.body.secret.slice(6), ct!.hex, "whsec_"]) expect(row).not.toContain(s);
  }
  expect(created.after).toMatchObject({ url: "https://example.com/audit", event_types: ["invoice.paid"], status: "enabled" });
});

test("AC-INT-031: viewer の送信先の作成（403）は outcome = denied で残る", async ({ api, world, tokens, wait }) => {
  const tenant = await world.createTenant();
  const viewer = await world.createStaff(tenant, "viewer");
  const res = await consoleOf(api, tenant, viewer.token)("POST", "/v1/console/webhook-endpoints", { url: "https://example.com/v", event_types: ["invoice.paid"] });
  expect(res.status).toBe(403);
  const op = await tokens.operator();
  const rec = await untilRecord(api, wait, op, { tenant_id: tenant.id, actor_id: viewer.sub });
  expect(rec).toMatchObject({ outcome: "denied", action: "http.post", actor: { type: "staff", id: viewer.sub, role: "viewer" }, route: "POST /v1/console/webhook-endpoints" });
});

test("ops の閲覧：action・期間で絞り込み、カーソルで次のページを読む", async ({ api, world, tokens }) => {
  const tenant = await world.createTenant();
  for (let i = 0; i < 3; i++) await createEndpoint(api, tenant, `https://example.com/p${i}`, ["invoice.paid"]);
  const op = await tokens.operator();
  const p1 = await records(api, op, { tenant_id: tenant.id, action: "webhook_endpoint.create", limit: "2" });
  expect(p1.data).toHaveLength(2);
  expect(p1.has_more).toBe(true);
  const p2 = await records(api, op, { tenant_id: tenant.id, action: "webhook_endpoint.create", limit: "2", starting_after: p1.next_cursor! });
  expect(p2.data).toHaveLength(1);
  expect(p2.has_more).toBe(false);
  const all = [...p1.data, ...p2.data];
  expect(new Set(all.map((r) => r.id)).size).toBe(3);
  expect(Date.parse(all[0]!.occurred_at)).toBeGreaterThanOrEqual(Date.parse(all[2]!.occurred_at));
  const future = await records(api, op, { tenant_id: tenant.id, occurred_from: "2999-01-01T00:00:00Z" });
  expect(future.data).toEqual([]);
});
