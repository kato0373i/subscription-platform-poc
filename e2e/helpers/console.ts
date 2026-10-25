// 事業者コンソールのブラウザのテスト（business-console 設計 §5.7・NFR-010）。
// スタッフは MiniStack の Cognito に本物のアカウントを作る：開発用 JWT の owner が console の API で招待し、
// MiniStack の SES に届いた招待メールから仮パスワードを読む。
import { randomUUID } from "node:crypto";
import { expect, type Page } from "@playwright/test";
import type { Api } from "../fixtures/api.ts";
import type { Mail } from "../fixtures/mail.ts";
import type { Wait } from "../fixtures/wait.ts";
import type { TenantWorld } from "../fixtures/world.ts";

export const CONSOLE = process.env.CONSOLE_BASE_URL ?? "http://localhost:8090";

export interface CognitoStaff {
  email: string;
  password: string;
  staffId: string;
}

/** Cognito のパスワードの方針（12 文字以上・大小英字・数字・記号）を満たす値。 */
export function strongPassword(): string {
  return `Sp-${randomUUID().slice(0, 8)}Aa1!`;
}

export function uniqueEmail(tag: string): string {
  return `console-${tag}-${randomUUID().slice(0, 8)}@example.com`.toLowerCase();
}

/** SES に届いた、宛先 email の最新のメール本文（subject で絞る）。 */
export async function latestMail(mail: Mail, wait: Wait, email: string, subject: string, after = 0): Promise<string> {
  return wait.until(
    async () => {
      const ms = (await mail.sesMessages()).filter((m) => m.To.includes(email) && m.Subject.includes(subject));
      return ms.length > after ? ms[ms.length - 1]!.BodyText : undefined;
    },
    { message: `${email} への「${subject}」のメール` },
  );
}

export function tempPasswordOf(body: string): string {
  const m = /仮パスワード[：:]\s*(\S+)/.exec(body);
  if (!m) throw new Error(`招待メールに仮パスワードがない: ${body}`);
  return m[1]!;
}

/** パスワード再設定の確認コード（宛先 email の、招待以外のメールで 6 桁の数字を含む最新のもの）。 */
export async function latestResetCode(mail: Mail, wait: Wait, email: string): Promise<string> {
  return wait.until(
    async () => {
      const ms = (await mail.sesMessages()).filter((m) => m.To.includes(email) && !m.Subject.includes("招待"));
      const m = ms.length ? /\b(\d{6})\b/.exec(ms[ms.length - 1]!.BodyText) : null;
      return m?.[1];
    },
    { message: `${email} への確認コードのメール` },
  );
}

/** console の API でスタッフを招待し（Cognito にアカウントができる）、招待メールの仮パスワードを返す。 */
export async function inviteCognitoStaff(api: Api, mail: Mail, wait: Wait, tenant: TenantWorld, role: "owner" | "admin" | "viewer", tag: string) {
  const email = uniqueEmail(tag);
  const res = await api.call<{ id: string; status: string }>("POST", "/v1/console/staff", {
    token: tenant.ownerToken,
    tenantId: tenant.id,
    idempotencyKey: randomUUID(),
    body: { email, display_name: `${role} ${tag}`, role },
  });
  expect(res.status, JSON.stringify(res.body)).toBe(201);
  expect(res.body.status).toBe("invited");
  const temp = tempPasswordOf(await latestMail(mail, wait, email, "招待"));
  return { email, temp, staffId: res.body.id };
}

/** ログイン画面でログインする（仮パスワードなら新しいパスワードも設定する）。 */
export async function login(page: Page, email: string, password: string, newPassword?: string) {
  await page.goto(`${CONSOLE}/console/login`);
  await page.getByLabel("メールアドレス").fill(email);
  await page.getByRole("textbox", { name: "パスワード", exact: true }).fill(password);
  await page.getByRole("button", { name: "ログイン" }).click();
  if (newPassword) {
    await expect(page).toHaveURL(/\/console\/login\/new-password$/);
    await page.getByRole("textbox", { name: "新しいパスワード", exact: true }).fill(newPassword);
    await page.getByRole("textbox", { name: "新しいパスワード（確認）" }).fill(newPassword);
    await page.getByRole("button", { name: "設定してはじめる" }).click();
  }
}

/** 招待 → 初回ログイン（新しいパスワード）まで済ませ、事業者のダッシュボード（最初の画面）にいる状態にする。 */
export async function signInAsNewStaff(page: Page, api: Api, mail: Mail, wait: Wait, tenant: TenantWorld, role: "owner" | "admin" | "viewer", tag: string): Promise<CognitoStaff> {
  const inv = await inviteCognitoStaff(api, mail, wait, tenant, role, tag);
  const password = strongPassword();
  await login(page, inv.email, inv.temp, password);
  await expect(page).toHaveURL(new RegExp(`/console/t/${tenant.id}/dashboard$`));
  return { email: inv.email, password, staffId: inv.staffId };
}
