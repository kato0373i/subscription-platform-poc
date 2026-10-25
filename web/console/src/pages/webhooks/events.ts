// 事業者に送るイベントの種類（integration.md §4.1。modules/integration/internal/domain/event.go と一致させる）。
export const eventTypes: { value: string; label: string }[] = [
  { value: "subscription.started", label: "契約の開始" },
  { value: "subscription.trial_will_end", label: "トライアル終了の予告" },
  { value: "subscription.renewed", label: "契約の更新" },
  { value: "subscription.past_due", label: "支払い遅延" },
  { value: "subscription.recovered", label: "支払い遅延からの回復" },
  { value: "subscription.plan_change_scheduled", label: "プラン変更の予約" },
  { value: "subscription.plan_change_revoked", label: "プラン変更の取り消し" },
  { value: "subscription.plan_changed", label: "プランの変更" },
  { value: "subscription.cancellation_scheduled", label: "解約の予約" },
  { value: "subscription.cancellation_revoked", label: "解約予約の取り消し" },
  { value: "subscription.canceled", label: "解約" },
  { value: "invoice.paid", label: "請求の支払い" },
  { value: "invoice.payment_failed", label: "請求の支払い失敗" },
];

export const errorKindLabel: Record<string, string> = {
  timeout: "タイムアウト",
  connection: "接続できない",
  tls: "TLS のエラー",
  ssrf_blocked: "許可されない送信先",
  http_status: "HTTP のエラー",
};
