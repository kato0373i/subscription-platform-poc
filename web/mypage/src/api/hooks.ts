// API を呼ぶ関数（TanStack Query から使う）。契約者の API は X-Tenant-Id を使わない。
import { useMemo } from "react";
import { useAuth } from "../auth/AuthProvider.tsx";
import { apiRequest, type RequestOptions, type TokenSource } from "./client.ts";

export interface Api {
  get<T>(path: string, query?: RequestOptions["query"]): Promise<T>;
  post<T>(path: string, body?: unknown): Promise<T>;
  put<T>(path: string, body?: unknown): Promise<T>;
  del(path: string): Promise<void>;
}

export function useApi(): Api {
  const auth = useAuth();
  return useMemo(() => {
    const token: TokenSource = () => auth.idToken();
    return {
      get: (path, query) => apiRequest(token, "GET", path, { query }),
      post: (path, body) => apiRequest(token, "POST", path, { idempotent: true, body }),
      put: (path, body) => apiRequest(token, "PUT", path, { idempotent: true, body: body ?? {} }),
      del: (path) => apiRequest(token, "DELETE", path, {}),
    };
  }, [auth]);
}
