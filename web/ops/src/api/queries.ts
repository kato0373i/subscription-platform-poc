// 共通の読み込み：事業者（名前の表示に使う。operator-console §5.2）と業務の時計。
import { useQuery } from "@tanstack/react-query";
import { useApi } from "./hooks.ts";
import type { Tenant } from "./types.ts";
import { todayJST } from "../lib/format.ts";

export function useTenant(id: string | null | undefined) {
  const api = useApi();
  return useQuery({
    queryKey: ["tenant", id],
    queryFn: () => api.get<Tenant>(`/v1/ops/tenants/${id}`),
    enabled: !!id,
    // 名前の表示に使うだけなので、一度読んだら使い回す（詳細の画面は invalidate で読み直す）
    staleTime: 5 * 60_000,
  });
}

/**
 * 業務の今日と現在時刻。開発のビルドは dev clock（/v1/dev/clock）で時刻を進めるので、それに合わせる。
 * 本番のビルドにはこの API がない（404）ので、ブラウザの時計を使う。
 */
export function useBusinessClock() {
  const q = useQuery({
    queryKey: ["business-clock"],
    queryFn: async () => {
      try {
        const res = await fetch("/v1/dev/clock", { cache: "no-store" });
        if (res.ok) {
          const c = (await res.json()) as { now: string; today: string };
          return { today: c.today, offsetMs: new Date(c.now).getTime() - Date.now() };
        }
      } catch {
        /* 本番：ブラウザの時計 */
      }
      return { today: todayJST(), offsetMs: 0 };
    },
    staleTime: 60_000,
  });
  return { ready: !!q.data, today: q.data?.today ?? todayJST(), now: () => Date.now() + (q.data?.offsetMs ?? 0) };
}
