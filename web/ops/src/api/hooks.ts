// API を呼ぶ関数（TanStack Query から使う）。ops の API は X-Tenant-Id を使わない。
import { useMemo } from "react";
import { useAuth } from "../auth/AuthProvider.tsx";
import { apiRequest, type RequestOptions, type TokenSource } from "./client.ts";

export interface Api {
  get<T>(path: string, query?: RequestOptions["query"]): Promise<T>;
  post<T>(path: string, body?: unknown): Promise<T>;
}

export function useApi(): Api {
  const auth = useAuth();
  return useMemo(() => {
    const token: TokenSource = () => auth.idToken();
    return {
      get: (path, query) => apiRequest(token, "GET", path, { query }),
      post: (path, body) => apiRequest(token, "POST", path, { idempotent: true, body: body ?? {} }),
    };
  }, [auth]);
}
