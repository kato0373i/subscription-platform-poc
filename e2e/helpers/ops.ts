// 運営者コンソールのブラウザのテスト（operator-console 設計 §5.3・§9）。運営者は本番と同じく管理者が作る：
// MiniStack の Cognito の管理 API（AdminCreateUser・AdminAddUserToGroup）で、運営者のプールに仮パスワードのアカウントを作る。
import { randomUUID } from "node:crypto";
import { expect, type Page } from "@playwright/test";
import { strongPassword } from "./console.ts";

export const WEB = process.env.CONSOLE_BASE_URL ?? "http://localhost:8090";
export const OPS = `${WEB}/ops`;
const AWS = process.env.AWS_ENDPOINT_URL ?? "http://localhost:4566";
const POOL = process.env.COGNITO_OPERATOR_USER_POOL_ID ?? "";

export interface Operator {
  email: string;
  password: string;
  sub: string;
}

async function cognito<T = unknown>(action: string, body: object): Promise<T> {
  const r = await fetch(AWS, {
    method: "POST",
    headers: {
      "Content-Type": "application/x-amz-json-1.1",
      "X-Amz-Target": `AWSCognitoIdentityProviderService.${action}`,
      Authorization: "AWS4-HMAC-SHA256 Credential=test/20260101/ap-northeast-1/cognito-idp/aws4_request, SignedHeaders=host, Signature=e2e",
    },
    body: JSON.stringify(body),
  });
  if (!r.ok) throw new Error(`cognito ${action}: ${r.status} ${await r.text()}`);
  const text = await r.text();
  return (text ? JSON.parse(text) : {}) as T;
}

/** 運営者のプールにアカウントを作る（仮パスワード）。operators を false にすると、グループに入れない（権限のないアカウント）。 */
export async function createOperator(tag: string, o: { operators?: boolean } = {}): Promise<{ email: string; temp: string; sub: string }> {
  if (!POOL) throw new Error("COGNITO_OPERATOR_USER_POOL_ID がない（task up を流す）");
  const email = `ops-${tag}-${randomUUID().slice(0, 8)}@example.com`.toLowerCase();
  const temp = strongPassword();
  const created = await cognito<{ User: { Attributes: { Name: string; Value: string }[] } }>("AdminCreateUser", {
    UserPoolId: POOL, Username: email, TemporaryPassword: temp, MessageAction: "SUPPRESS",
    UserAttributes: [{ Name: "email", Value: email }, { Name: "email_verified", Value: "true" }],
  });
  if (o.operators !== false) await cognito("AdminAddUserToGroup", { UserPoolId: POOL, Username: email, GroupName: "operators" });
  return { email, temp, sub: created.User.Attributes.find((a) => a.Name === "sub")!.Value };
}

/** ログイン画面でログインする（仮パスワードなら新しいパスワードも設定する）。 */
export async function login(page: Page, email: string, password: string, newPassword?: string) {
  await page.goto(`${OPS}/login`);
  await page.getByLabel("メールアドレス").fill(email);
  await page.getByRole("textbox", { name: "パスワード", exact: true }).fill(password);
  await page.getByRole("button", { name: "ログイン" }).click();
  if (newPassword) {
    await expect(page).toHaveURL(/\/ops\/login\/new-password$/);
    await page.getByRole("textbox", { name: "新しいパスワード", exact: true }).fill(newPassword);
    await page.getByRole("textbox", { name: "新しいパスワード（確認）" }).fill(newPassword);
    await page.getByRole("button", { name: "設定してはじめる" }).click();
  }
}

/** 運営者を作り、初回ログイン（新しいパスワード）まで済ませて、ホームにいる状態にする。 */
export async function signInAsOperator(page: Page, tag: string): Promise<Operator> {
  const op = await createOperator(tag);
  const password = strongPassword();
  await login(page, op.email, op.temp, password);
  await expect(page.getByRole("heading", { name: "ホーム" })).toBeVisible();
  return { email: op.email, password, sub: op.sub };
}
