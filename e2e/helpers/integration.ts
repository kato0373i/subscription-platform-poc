// Integration（事業者 Webhook・メール）の E2E の準備（integration.md §11）。送信先は webhook-sink（sink fixture）を使う。
import { expect } from "@playwright/test";
import type { ConsoleIntegrationComponents } from "@sp/api-client";
import type { Api } from "../fixtures/api.ts";
import type { Db } from "../fixtures/db.ts";
import type { Sink, SinkRecord } from "../fixtures/sink.ts";
import type { Wait } from "../fixtures/wait.ts";
import type { TenantWorld } from "../fixtures/world.ts";
import { consoleOf } from "./catalog.ts";

export type Endpoint = ConsoleIntegrationComponents["schemas"]["Endpoint"];
export type Delivery = ConsoleIntegrationComponents["schemas"]["Delivery"];
export type CreatedEndpoint = Endpoint & { secret: string };

export const ALL_EVENT_TYPES = [
  "subscription.started", "subscription.trial_will_end", "subscription.renewed", "subscription.past_due", "subscription.recovered",
  "subscription.plan_change_scheduled", "subscription.plan_change_revoked", "subscription.plan_changed",
  "subscription.cancellation_scheduled", "subscription.cancellation_revoked", "subscription.canceled", "invoice.paid", "invoice.payment_failed",
];

/** sink を送信先として登録する（owner）。 */
export async function createEndpoint(api: Api, tenant: TenantWorld, url: string, eventTypes = ALL_EVENT_TYPES, token = tenant.ownerToken): Promise<CreatedEndpoint> {
  const res = await consoleOf(api, tenant, token)<CreatedEndpoint>("POST", "/v1/console/webhook-endpoints", { url, event_types: eventTypes });
  expect(res.status, JSON.stringify(res.body)).toBe(201);
  return res.body;
}

/** 新しい sink を作って送信先として登録する。 */
export async function sinkEndpoint(api: Api, sink: Sink, tenant: TenantWorld, eventTypes = ALL_EVENT_TYPES) {
  const s = await sink.create();
  const ep = await createEndpoint(api, tenant, s.url, eventTypes);
  return { ...s, endpoint: ep };
}

/** sink の記録の本文（JSON）。 */
export function bodyOf<T = WebhookBody>(rec: SinkRecord): T {
  return JSON.parse(rec.body_text) as T;
}

export function header(rec: SinkRecord, name: string): string | undefined {
  const k = Object.keys(rec.headers).find((h) => h.toLowerCase() === name.toLowerCase());
  return k ? rec.headers[k]?.[0] : undefined;
}

export interface WebhookBody {
  id: string;
  type: string;
  api_version: string;
  created_at: string;
  tenant_id: string;
  data: { object: Record<string, unknown> & { object: string; id: string; version: number }; [k: string]: unknown };
}

/** 一定時間 sink に新しい記録が来ないこと（届かないことの確認。時刻ではなく配信ループの数周期分を待つ）。 */
export async function expectNoMoreRequests(sink: Sink, sinkId: string, after: number, waitMs = 3_000) {
  const more = await sink.requests(sinkId, { after, waitMs });
  expect(more.map((r) => bodyOf(r).type)).toEqual([]);
}

/** 配信の行（DB）。 */
export async function deliveries(db: Db, endpointId: string) {
  return db.query<{ id: string; status: string; attempt_count: number; trigger: string; webhook_event_id: string; last_error: string | null }>(
    "SELECT id, status, attempt_count, trigger, webhook_event_id, last_error FROM integration.webhook_deliveries WHERE endpoint_id = $1 ORDER BY created_at, id",
    [endpointId],
  );
}

export async function untilDelivery(db: Db, wait: Wait, endpointId: string, pred: (d: { status: string; attempt_count: number }) => boolean, message: string) {
  return wait.until(async () => (await deliveries(db, endpointId)).find(pred), { message });
}
