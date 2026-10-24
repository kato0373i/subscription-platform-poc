// identity.md §7.3（GET /v1/me と初回ログイン時のアカウント作成）：AC-IDN-021・023・024・025
import { randomUUID } from "node:crypto";
import type { MeCustomerComponents } from "@sp/api-client";
import { test, expect } from "../../fixtures/index.ts";

type Account = MeCustomerComponents["schemas"]["Account"];

test("AC-IDN-021: 未登録の subject で呼ぶと active のアカウントが作られ、email は小文字になる", async ({ api, tokens, db }) => {
  const local = `Foo-${randomUUID().slice(0, 8)}`;
  const t = await tokens.issue({ pool: "consumer", email: `${local}@Example.com`, name: "山田 太郎" });
  const res = await api.call<Account>("GET", "/v1/me", { token: t.token });
  expect(res.status, JSON.stringify(res.body)).toBe(200);
  expect(res.body).toMatchObject({ email: `${local.toLowerCase()}@example.com`, name: "山田 太郎", status: "active" });
  expect(await db.count("SELECT count(*) FROM customer.customer_accounts WHERE subject = $1", [t.sub])).toBe(1);
  // 2 回目は同じアカウント
  const again = await api.call<Account>("GET", "/v1/me", { token: t.token });
  expect(again.body.id).toBe(res.body.id);
});

test("AC-IDN-023: 別の active アカウントが使っている email では作られない（自動で紐づけない）", async ({ api, tokens, db, world }) => {
  const first = await world.createConsumer();
  const t = await tokens.issue({ pool: "consumer", email: first.email });
  const res = await api.call("GET", "/v1/me", { token: t.token });
  expect(res.status).toBe(409);
  expect(res.problem?.code).toBe("email_already_registered");
  expect(await db.count("SELECT count(*) FROM customer.customer_accounts WHERE subject = $1", [t.sub])).toBe(0);
});

test("AC-IDN-024: email クレームが無い、または email_verified = false なら 403", async ({ api, tokens }) => {
  const noEmail = await tokens.issue({ pool: "consumer", email: "" });
  const r1 = await api.call("GET", "/v1/me", { token: noEmail.token });
  expect(r1.status).toBe(403);
  expect(r1.problem?.code).toBe("email_unverified");
  const unverified = await tokens.issue({ pool: "consumer", emailVerified: false });
  const r2 = await api.call("GET", "/v1/me", { token: unverified.token });
  expect(r2.status).toBe(403);
  expect(r2.problem?.code).toBe("email_unverified");
});

test("AC-IDN-025: email・name を変えたトークンで呼ぶと新しい値になる", async ({ api, tokens, world }) => {
  const c = await world.createConsumer();
  const email = `changed-${randomUUID().slice(0, 8)}@example.com`;
  const t = await tokens.issue({ pool: "consumer", sub: c.sub, email, name: "新しい名前" });
  const res = await api.call<Account>("GET", "/v1/me", { token: t.token });
  expect(res.status).toBe(200);
  expect(res.body).toMatchObject({ id: c.customerAccountId, email, name: "新しい名前" });
});

test("consumer 以外の主体は /v1/me を使えない", async ({ api, world }) => {
  const tenant = await world.createTenant();
  const res = await api.call("GET", "/v1/me", { token: tenant.ownerToken });
  expect(res.status).toBe(403);
  expect((await api.call("GET", "/v1/me")).status).toBe(401);
});
