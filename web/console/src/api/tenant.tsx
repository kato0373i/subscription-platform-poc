// 選んだ事業者と、そのロールで使える操作（設計 §5.5）。事業者は URL の /t/:tenantId/ で決まる。
import { useQuery } from "@tanstack/react-query";
import { createContext, useContext, type ReactNode } from "react";
import type { ConsoleTenantComponents } from "@sp/api-client";
import { useApi } from "./hooks.ts";

export type Membership = ConsoleTenantComponents["schemas"]["Membership"];

export function useMemberships() {
  const api = useApi();
  return useQuery({ queryKey: ["memberships"], queryFn: () => api.get<{ data: Membership[] }>("/v1/console/memberships").then((r) => r.data), staleTime: 60_000 });
}

const TenantContext = createContext<Membership | null>(null);

export function TenantProvider({ membership, children }: { membership: Membership; children: ReactNode }) {
  return <TenantContext.Provider value={membership}>{children}</TenantContext.Provider>;
}

export function useTenant(): Membership {
  const t = useContext(TenantContext);
  if (!t) throw new Error("useTenant outside TenantProvider");
  return t;
}

/** ロールで使える操作か（停止中の事業者では書き込みの操作を使えない。FR-004・FR-005）。 */
export function useCan(permission: string): { allowed: boolean; reason: string | null } {
  const t = useTenant();
  if (!t.permissions.includes(permission)) {
    const needOwner = ["webhook.manage", "apikey.manage", "staff.manage"].includes(permission);
    return { allowed: false, reason: needOwner ? "この操作にはオーナーの権限が必要です" : "この操作には管理者以上の権限が必要です" };
  }
  if (permission !== "console.read" && t.tenant_status === "suspended") return { allowed: false, reason: "この事業者は停止されています（閲覧のみ）" };
  return { allowed: true, reason: null };
}
