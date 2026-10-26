// platform.md §6（Idempotency-Key）：AC-PLT-029・030・033。支払方法の登録（payments）で確かめる。
import { test, expect } from "../../fixtures/index.ts";

const body = () => ({ method_type: "card", provider: "stub", token: `tok_stub_ok_4242_1228_${crypto.randomUUID().replaceAll("-", "").slice(0, 8)}` });

test("AC-PLT-029・030: 同じキー・同じ本文の再送は保存済みの応答（Idempotent-Replayed: true）で 1 件だけ、違う本文は 422 idempotency_key_reused", async ({ api, world, db }) => {
  const consumer = await world.createConsumer();
  const key = `plt-${crypto.randomUUID()}`;
  const b = body();
  const first = await api.call<{ id: string }>("POST", "/v1/me/payment-methods", { token: consumer.token, idempotencyKey: key, body: b });
  expect(first.status, JSON.stringify(first.body)).toBe(201);
  expect(first.headers["idempotent-replayed"]).toBeUndefined();

  const again = await api.call<{ id: string }>("POST", "/v1/me/payment-methods", { token: consumer.token, idempotencyKey: key, body: b });
  expect(again.status).toBe(201);
  expect(again.headers["idempotent-replayed"]).toBe("true");
  expect(again.body).toEqual(first.body);
  expect(await db.count("SELECT count(*) FROM payments.payment_methods WHERE customer_account_id = $1", [consumer.customerAccountId])).toBe(1);

  const other = await api.call("POST", "/v1/me/payment-methods", { token: consumer.token, idempotencyKey: key, body: body() });
  expect(other.status).toBe(422);
  expect(other.problem?.code).toBe("idempotency_key_reused");
  expect(await db.count("SELECT count(*) FROM payments.payment_methods WHERE customer_account_id = $1", [consumer.customerAccountId])).toBe(1);
});

test("AC-PLT-033: Idempotency-Key なしは 400 validation_failed（errors[0].field = Idempotency-Key）", async ({ api, world }) => {
  const consumer = await world.createConsumer();
  const res = await api.call("POST", "/v1/me/payment-methods", { token: consumer.token, body: body() });
  expect(res.status).toBe(400);
  expect(res.problem?.code).toBe("validation_failed");
  expect((res.body as { errors: { field: string }[] }).errors[0]!.field).toBe("Idempotency-Key");
});
