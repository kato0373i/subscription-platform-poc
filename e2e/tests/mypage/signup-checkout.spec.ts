// consumer-mypage FR-001・FR-004・FR-011（ブラウザ・MiniStack の Cognito）：申込ページからアカウントを作って申し込み、マイページで契約を確かめる。
import { test, expect } from "../../fixtures/index.ts";
import { createPlan } from "../../helpers/catalog.ts";
import { fillCardAndSubmit } from "../../helpers/checkout.ts";
import { MYPAGE, WEB, expectNoOverflow, login, newConsumer, signUp } from "../../helpers/mypage.ts";

test("FR-011・FR-001・FR-004: 申込ページからアカウントを作って申し込むと、マイページに事業者名・プラン名・金額つきで契約が出る", async ({ page, api, world, mail, wait }) => {
  const tenant = await world.createTenant();
  const plan = await createPlan(api, tenant, { amount: 980 });
  const c = newConsumer("signup");

  await page.goto(`${WEB}/checkout?plan_id=${plan.id}`);
  await expect(page.locator("#plan-price")).toContainText("980円");
  await page.getByRole("link", { name: "アカウントを作って申し込む" }).click();
  await signUp(page, mail, wait, c);
  // 申込ページに戻り、そのまま申し込める
  await expect(page).toHaveURL(new RegExp(`/checkout\\?plan_id=${plan.id}$`));
  await fillCardAndSubmit(page, "4242424242424242");
  await expect(page.locator("#result")).toHaveText("お申し込みが完了しました。");

  await page.goto(`${MYPAGE}/`);
  const card = page.getByTestId("subscription-card");
  await expect(card).toHaveCount(1);
  await expect(card).toContainText(tenant.name);
  await expect(card).toContainText("商品・月額プラン");
  await expect(card).toContainText("980円／月");
  await expect(card).toContainText("利用中");
});

test("FR-001: 登録済みのメールアドレスは案内を出し、方針に合わないパスワードは登録できない。誤ったパスワードではログインできない", async ({ page, mail, wait }) => {
  const c = newConsumer("dup");
  await signUp(page, mail, wait, c);
  await expect(page.getByRole("heading", { name: "契約" })).toBeVisible();
  await page.getByRole("link", { name: "アカウント" }).click();
  await page.getByRole("button", { name: "ログアウト" }).click();
  await expect(page).toHaveURL(/\/mypage\/login\?notice=logout/);

  await page.goto(`${MYPAGE}/signup`);
  await page.getByLabel("氏名").fill("二重");
  await page.getByLabel("メールアドレス").fill(c.email);
  await page.getByRole("textbox", { name: "パスワード" }).fill("abcdefgh");
  await page.getByRole("button", { name: "確認コードを送る" }).click();
  await expect(page.getByText("数字を含めてください", { exact: true })).toBeVisible(); // 方針（画面での確認）
  await page.getByRole("textbox", { name: "パスワード" }).fill("abcd1234");
  await page.getByRole("button", { name: "確認コードを送る" }).click();
  await expect(page.getByRole("alert")).toContainText("このメールアドレスは登録済みです");

  await login(page, { email: c.email, password: "wrongpass1" });
  await expect(page.getByRole("alert")).toContainText("メールアドレスまたはパスワードが正しくありません");
  await login(page, c);
  await expect(page).toHaveURL(new RegExp(`${MYPAGE}/?$`));
  await expect(page.getByText("契約はまだありません")).toBeVisible();
});

test("FR-002・FR-003: パスワードを再設定してログインすると、開こうとした画面に戻る", async ({ page, mail, wait }) => {
  const c = newConsumer("reset");
  await signUp(page, mail, wait, c);
  await page.getByRole("link", { name: "アカウント" }).click();
  await page.getByRole("button", { name: "ログアウト" }).click();
  await expect(page).toHaveURL(/\/mypage\/login\?notice=logout/);

  await page.goto(`${MYPAGE}/payment-methods`);
  await expect(page).toHaveURL(/\/mypage\/login\?return=/);
  await page.getByRole("link", { name: "パスワードをお忘れの場合" }).click();
  await expect(page.getByRole("heading", { name: "パスワードの再設定" })).toBeVisible();
  const before = (await mail.sesMessages()).filter((m) => m.To.includes(c.email)).length;
  await page.getByLabel("メールアドレス").fill(c.email);
  await page.getByRole("button", { name: "確認コードを送る" }).click();
  const { latestCode } = await import("../../helpers/mypage.ts");
  await page.getByLabel("確認コード").fill(await latestCode(mail, wait, c.email, before));
  const next = { ...c, password: `${c.password}x1` };
  await page.getByRole("textbox", { name: "新しいパスワード" }).fill(next.password);
  await page.getByRole("button", { name: "パスワードを再設定する" }).click();
  await expect(page.getByText("パスワードを再設定しました")).toBeVisible();
  await page.getByRole("textbox", { name: "パスワード" }).fill(next.password);
  await page.getByRole("button", { name: "ログイン" }).click();
  await expect(page).toHaveURL(`${MYPAGE}/payment-methods`);
  await expectNoOverflow(page, 1280);
});
