// 画面で共有する問い合わせ。
import { useQuery } from "@tanstack/react-query";
import { useApi } from "./hooks.ts";
import type { Me, Page, PaymentMethod, Subscription } from "./types.ts";

/** 自分の契約（すべての事業者。100 件まで。契約者の契約は通常数件）。 */
export function useSubscriptions() {
  const api = useApi();
  // 申込ページ・メールのリンクなど画面の外で変わるので、画面を開くたびに読み直す
  return useQuery({ queryKey: ["subscriptions"], staleTime: 0, queryFn: () => api.get<Page<Subscription>>("/v1/me/subscriptions", { limit: 100 }).then((r) => r.data) });
}

export function useSubscription(id: string | undefined) {
  const api = useApi();
  return useQuery({ queryKey: ["subscription", id], staleTime: 0, queryFn: () => api.get<Subscription>(`/v1/me/subscriptions/${id}`), enabled: !!id });
}

export function usePaymentMethods() {
  const api = useApi();
  return useQuery({ queryKey: ["payment-methods"], staleTime: 0, queryFn: () => api.get<{ data: PaymentMethod[] }>("/v1/me/payment-methods").then((r) => r.data.filter((p) => p.status === "active")) });
}

export function useMe() {
  const api = useApi();
  return useQuery({ queryKey: ["me"], queryFn: () => api.get<Me>("/v1/me"), staleTime: 60_000, retry: false });
}

/** 有効な契約（退会・カードの削除の判定に使う）。 */
export const isOpen = (s: Subscription) => s.status === "trialing" || s.status === "active" || s.status === "past_due" || s.status === "pending_initial";
