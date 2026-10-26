// エラーコード → 画面の文言（consumer-mypage FR-014）。項目のエラーは fieldMessage。
import type { ApiError } from "./client.ts";

const byCode: Record<string, string> = {
  network_error: "通信できません。接続を確認してください。",
  not_found: "見つかりませんでした。",
  unauthenticated: "ログインの有効期限が切れました。もう一度ログインしてください。",
  account_withdrawn: "退会済みのアカウントです。",
  email_unverified: "メールアドレスの確認が済んでいません。",
  subscription_terminal: "この契約はすでに終了しています。",
  subscription_state_conflict: "契約の状態が変わりました。画面を読み直してください。",
  subscription_not_modifiable: "契約の状態が変わりました。画面を読み直してください。",
  cancellation_scheduled: "解約を予約している契約は、プランを変更できません。先に解約を取り消してください。",
  cancellation_not_revocable: "この解約は取り消せません。",
  plan_change_not_allowed: "このプランには変更できません。",
  plan_unavailable: "このプランは現在選べません。",
  payment_method_in_use: "このカードは契約のお支払いに使われているため削除できません。先に契約の支払方法を変更してください。",
  payment_method_unavailable: "このカードは使えません。別のカードを選んでください。",
  payment_method_limit_exceeded: "登録できるカードの上限に達しています。使っていないカードを削除してください。",
  invalid_payment_token: "カード情報を確認できませんでした。",
  unsupported_payment_method: "このお支払い方法は使えません。",
  payment_declined: "カードがご利用いただけませんでした。別のカードをお試しください。",
  provider_unavailable: "ただいまお支払いを受け付けられません。時間をおいてお試しください。",
  collection_in_progress: "お支払いの処理中です。しばらくしてから画面を読み直してください。",
  has_open_subscriptions: "有効な契約が残っているため退会できません。先に解約してください。",
  request_in_progress: "前の操作を処理しています。しばらくお待ちください。",
  conflict: "ほかの操作と重なりました。画面を読み直してください。",
};

export function errorMessage(e: unknown): string {
  const err = e as ApiError;
  if (err?.code && byCode[err.code]) return byCode[err.code]!;
  if (err?.status === 400) return "入力内容を確認してください。";
  return "処理に失敗しました。時間をおいて再度お試しください。";
}

const fieldCodes: Record<string, string> = {
  required: "入力してください",
  invalid: "形式が正しくありません",
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
