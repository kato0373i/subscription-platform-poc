// 送信先の作成・検証・鍵・認可・テナント分離（integration.md §7.1）：AC-INT-001・002・004・016・017、E18〜E20。
import { test, expect } from "../../fixtures/index.ts";
import { consoleOf, createPlan } from "../../helpers/catalog.ts";
import { ALL_EVENT_TYPES, createEndpoint, type CreatedEndpoint, type Delivery, type Endpoint, sinkEndpoint } from "../../helpers/integration.ts";
import { expectIsolated } from "../../helpers/isolation.ts";
import { subscribe } from "../../helpers/orchestration.ts";

test("AC-INT-001・002: https 以外は url_not_https（例外のホストだけ http を許す）。一覧に出てこない", async ({ api, world }) => {
  const tenant = await world.createTenant();
  const call = consoleOf(api, tenant);
  for (const url of ["http://example.com/hook", "http://other-host/hooks/x"]) {
    const res = await call("POST", "/v1/console/webhook-endpoints", { url, event_types: ["invoice.paid"] });
    expect(res.status, url).toBe(400);
    expect(res.problem?.code).toBe("validation_failed");
    expect(res.problem?.errors).toContainEqual({ field: "url", code: "url_not_https" });
  }
  const ok = await call<Endpoint>("POST", "/v1/console/webhook-endpoints", { url: "http://webhook-sink:8080/hooks/x", event_types: ["invoice.paid"] });
  expect(ok.status, JSON.stringify(ok.body)).toBe(201);
  const forbidden = await call("POST", "/v1/console/webhook-endpoints", { url: "https://169.254.169.254/latest", event_types: ["invoice.paid"] });
  expect(forbidden.problem?.errors).toContainEqual({ field: "url", code: "url_forbidden_host" });
  const list = await call<{ data: Endpoint[] }>("GET", "/v1/console/webhook-endpoints");
  expect(list.body.data.map((e) => e.url)).toEqual(["http://webhook-sink:8080/hooks/x"]);
});

test("AC-INT-004: 作成の応答にだけ whsec_ の鍵があり、GET にはない。DB には暗号文だけ", async ({ api, world, db }) => {
  const tenant = await world.createTenant();
  const ep = await createEndpoint(api, tenant, "https://example.com/hook", ["invoice.paid", "invoice.paid", "subscription.started"]);
  expect(ep.secret).toMatch(/^whsec_[A-Za-z0-9_-]{43}$/);
  expect(ep).toMatchObject({ status: "enabled", api_version: "2026-10-01", version: 1, event_types: ["invoice.paid", "subscription.started"] }); // E18：重複は 1 つ
  const got = await consoleOf(api, tenant)<Endpoint>("GET", `/v1/console/webhook-endpoints/${ep.id}`);
  expect(got.status).toBe(200);
  expect(got.body).not.toHaveProperty("secret");
  expect(JSON.stringify(got.body)).not.toContain(ep.secret);
  const [row] = await db.query<{ ct: string; key_id: string }>(
    "SELECT encode(secret_current_ciphertext, 'escape') AS ct, secret_key_id AS key_id FROM integration.webhook_endpoints WHERE id = $1",
    [ep.id],
  );
  expect(row!.ct).not.toContain(ep.secret.slice(6));
  expect(row!.ct).not.toContain("whsec_");
  expect(row!.key_id).toMatch(/^aesgcm:/);
});

test("E18・E20：未知のイベントは 400、11 個目の送信先は 409（削除済みは数えない）", async ({ api, world }) => {
  const tenant = await world.createTenant();
  const call = consoleOf(api, tenant);
  const unknown = await call("POST", "/v1/console/webhook-endpoints", { url: "https://example.com/a", event_types: ["invoice.issued"] });
  expect(unknown.problem?.errors).toContainEqual({ field: "event_types", code: "event_type_unknown" });
  const empty = await call("POST", "/v1/console/webhook-endpoints", { url: "https://example.com/a", event_types: [] });
  expect(empty.problem?.errors).toContainEqual({ field: "event_types", code: "event_types_empty" });
  const created: CreatedEndpoint[] = [];
  for (let i = 0; i < 10; i++) created.push(await createEndpoint(api, tenant, `https://example.com/${i}`, ["invoice.paid"]));
  const eleventh = await call("POST", "/v1/console/webhook-endpoints", { url: "https://example.com/10", event_types: ["invoice.paid"] });
  expect(eleventh.status).toBe(409);
  expect(eleventh.problem?.code).toBe("webhook_endpoint_limit_exceeded");
  expect((await call("DELETE", `/v1/console/webhook-endpoints/${created[0]!.id}`)).status).toBe(204);
  expect((await call("POST", "/v1/console/webhook-endpoints", { url: "https://example.com/10", event_types: ["invoice.paid"] })).status).toBe(201);
  const page1 = await call<{ data: Endpoint[]; has_more: boolean; next_cursor: string }>("GET", "/v1/console/webhook-endpoints", undefined, { limit: 6 });
  const page2 = await call<{ data: Endpoint[]; has_more: boolean }>("GET", "/v1/console/webhook-endpoints", undefined, { limit: 6, starting_after: page1.body.next_cursor });
  expect(page1.body.has_more).toBe(true);
  expect(page2.body.has_more).toBe(false);
  expect([...page1.body.data, ...page2.body.data].map((e) => e.id)).not.toContain(created[0]!.id);
  expect(page1.body.data.length + page2.body.data.length).toBe(10);
});

test("PATCH の部分更新と If-Match（古い version は 409）、削除済みは 404", { tag: "@isolation:consoleUpdateWebhookEndpoint" }, async ({ api, world }) => {
  const tenant = await world.createTenant();
  const other = await world.createTenant();
  const ep = await createEndpoint(api, tenant, "https://example.com/hook", ["invoice.paid"]);
  const call = consoleOf(api, tenant);
  const upd = await api.call<Endpoint>("PATCH", `/v1/console/webhook-endpoints/${ep.id}`, {
    token: tenant.ownerToken, tenantId: tenant.id, body: { enabled: false, description: "停止中" }, headers: { "If-Match": "1" },
  });
  expect(upd.status, JSON.stringify(upd.body)).toBe(200);
  expect(upd.body).toMatchObject({ status: "disabled", description: "停止中", version: 2, url: "https://example.com/hook" });
  const stale = await api.call("PATCH", `/v1/console/webhook-endpoints/${ep.id}`, {
    token: tenant.ownerToken, tenantId: tenant.id, body: { enabled: true }, headers: { "If-Match": "1" },
  });
  expect(stale.status).toBe(409);
  expect(stale.problem?.code).toBe("conflict");
  await expectIsolated((as) =>
    as === "owner"
      ? call("PATCH", `/v1/console/webhook-endpoints/${ep.id}`, { enabled: true })
      : consoleOf(api, other)("PATCH", `/v1/console/webhook-endpoints/${ep.id}`, { enabled: true }),
  );
  expect((await call("DELETE", `/v1/console/webhook-endpoints/${ep.id}`)).status).toBe(204);
  expect((await call("GET", `/v1/console/webhook-endpoints/${ep.id}`)).status).toBe(404);
  expect((await call("PATCH", `/v1/console/webhook-endpoints/${ep.id}`, { enabled: true })).status).toBe(404);
});

test("AC-INT-016: viewer は作成・再送が 403、一覧・取得は 200。admin は作成が 403・再送は可（identity §3.4）", async ({ api, world, sink, wait, db }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const { endpoint: ep } = await sinkEndpoint(api, sink, tenant, ["subscription.started"]);
  await subscribe(api, world, tenant, plan.id);
  const delivery = await wait.until(async () => (await db.query<{ id: string }>("SELECT id FROM integration.webhook_deliveries WHERE endpoint_id = $1 AND status = 'succeeded'", [ep.id]))[0], {
    message: "配信が succeeded になる",
  });
  const viewer = await world.createStaff(tenant, "viewer");
  const admin = await world.createStaff(tenant, "admin");
  const asViewer = consoleOf(api, tenant, viewer.token);
  expect((await asViewer("POST", "/v1/console/webhook-endpoints", { url: "https://example.com/v", event_types: ["invoice.paid"] })).status).toBe(403);
  expect((await asViewer("POST", `/v1/console/webhook-deliveries/${delivery.id}/redeliver`)).status).toBe(403);
  expect((await asViewer("GET", "/v1/console/webhook-endpoints")).status).toBe(200);
  expect((await asViewer("GET", `/v1/console/webhook-endpoints/${ep.id}`)).status).toBe(200);
  expect((await asViewer("GET", "/v1/console/webhook-deliveries")).status).toBe(200);
  expect((await asViewer("GET", `/v1/console/webhook-deliveries/${delivery.id}`)).status).toBe(200);
  const asAdmin = consoleOf(api, tenant, admin.token);
  expect((await asAdmin("POST", "/v1/console/webhook-endpoints", { url: "https://example.com/a", event_types: ["invoice.paid"] })).status).toBe(403);
  const redo = await asAdmin<Delivery>("POST", `/v1/console/webhook-deliveries/${delivery.id}/redeliver`);
  expect(redo.status, JSON.stringify(redo.body)).toBe(202);
  expect(redo.body).toMatchObject({ trigger: "manual", redelivery_of: delivery.id, status: "pending" });
});

test(
  "AC-INT-017: 事業者 A の staff は B の送信先・配信を取得・変更・再送できない（404）",
  { tag: ["@isolation:consoleGetWebhookEndpoint", "@isolation:consoleDeleteWebhookEndpoint", "@isolation:consoleRotateWebhookSecret", "@isolation:consoleGetWebhookDelivery", "@isolation:consoleRedeliverWebhook"] },
  async ({ api, world, sink, wait, db }) => {
    const b = await world.createTenant();
    const a = await world.createTenant();
    const plan = await createPlan(api, b, { amount: 980 });
    const { endpoint: ep } = await sinkEndpoint(api, sink, b, ["subscription.started"]);
    await subscribe(api, world, b, plan.id);
    const d = await wait.until(async () => (await db.query<{ id: string }>("SELECT id FROM integration.webhook_deliveries WHERE endpoint_id = $1 AND status = 'succeeded'", [ep.id]))[0], {
      message: "配信が succeeded になる",
    });
    const asB = consoleOf(api, b);
    const asA = consoleOf(api, a);
    const pick = (as: "owner" | "other") => (as === "owner" ? asB : asA);
    await expectIsolated((as) => pick(as)("GET", `/v1/console/webhook-endpoints/${ep.id}`));
    await expectIsolated((as) => pick(as)("GET", `/v1/console/webhook-deliveries/${d.id}`));
    await expectIsolated((as) => pick(as)("POST", `/v1/console/webhook-deliveries/${d.id}/redeliver`));
    await expectIsolated((as) => pick(as)("POST", `/v1/console/webhook-endpoints/${ep.id}/rotate-secret`, { grace_period_hours: 0 }));
    const aList = await asA<{ data: Delivery[] }>("GET", "/v1/console/webhook-deliveries", undefined, { endpoint_id: ep.id });
    expect(aList.body.data).toEqual([]);
    await expectIsolated((as) => pick(as)("DELETE", `/v1/console/webhook-endpoints/${ep.id}`));
  },
);

test("全 13 種類の event_types で作れる", async ({ api, world }) => {
  const tenant = await world.createTenant();
  const ep = await createEndpoint(api, tenant, "https://example.com/all", ALL_EVENT_TYPES);
  expect(ep.event_types).toHaveLength(13);
});
