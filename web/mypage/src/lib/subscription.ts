// 契約の表示の共通の処理（FR-004・FR-005）。
import type { Subscription } from "../api/types.ts";
import { intervalLabel } from "../api/types.ts";
import { date, yen } from "./format.ts";

/** 一覧の並び順：支払い遅延 → 利用中・トライアル中 → その他（解約済みなど）。同じ区分は新しい順。 */
export function sortSubscriptions(xs: Subscription[]): Subscription[] {
  const rank = (s: Subscription) => (s.status === "past_due" ? 0 : s.status === "active" || s.status === "trialing" || s.status === "pending_initial" ? 1 : 2);
  return [...xs].sort((a, b) => rank(a) - rank(b) || b.created_at.localeCompare(a.created_at));
}

export function priceText(s: Subscription): string {
  return s.display ? `${yen(s.display.amount)}／${intervalLabel[s.interval]}` : "—";
}

/** 期間・次の予定の 1 行の説明。 */
export function scheduleText(s: Subscription): string {
  switch (s.status) {
    case "trialing":
      return s.scheduled_cancellation
        ? `トライアルは ${date(s.trial?.end)} まで（終了後に解約されます）`
        : `トライアルは ${date(s.trial?.end)} まで。${date(s.next_renewal_date)} から有料になります`;
    case "active":
      return s.scheduled_cancellation ? `${date(s.current_period.end)} まで利用できます（解約予定）` : `次回の更新日 ${date(s.next_renewal_date)}`;
    case "past_due":
      return "お支払いが確認できていません。支払方法を変更してください";
    case "canceled":
      return `${date(s.canceled_at)} に解約されました`;
    default:
      return "";
  }
}

export function titleOf(s: Subscription) {
  return s.display ? { tenant: s.display.tenant_name, name: `${s.display.product_name}・${s.display.plan_name}` } : { tenant: "—", name: "契約" };
}
