// operator-console FR-001（ブラウザ・MiniStack の Cognito の運営者のプール）：仮パスワード → 新しいパスワード、operators でないアカウントの拒否、
// ログアウトと未ログインの保護、無操作の自動ログアウト。
import { test, expect } from "../../fixtures/index.ts";
import { strongPassword } from "../../helpers/console.ts";
import { OPS, createOperator, login, signInAsOperator } from "../../helpers/ops.ts";

test("FR-001: 管理者が作った運営者は、仮パスワードでログインして新しいパスワードを設定するとホームが開く", async ({ page }) => {
  const op = await signInAsOperator(page, "login");
  await expect(page.getByTestId("user-menu")).toContainText(op.email);
  await expect(page.getByTestId("home-pending")).toBeVisible();

  // ログアウトすると、保護された画面はログイン画面に戻る
  await page.getByTestId("user-menu").click();
  await page.getByRole("menuitem", { name: "ログアウト" }).click();
  await expect(page).toHaveURL(/\/ops\/login/);
  await page.goto(`${OPS}/audit`);
  await expect(page).toHaveURL(/\/ops\/login/);

  // 設定したパスワードでもう一度ログインできる
  await login(page, op.email, op.password);
  await expect(page.getByRole("heading", { name: "ホーム" })).toBeVisible();
});

test("FR-001: グループ operators に属さないアカウントは「運営者の権限がありません」になり、ops の API も 403", async ({ page, api }) => {
  const acc = await createOperator("nogroup", { operators: false });
  await login(page, acc.email, acc.temp, strongPassword());
  await expect(page.getByTestId("not-operator")).toContainText("運営者の権限がありません");
  await expect(page.getByRole("heading", { name: "ホーム" })).toHaveCount(0);

  const token = await page.evaluate(() => Object.entries(localStorage).find(([k]) => k.startsWith("CognitoIdentityServiceProvider.") && k.endsWith(".idToken"))?.[1]);
  const res = await api.call("GET", "/v1/ops/tenants", { token });
  expect(res.status).toBe(403);
});

test("FR-001: 30 分操作しないと、次に開いたときにログイン画面に移り理由が表示される", async ({ page }) => {
  await signInAsOperator(page, "idle");
  // 最後の操作の時刻を 31 分前にする（タブ間で共有している localStorage の値）
  await page.evaluate(() => localStorage.setItem("sp.ops.lastActivity", String(Date.now() - 31 * 60 * 1000)));
  await page.goto(`${OPS}/audit`);
  await expect(page).toHaveURL(/\/ops\/login\?reason=idle/);
  await expect(page.getByText("一定時間操作がなかったためログアウトしました")).toBeVisible();
});
