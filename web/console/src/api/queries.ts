// 画面で共有する問い合わせ（プランの名前の解決など）。
import { useQuery } from "@tanstack/react-query";
import { useApi } from "./hooks.ts";
import type { Page, Plan, Product } from "./types.ts";

/** 事業者の全プラン（一覧の表示名の解決とフィルターに使う。100 件まで）。 */
export function useAllPlans() {
  const api = useApi();
  return useQuery({ queryKey: ["plans", api.tenantId, "all"], queryFn: () => api.get<Page<Plan>>("/v1/console/plans", { limit: 100 }).then((r) => r.data) });
}

export function useAllProducts() {
  const api = useApi();
  return useQuery({ queryKey: ["products", api.tenantId, "all"], queryFn: () => api.get<Page<Product>>("/v1/console/products", { limit: 100 }).then((r) => r.data) });
}
