// operator-console のブラウザのテスト（時刻を進めないもの）：FR-002（ホーム）・FR-003（登録と招待）・FR-004（承認）・FR-008（更新処理の再実行）・
// FR-011（監査ログ）・FR-012（エラーの表示）。
import { randomUUID } from "node:crypto";
import { test, expect } from "../../fixtures/index.ts";
import { latestMail } from "../../helpers/console.ts";
import { addDays } from "../../helpers/orchestration.ts";
import { OPS, signInAsOperator } from "../../helpers/ops.ts";

test("FR-003・FR-004・FR-011: 事業者を登録してオーナーを招待し、ショップ ID で承認する。承認は監査ログで探せる", async ({ page, mail, wait }) => {
  const op = await signInAsOperator(page, "tenant");
  const name = `ミライ書店 ${randomUUID().slice(0, 6)}`;
  const owner = `owner-${randomUUID().slice(0, 8)}@example.com`;

  await page.getByRole("link", { name: "事業者", exact: true }).click();
  await page.getByRole("button", { name: "事業者を登録" }).click();
  await page.getByLabel("事業者名").fill(name);
  await page.getByRole("button", { name: "登録する" }).click();
  await expect(page.getByText(`${name} を登録しました（審査待ち）`)).toBeVisible();

  // 続けて最初のオーナーを招待する（招待メールが届く）
  const invite = page.getByRole("dialog", { name: "オーナーを招待" });
  await invite.getByLabel("メールアドレス").fill(owner);
  await invite.getByLabel("氏名").fill("山田 太郎");
  await invite.getByRole("button", { name: "招待する" }).click();
  await expect(page.getByText("オーナーに招待メールを送りました")).toBeVisible();
  expect(await latestMail(mail, wait, owner, "招待")).toContain("仮パスワード");

  // 審査待ちで絞り込むと出る → 詳細で承認する
  await page.getByTestId("tenant-status-filter").getByText("審査待ち").click();
  await page.getByRole("cell", { name, exact: true }).click();
  await expect(page.getByRole("heading", { name })).toBeVisible();
  await expect(page.getByTestId("tenant-status")).toHaveText("審査待ち");
  const tenantId = page.url().split("/").pop()!;
  const shop = `shop_${randomUUID().replaceAll("-", "").slice(0, 12)}`;
  await page.getByRole("button", { name: "承認する" }).click();
  await page.getByLabel("fincode のショップ ID").fill(shop);
  await page.getByRole("dialog").getByRole("button", { name: "承認する" }).click();
  await expect(page.getByTestId("tenant-status")).toHaveText("有効");
  await expect(page.getByText(shop)).toBeVisible();

  // 監査ログ：事業者と操作で絞ると承認の記録が出て、開くとショップ ID と運営者がわかる
  await page.getByRole("link", { name: "監査ログ" }).click();
  await page.getByLabel("事業者 ID").fill(tenantId);
  await page.getByLabel("操作", { exact: true }).fill("tenant.approve");
  await page.getByRole("button", { name: "検索" }).click();
  await expect(page).toHaveURL(new RegExp(`tenant_id=${tenantId}`));
  const rows = page.getByRole("row").filter({ hasText: "tenant.approve" });
  await expect(rows).toHaveCount(1);
  await rows.click();
  const drawer = page.getByRole("dialog", { name: "監査の記録" });
  await expect(drawer.getByTestId("audit-after")).toContainText(shop);
  await expect(drawer).toContainText(op.sub);
  await expect(drawer).toContainText("POST /v1/ops/tenants/{tenant_id}/approve");
});

test("FR-004・FR-012: 別の事業者で使われているショップ ID では承認できず、理由が表示される", async ({ page, world }) => {
  const used = await world.createTenant(); // 承認済み（shop_<id>）
  await signInAsOperator(page, "dup-shop");
  await page.getByRole("link", { name: "事業者", exact: true }).click();
  await page.getByRole("button", { name: "事業者を登録" }).click();
  await page.getByLabel("事業者名").fill(`重複 ${randomUUID().slice(0, 6)}`);
  await page.getByRole("button", { name: "登録する" }).click();
  await page.getByRole("dialog", { name: "オーナーを招待" }).getByRole("button", { name: "あとで" }).click();
  await page.getByRole("cell", { name: /^重複 / }).first().click();
  await page.getByRole("button", { name: "承認する" }).click();
  await page.getByLabel("fincode のショップ ID").fill(`shop_${used.id.replaceAll("-", "")}`);
  await page.getByRole("dialog").getByRole("button", { name: "承認する" }).click();
  await expect(page.getByText("このショップ ID は別の事業者で使われています。")).toBeVisible();
  await expect(page.getByText(/問い合わせ番号：/)).toBeVisible();
  await expect(page.getByTestId("tenant-status")).toHaveText("審査待ち");
});

test("FR-002: ホームの審査待ちの件数をクリックすると、審査待ちで絞り込んだ事業者の一覧が開く", async ({ page, api, tokens }) => {
  const op = await tokens.operator();
  const name = `審査待ち ${randomUUID().slice(0, 6)}`;
  expect((await api.call("POST", "/v1/ops/tenants", { token: op, body: { name } })).status).toBe(201);
  await signInAsOperator(page, "home");
  await expect(page.getByTestId("home-pending-value")).not.toHaveText("0");
  await expect(page.getByTestId("home-billing")).toBeVisible();
  await page.getByTestId("home-pending").click();
  await expect(page).toHaveURL(/\/ops\/tenants\?status=pending_review/);
  await expect(page.getByRole("cell", { name, exact: true })).toBeVisible();
});

test("FR-008: 過去の課金日を選んで更新処理を再実行すると、手動の再実行の記録が追加される", async ({ page, api }) => {
  const today = ((await api.dev.getClock()).body as { today: string }).today;
  const date = addDays(today, -(4000 + Math.floor(Math.random() * 1000))); // 契約のない、他のテストの課金日と重ならない過去の日
  await signInAsOperator(page, "rerun");
  await page.goto(`${OPS}/billing-runs?date=${date}`);
  await expect(page.getByText("この課金日の更新処理はまだ実行されていません")).toBeVisible();
  await page.getByRole("button", { name: "再実行" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "再実行する" }).click();
  await expect(page.getByText("再実行を受け付けました")).toBeVisible();
  const row = page.getByRole("row").filter({ hasText: "手動の再実行" });
  await expect(row).toHaveCount(1);
  await expect(row).toContainText("投入済み");
  await expect(page.getByTestId("outcome-paid")).toHaveText("0");
});
