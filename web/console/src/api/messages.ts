// エラーコード → 画面の文言（FR-016）。項目のエラーは fieldMessage。
import type { ApiError } from "./client.ts";

const byCode: Record<string, string> = {
  network_error: "通信できません。接続を確認してください。",
  forbidden: "この操作を行う権限がありません。",
  not_found: "見つかりませんでした。削除されたか、アクセス権がない可能性があります。",
  conflict: "他のスタッフがこの内容を更新しました。最新の内容を読み込み直してください。",
  tenant_not_active: "事業者が有効でないため、この操作はできません（審査中は公開できず、停止中は変更できません）。",
  last_owner_required: "オーナーが最低 1 名必要です。",
  staff_already_member: "このメールアドレスのスタッフはすでに所属しています。",
  staff_not_invited: "招待中のスタッフではありません。",
  identity_provider_unavailable: "認証基盤に接続できません。時間をおいて再度お試しください。",
  webhook_endpoint_limit_exceeded: "送信先は 10 個までです。",
  webhook_delivery_in_progress: "自動の再試行を待っています。",
  webhook_endpoint_unavailable: "送信先が無効か削除されています。",
  coupon_code_taken: "このコードはすでに使われています。",
  api_key_limit_exceeded: "有効な API キーの上限に達しています。",
  price_unchanged: "現在と同じ金額です。",
};

export function errorMessage(e: unknown): string {
  const err = e as ApiError;
  if (err?.code && byCode[err.code]) return byCode[err.code]!;
  if (err?.status === 400) return "入力内容を確認してください。";
  return "処理に失敗しました。時間をおいて再度お試しください。";
}

const fieldCodes: Record<string, string> = {
  required: "入力してください",
  too_long: "長すぎます",
  invalid: "形式が正しくありません",
  out_of_range: "範囲外の値です",
  url_invalid: "URL の形式が正しくありません",
  url_not_https: "https の公開 URL を指定してください",
  url_forbidden_host: "https の公開 URL を指定してください",
  event_types_empty: "1 つ以上選んでください",
  event_type_unknown: "選べないイベントが含まれています",
  not_increasing: "前の回より後の日数にしてください",
  in_the_past: "未来の日時を指定してください",
};

export function fieldMessage(code: string): string {
  return fieldCodes[code] ?? "入力内容を確認してください";
}

/** API の項目のエラーを Mantine の form.setErrors の形にする。 */
export function fieldErrors(e: unknown): Record<string, string> {
  const out: Record<string, string> = {};
  for (const f of (e as ApiError)?.fields ?? []) out[f.field] = fieldMessage(f.code);
  return out;
}
