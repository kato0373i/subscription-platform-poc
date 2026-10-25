// E2E-004 月末開始の更新（orchestration.md §11b.3）：FR-019、D-11（BillingPeriod の月末の補正）。
import { test, expect } from "../../fixtures/index.ts";
import { createPlan } from "../../helpers/catalog.ts";
import { JOB, expectInvariants, jst, meSubscription, subscribe } from "../../helpers/orchestration.ts";

function lastDayOfFeb(y: number): string {
  const leap = (y % 4 === 0 && y % 100 !== 0) || y % 400 === 0;
  return `${y}-02-${leap ? 29 : 28}`;
}

test("E2E-004: 1/31 に始めた契約の更新日は 2 月末 → 3/31 → 4/30 → 5/31", async ({ api, world, db, wait, clock }) => {
  const { today } = await clock.now();
  let y = Number(today.slice(0, 4));
  if (today >= `${y}-01-31`) y++;
  await clock.setTo(jst(`${y}-01-31`, "09:00"));
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const { consumer, subscription: sub } = await subscribe(api, world, tenant, plan.id);
  const feb = lastDayOfFeb(y);
  expect(sub.next_renewal_date).toBe(feb);
  const expected: [string, string][] = [
    [feb, `${y}-03-31`],
    [`${y}-03-31`, `${y}-04-30`],
    [`${y}-04-30`, `${y}-05-31`],
  ];
  for (const [date, next] of expected) {
    await clock.setTo(jst(date, "00:30"));
    await clock.runJob(JOB.dispatch, { asOf: date, force: true });
    await wait.until(async () => (await meSubscription(api, consumer, sub.id)).next_renewal_date === next, { message: `${date} の更新で次回 ${next}` });
  }
  const invoices = await db.query<{ period_start: string; status: string }>(
    "SELECT to_char(period_start, 'YYYY-MM-DD') AS period_start, status FROM billing.invoices WHERE subscription_id = $1 ORDER BY period_start",
    [sub.id],
  );
  expect(invoices).toEqual([`${y}-01-31`, feb, `${y}-03-31`, `${y}-04-30`].map((d) => ({ period_start: d, status: "paid" })));
  await expectInvariants(db, wait, tenant.id);
});
