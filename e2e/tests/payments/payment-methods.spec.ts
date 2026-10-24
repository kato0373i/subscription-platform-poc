// payments.md §7.2・§7.3（支払方法の登録と一覧）：AC-PAY-001・002・003（一覧の部分）・005・006
import type { MePaymentsComponents } from "@sp/api-client";
import { test, expect } from "../../fixtures/index.ts";

type PaymentMethod = MePaymentsComponents["schemas"]["PaymentMethod"];

const token = (scenario: string, last4 = "4242", exp = "1228") => `tok_stub_${scenario}_${last4}_${exp}_${crypto.randomUUID().replaceAll("-", "").slice(0, 8)}`;

test("AC-PAY-001・002: トークンで登録すると表示用の情報だけが返り、同じ Idempotency-Key の再送は同じ支払方法", async ({ api, world, db }) => {
  const consumer = await world.createConsumer();
  const key = `pm-${crypto.randomUUID()}`;
  const body = { method_type: "card", provider: "stub", token: token("ok") };
  const res = await api.call<PaymentMethod>("POST", "/v1/me/payment-methods", { token: consumer.token, idempotencyKey: key, body });
  expect(res.status, JSON.stringify(res.body)).toBe(201);
  expect(res.body).toMatchObject({ method_type: "card", status: "active", expired: false, display: { brand: "visa", last4: "4242", exp_month: 12, exp_year: 2028 } });

  const again = await api.call<PaymentMethod>("POST", "/v1/me/payment-methods", { token: consumer.token, idempotencyKey: key, body });
  expect(again.status).toBe(201);
  expect(again.body.id).toBe(res.body.id);
  expect(await db.count("SELECT count(*) FROM payments.payment_methods WHERE customer_account_id = $1", [consumer.customerAccountId])).toBe(1);
});

test("AC-PAY-006: カード番号の項目は受け付けない（422 ではなく OpenAPI の検証で 400 validation_failed）", async ({ api, world }) => {
  const consumer = await world.createConsumer();
  const res = await api.call("POST", "/v1/me/payment-methods", {
    token: consumer.token,
    idempotencyKey: `pm-${crypto.randomUUID()}`,
    body: { method_type: "card", provider: "stub", token: token("ok"), card_number: "4242424242424242" },
  });
  expect(res.status).toBe(400);
  expect(res.problem?.code).toBe("validation_failed");
});

test("トークンの拒否・使用済み・有効でない代行・Idempotency-Key なし", async ({ api, world }) => {
  const consumer = await world.createConsumer();
  const post = (body: object, key = `pm-${crypto.randomUUID()}`) => api.call("POST", "/v1/me/payment-methods", { token: consumer.token, idempotencyKey: key, body });
  expect((await post({ method_type: "card", provider: "stub", token: token("reg_reject") })).problem?.code).toBe("invalid_payment_token");
  expect((await post({ method_type: "card", provider: "stub", token: "tok_bad" })).problem?.code).toBe("invalid_payment_token");
  const used = token("ok");
  expect((await post({ method_type: "card", provider: "stub", token: used })).status).toBe(201);
  expect((await post({ method_type: "card", provider: "stub", token: used })).problem?.code).toBe("invalid_payment_token");
  expect((await post({ method_type: "card", provider: "fincode", token: token("ok") })).problem?.code).toBe("unsupported_payment_method");
  const noKey = await api.call("POST", "/v1/me/payment-methods", { token: consumer.token, body: { method_type: "card", provider: "stub", token: token("ok") } });
  expect(noKey.status).toBe(400);
});

test("AC-PAY-005: 有効な支払方法は 10 件まで", async ({ api, world }) => {
  const consumer = await world.createConsumer();
  for (let i = 0; i < 10; i++) await world.createPaymentMethod(consumer);
  const res = await api.call("POST", "/v1/me/payment-methods", {
    token: consumer.token,
    idempotencyKey: `pm-${crypto.randomUUID()}`,
    body: { method_type: "card", provider: "stub", token: token("ok") },
  });
  expect(res.status).toBe(422);
  expect(res.problem?.code).toBe("payment_method_limit_exceeded");
});

test("AC-PAY-003（一覧）: 一覧は自分の支払方法だけを新しい順に返す。有効期限切れは expired", async ({ api, world }) => {
  const x = await world.createConsumer();
  const y = await world.createConsumer();
  const older = await api.call<PaymentMethod>("POST", "/v1/me/payment-methods", { token: x.token, idempotencyKey: `pm-${crypto.randomUUID()}`, body: { method_type: "card", provider: "stub", token: token("ok", "1111", "0120") } });
  const newer = await world.createPaymentMethod(x, "insufficient");
  await world.createPaymentMethod(y);
  const list = await api.call<{ data: PaymentMethod[]; has_more: boolean; next_cursor: string | null }>("GET", "/v1/me/payment-methods", { token: x.token, query: { limit: 1 } });
  expect(list.body.data.map((m) => m.id)).toEqual([newer.id]);
  expect(list.body.has_more).toBe(true);
  const rest = await api.call<{ data: PaymentMethod[] }>("GET", "/v1/me/payment-methods", { token: x.token, query: { starting_after: list.body.next_cursor! } });
  expect(rest.body.data.map((m) => m.id)).toEqual([older.body.id]);
  expect(rest.body.data[0]).toMatchObject({ expired: true, display: { last4: "1111", brand: "visa" } });
  // 決済での他人の支払方法の 404（AC-PAY-003 の申込の部分）は orchestration の実装後
});

test("申込ページの設定は有効な手段（stub）を返す", async ({ api }) => {
  const res = await api.call<{ methods: { method_type: string; provider: string; public_key: string | null }[] }>("GET", "/v1/checkout/payment-config");
  expect(res.status).toBe(200);
  expect(res.body.methods).toEqual([{ method_type: "card", provider: "stub", public_key: null }]);
});
