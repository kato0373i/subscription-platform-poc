// エラーコード → 画面の文言（operator-console FR-012）。項目のエラーは fieldMessage。
import type { ApiError } from "./client.ts";

const byCode: Record<string, string> = {
  network_error: "通信できません。接続を確認してください。",
  forbidden: "運営者の権限がありません。",
  not_found: "見つかりませんでした。",
  conflict: "ほかの操作と重なりました。画面を読み直してください。",
  tenant_state_conflict: "事業者の状態がこの操作を許しません（既に承認・停止されている可能性があります）。",
  fincode_shop_id_taken: "このショップ ID は別の事業者で使われています。",
  staff_already_member: "このメールアドレスのスタッフはすでに所属しています。",
  identity_provider_unavailable: "認証基盤に接続できません。時間をおいて再度お試しください。",
  attempt_already_finalized: "この決済は既に確定しています。",
  attempt_not_resolvable: "この決済はまだ確定できません（依頼からの時間が短いか、確認中です）。",
  outcome_mismatch: "決済代行の結果と食い違います。決済代行の管理画面を確認してください。",
  payable_already_paid: "この請求は既に支払われています。",
  invoice_not_collectible: "この請求は再決済できません。",
  collection_in_progress: "この請求は決済の処理中です。",
  billing_run_in_progress: "この課金日の更新処理は実行中です。",
  webhook_delivery_in_progress: "自動の再試行を待っています。",
  webhook_endpoint_unavailable: "送信先が無効か削除されています。",
  validation_failed: "入力内容を確認してください。",
};

export function errorMessage(e: unknown): string {
  const err = e as ApiError;
  if (err?.code && byCode[err.code]) return byCode[err.code]!;
  if (err?.status === 400) return "入力内容を確認してください。";
  return "処理に失敗しました。時間をおいて再度お試しください。";
}

export function fieldErrors(e: unknown): Record<string, string> {
  const out: Record<string, string> = {};
  for (const f of (e as ApiError)?.fields ?? []) out[f.field] = f.code === "required" ? "入力してください" : "形式が正しくありません";
  return out;
}
