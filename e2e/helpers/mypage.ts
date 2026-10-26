// 契約者のマイページのブラウザのテスト（consumer-mypage 設計 §9）。契約者は MiniStack の Cognito（契約者のプール）に画面から登録し、
// 確認コードは MiniStack の SES に届いたメールから読む。API の準備には、ブラウザに保存された ID トークンを使う。
import { randomUUID } from "node:crypto";
import { expect, type Page } from "@playwright/test";
import type { Api } from "../fixtures/api.ts";
import type { Mail } from "../fixtures/mail.ts";
import type { Wait } from "../fixtures/wait.ts";

export const WEB = process.env.CONSOLE_BASE_URL ?? "http://localhost:8090";
export const MYPAGE = `${WEB}/mypage`;

export interface Consumer {
  email: string;
  password: string;
  name: string;
}

export function newConsumer(tag: string): Consumer {
  const id = randomUUID().slice(0, 8);
  return { email: `mypage-${tag}-${id}@example.com`.toLowerCase(), password: `pass${id}9`, name: `契約者 ${tag}` };
}

/** 宛先 email の最新の確認コード（after 通目より後のメール）。 */
export async function latestCode(mail: Mail, wait: Wait, email: string, after = 0): Promise<string> {
  return wait.until(
    async () => {
      const ms = (await mail.sesMessages()).filter((m) => m.To.includes(email));
      const m = ms.length > after ? /\b(\d{6})\b/.exec(ms[ms.length - 1]!.BodyText) : null;
      return m?.[1];
    },
    { message: `${email} への確認コード` },
  );
}

export async function countMails(mail: Mail, email: string): Promise<number> {
  return (await mail.sesMessages()).filter((m) => m.To.includes(email)).length;
}

/** 登録の画面からアカウントを作り、確認コードで登録を完了する（ログインした状態になる）。 */
export async function signUp(page: Page, mail: Mail, wait: Wait, c: Consumer, from = `${MYPAGE}/signup`) {
  if (!page.url().includes("/mypage/signup")) await page.goto(from);
  await expect(page.getByRole("heading", { name: "アカウントを作る" })).toBeVisible();
  const before = await countMails(mail, c.email);
  await page.getByLabel("氏名").fill(c.name);
  await page.getByLabel("メールアドレス").fill(c.email);
  await page.getByRole("textbox", { name: "パスワード" }).fill(c.password);
  await page.getByRole("button", { name: "確認コードを送る" }).click();
  await expect(page.getByRole("heading", { name: "確認コードの入力" })).toBeVisible();
  await page.getByLabel("確認コード").fill(await latestCode(mail, wait, c.email, before));
  await page.getByRole("button", { name: "登録を完了する" }).click();
}

export async function login(page: Page, c: Pick<Consumer, "email" | "password">, url = `${MYPAGE}/login`) {
  await page.goto(url);
  await page.getByLabel("メールアドレス").fill(c.email);
  await page.getByRole("textbox", { name: "パスワード" }).fill(c.password);
  await page.getByRole("button", { name: "ログイン" }).click();
}

/** ブラウザに保存された契約者の ID トークン（Amplify の localStorage）。 */
export async function idToken(page: Page): Promise<string> {
  const t = await page.evaluate(() => Object.entries(localStorage).find(([k]) => k.startsWith("CognitoIdentityServiceProvider.") && k.endsWith(".idToken"))?.[1]);
  if (!t) throw new Error("ID トークンがブラウザにない");
  return t;
}

/** 契約者の ID トークンで、カードを登録して申し込む（画面のテストの準備）。 */
export async function subscribeAs(api: Api, token: string, tenantId: string, planId: string, scenario = "ok") {
  const pm = await api.call<{ id: string }>("POST", "/v1/me/payment-methods", {
    token, idempotencyKey: randomUUID(), body: { method_type: "card", provider: "stub", token: `tok_stub_${scenario}_4242_1299_${randomUUID().replaceAll("-", "").slice(0, 8)}` },
  });
  expect(pm.status, JSON.stringify(pm.body)).toBe(201);
  const res = await api.call<{ subscription: { id: string; status: string } }>("POST", "/v1/me/subscriptions", {
    token, idempotencyKey: randomUUID(), body: { tenant_id: tenantId, plan_id: planId, payment_method_id: pm.body.id },
  });
  expect(res.status, JSON.stringify(res.body)).toBe(201);
  return { pmId: pm.body.id, subscription: res.body.subscription };
}

/** ページが横にはみ出していない。 */
export async function expectNoOverflow(page: Page, width = 375) {
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width);
}
