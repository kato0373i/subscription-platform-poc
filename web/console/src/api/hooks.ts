// API を呼ぶ関数（TanStack Query から使う）。事業者の中の画面では X-Tenant-Id を自動で付ける。
import { useMemo } from "react";
import { useParams } from "react-router";
import { useAuth } from "../auth/AuthProvider.tsx";
import { apiRequest, type RequestOptions, type TokenSource } from "./client.ts";

export interface Api {
  token: TokenSource;
  tenantId?: string;
  get<T>(path: string, query?: RequestOptions["query"]): Promise<T>;
  post<T>(path: string, body?: unknown, o?: Omit<RequestOptions, "body">): Promise<T>;
  patch<T>(path: string, body: unknown, ifMatch?: number): Promise<T>;
  put<T>(path: string, body: unknown, ifMatch?: number): Promise<T>;
  del(path: string): Promise<void>;
}

export function useApi(): Api {
  const auth = useAuth();
  const { tenantId } = useParams();
  return useMemo(() => {
    const token: TokenSource = () => auth.idToken();
    const base = { tenantId };
    return {
      token,
      tenantId,
      get: (path, query) => apiRequest(token, "GET", path, { ...base, query }),
      post: (path, body, o) => apiRequest(token, "POST", path, { ...base, idempotent: true, ...o, body }),
      patch: (path, body, ifMatch) => apiRequest(token, "PATCH", path, { ...base, body, ifMatch }),
      put: (path, body, ifMatch) => apiRequest(token, "PUT", path, { ...base, body, ifMatch }),
      del: (path) => apiRequest(token, "DELETE", path, base),
    };
  }, [auth, tenantId]);
}
