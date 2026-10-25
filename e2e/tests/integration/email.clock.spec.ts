// 支払い遅延のメール（integration.md §5.3・§8.3・§11.3 の A・B）：AC-INT-022、FR-023。
import { test, expect } from "../../fixtures/index.ts";
import { createPlan } from "../../helpers/catalog.ts";
import { JOB, addMonths, jst, setBehavior, startAtNext15, subscribe, untilStatus } from "../../helpers/orchestration.ts";

test("AC-INT-022: 更新課金の失敗で支払い遅延になると payment_failed:{invoice_id} のメールが 1 行だけ sent になり、事業者名・金額・支払方法更新ページのリンクを含む", async ({ api, world, clock, db, wait, mail }) => {
  const D = await startAtNext15(clock);
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 1980 });
  const { consumer, pm, subscription } = await subscribe(api, world, tenant, plan.id);
  await setBehavior(api, pm.id, "decline", 99);
  const renewal = addMonths(D, 1);
  await clock.setTo(jst(renewal, "00:30"));
  await clock.runJob(JOB.dispatch, { asOf: renewal, force: true });
  await untilStatus(api, wait, consumer, subscription.id, "past_due");
  const [inv] = await db.query<{ id: string }>("SELECT id FROM billing.invoices WHERE subscription_id = $1 AND kind = 'renewal'", [subscription.id]);

  const sent = await mail.waitForSent(`payment_failed:${inv!.id}`);
  expect(sent.template_key).toBe("payment_failed");
  expect(sent.provider_message_id).toBeTruthy();
  expect(sent.subject).toContain(tenant.name);
  expect(sent.variables).toMatchObject({ tenant_name: tenant.name, amount: 1980 });
  expect(String(sent.variables.payment_method_update_url)).toContain(subscription.id);
  expect(JSON.stringify(sent.variables)).not.toContain(consumer.email);
  expect(sent.variables).not.toHaveProperty("customer_name");
  expect(await mail.notifications({ dedupeKey: `payment_failed:${inv!.id}` })).toHaveLength(1);

  // A：MiniStack の SES から本文を取り出す
  const msg = (await mail.sesMessages()).find((m) => m.MessageId === sent.provider_message_id);
  expect(msg).toBeTruthy();
  expect(msg!.To).toEqual([consumer.email]);
  for (const want of [tenant.name, "1,980円", subscription.id]) expect(msg!.BodyText).toContain(want);
  // 申込のメール（signup_completed）も送られている
  await mail.waitForSent(`signup_completed:${subscription.id}`);
});
