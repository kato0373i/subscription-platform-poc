// sink fixture：テスト用の Webhook 受信コンテナ webhook-sink（platform.md §10.3、integration §11.2 WS-1〜8）。
import { randomUUID } from "node:crypto";
import type { APIRequestContext } from "@playwright/test";

export type SinkRule =
  | { status: number; times?: number }
  | { delay_ms: number; status: number; times?: number }
  | { hang: true; times?: number }
  | { close: true; times?: number }
  | { status: 302; location: string; times?: number }
  | { status: 200; body_bytes: number; times?: number };

export interface SinkRecord {
  sink_id: string;
  seq: number;
  received_at: string;
  method: string;
  path: string;
  headers: Record<string, string[]>;
  body_base64: string;
  body_text: string;
  response: { status: number; rule: string };
}

export interface Sink {
  create(): Promise<{ sinkId: string; url: string }>;
  setBehavior(sinkId: string, rules: SinkRule[]): Promise<void>;
  requests(sinkId: string, opts?: { after?: number; waitMs?: number }): Promise<SinkRecord[]>;
  waitForRequests(sinkId: string, n: number, opts?: { timeout?: number }): Promise<SinkRecord[]>;
  stats(sinkId: string): Promise<{ count: number; max_in_flight: number }>;
  cleanup(): Promise<void>;
}

export function createSink(request: APIRequestContext, defaultTimeout: number): Sink {
  const control = process.env.SINK_CONTROL_URL ?? "http://localhost:18080";
  const target = process.env.SINK_TARGET_BASE ?? "http://localhost:18080";
  const created: string[] = [];
  const requests = async (sinkId: string, opts: { after?: number; waitMs?: number } = {}) => {
    const res = await request.get(`${control}/_control/sinks/${sinkId}/requests`, {
      params: { after: String(opts.after ?? 0), wait_ms: String(opts.waitMs ?? 0) },
      timeout: (opts.waitMs ?? 0) + 5_000,
    });
    return ((await res.json()) as { requests: SinkRecord[] }).requests;
  };
  return {
    async create() {
      const sinkId = `sink-${randomUUID()}`;
      created.push(sinkId);
      return { sinkId, url: `${target}/hooks/${sinkId}` };
    },
    async setBehavior(sinkId, rules) {
      const res = await request.put(`${control}/_control/sinks/${sinkId}/behavior`, { data: { rules } });
      if (!res.ok()) throw new Error(`sink behavior: ${res.status()}`);
    },
    requests,
    async waitForRequests(sinkId, n, opts = {}) {
      const deadline = Date.now() + (opts.timeout ?? defaultTimeout);
      let got: SinkRecord[] = [];
      while (got.length < n) {
        const left = deadline - Date.now();
        if (left <= 0) throw new Error(`sink: ${n} 件届きませんでした（${got.length} 件）: ${JSON.stringify(got.map((r) => r.response))}`);
        const more = await requests(sinkId, { after: got.at(-1)?.seq ?? 0, waitMs: Math.min(left, 10_000) });
        got = got.concat(more);
      }
      return got;
    },
    async stats(sinkId) {
      const res = await request.get(`${control}/_control/sinks/${sinkId}/stats`);
      return (await res.json()) as { count: number; max_in_flight: number };
    },
    async cleanup() {
      await Promise.all(created.map((id) => request.delete(`${control}/_control/sinks/${id}`).catch(() => undefined)));
    },
  };
}
