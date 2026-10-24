// api fixture（platform.md §10.2）。すべての呼び出しに logs fixture の相関 ID を X-Correlation-Id で付ける。
import type { APIRequestContext, APIResponse } from "@playwright/test";
import type { DevPaths, Problem } from "@sp/api-client";

export interface CallOptions {
  token?: string;
  tenantId?: string;
  idempotencyKey?: string;
  body?: unknown;
  query?: Record<string, string | number | undefined>;
  headers?: Record<string, string>;
}

export interface ApiResult<T = unknown> {
  status: number;
  headers: Record<string, string>;
  body: T;
  problem?: Problem;
  raw: APIResponse;
}

export interface Api {
  correlationId: string;
  call<T = unknown>(method: string, path: string, opts?: CallOptions): Promise<ApiResult<T>>;
  dev: DevApi;
}

type DevJson<P extends keyof DevPaths, M extends string> = DevPaths[P] extends Record<M, { responses: { 200: { content: { "application/json": infer R } } } }>
  ? R
  : unknown;

export interface DevApi {
  getClock(): Promise<ApiResult<DevJson<"/v1/dev/clock", "get">>>;
  setClock(body: { advance?: string; to?: string }): Promise<ApiResult<DevJson<"/v1/dev/clock", "post">>>;
  runJob(job: string, body?: { as_of?: string; params?: object; force?: boolean; wait_seconds?: number }): Promise<ApiResult<DevJson<"/v1/dev/jobs/{job}/run", "post">>>;
  issueToken(body: { pool: "consumer" | "staff" | "operator"; sub: string; email?: string; email_verified?: boolean; name?: string; groups?: string[]; ttl_seconds?: number }): Promise<ApiResult<DevJson<"/v1/dev/tokens", "post">>>;
}

export function createApi(request: APIRequestContext, correlationId: string): Api {
  const call = async <T>(method: string, path: string, opts: CallOptions = {}): Promise<ApiResult<T>> => {
    const headers: Record<string, string> = { "X-Correlation-Id": correlationId, ...opts.headers };
    if (opts.token) headers.Authorization = `Bearer ${opts.token}`;
    if (opts.tenantId) headers["X-Tenant-Id"] = opts.tenantId;
    if (opts.idempotencyKey) headers["Idempotency-Key"] = opts.idempotencyKey;
    const params: Record<string, string> = {};
    for (const [k, v] of Object.entries(opts.query ?? {})) if (v !== undefined) params[k] = String(v);
    const raw = await request.fetch(path, {
      method,
      headers,
      params,
      data: opts.body === undefined ? undefined : opts.body,
      failOnStatusCode: false,
    });
    const text = await raw.text();
    let body: unknown = text;
    try {
      body = text === "" ? undefined : JSON.parse(text);
    } catch {
      /* JSON でない本文はそのまま */
    }
    const ct = raw.headers()["content-type"] ?? "";
    return {
      status: raw.status(),
      headers: raw.headers(),
      body: body as T,
      problem: ct.includes("problem+json") ? (body as Problem) : undefined,
      raw,
    };
  };
  return {
    correlationId,
    call,
    dev: {
      getClock: () => call("GET", "/v1/dev/clock"),
      setClock: (body) => call("POST", "/v1/dev/clock", { body }),
      runJob: (job, body = {}) => call("POST", `/v1/dev/jobs/${job}/run`, { body }),
      issueToken: (body) => call("POST", "/v1/dev/tokens", { body }),
    },
  };
}
