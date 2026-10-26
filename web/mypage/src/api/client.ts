// API の呼び出し（事業者コンソールと同じ。consumer-mypage ADR-M02）：Bearer（Cognito の ID トークン）・X-Tenant-Id・Idempotency-Key（作成系）・If-Match（編集系）を付け、
// Problem Details を型付きのエラーにする。401 はトークンを更新して 1 回だけ再試行する。
import type { ErrorCode } from "@sp/api-client";

export interface FieldError {
  field: string;
  code: string;
}

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: ErrorCode | "network_error" | "unknown",
    readonly correlationId: string | null,
    readonly fields: FieldError[] = [],
    readonly detail?: string,
  ) {
    super(`${status} ${code}`);
  }
}

export interface RequestOptions {
  body?: unknown;
  tenantId?: string;
  ifMatch?: number;
  idempotent?: boolean;
  query?: Record<string, string | number | string[] | undefined | null>;
}

export type TokenSource = (forceRefresh?: boolean) => Promise<string>;

function buildUrl(path: string, query?: RequestOptions["query"]): string {
  const u = new URL(path, window.location.origin);
  for (const [k, v] of Object.entries(query ?? {})) {
    if (v === undefined || v === null || v === "") continue;
    if (Array.isArray(v)) v.forEach((x) => u.searchParams.append(k, x));
    else u.searchParams.set(k, String(v));
  }
  return u.pathname + u.search;
}

export async function apiRequest<T>(token: TokenSource, method: string, path: string, opt: RequestOptions = {}, retried = false): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json", Authorization: `Bearer ${await token(retried)}` };
  if (opt.body !== undefined) headers["Content-Type"] = "application/json";
  if (opt.tenantId) headers["X-Tenant-Id"] = opt.tenantId;
  if (opt.idempotent) headers["Idempotency-Key"] = crypto.randomUUID();
  if (opt.ifMatch !== undefined) headers["If-Match"] = String(opt.ifMatch);
  let res: Response;
  try {
    res = await fetch(buildUrl(path, opt.query), { method, headers, body: opt.body === undefined ? undefined : JSON.stringify(opt.body), credentials: "omit" });
  } catch {
    throw new ApiError(0, "network_error", null);
  }
  const corr = res.headers.get("X-Correlation-Id");
  if (res.status === 401 && !retried) return apiRequest<T>(token, method, path, opt, true);
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  const json = text ? safeParse(text) : null;
  if (!res.ok) {
    const p = (json ?? {}) as { code?: ErrorCode; errors?: FieldError[]; detail?: string; correlation_id?: string };
    throw new ApiError(res.status, p.code ?? "unknown", p.correlation_id ?? corr, p.errors ?? [], p.detail);
  }
  return json as T;
}

function safeParse(t: string): unknown {
  try {
    return JSON.parse(t);
  } catch {
    return null;
  }
}

/** 画面のエラーの収集（NFR-009）。失敗しても何もしない。 */
export function reportClientError(token: TokenSource, e: { message: string; stack?: string; tenantId?: string; correlationId?: string }) {
  void apiRequest(token, "POST", "/v1/console/client-errors", {
    body: { message: e.message.slice(0, 2000), stack: e.stack?.slice(0, 6000), url: window.location.pathname.slice(0, 2000), tenant_id: e.tenantId, correlation_id: e.correlationId },
  }).catch(() => undefined);
}
