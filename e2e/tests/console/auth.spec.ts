// business-console FR-001〜FR-004（ブラウザ・MiniStack の Cognito）：招待 → 初回ログイン → 新しいパスワード、誤ったパスワード、
// ログアウト、未ログインの保護、パスワードの再設定、複数の事業者の切り替え。
import { randomUUID } from "node:crypto";
import { test, expect } from "../../fixtures/index.ts";
import { CONSOLE, latestResetCode, login, signInAsNewStaff, strongPassword } from "../../helpers/console.ts";

test("FR-001・FR-006: 招待メールの仮パスワードでログインし、新しいパスワードを設定すると、所属する事業者のダッシュボードが開く", async ({ page, api, world, mail, wait }) => {
  const tenant = await world.createTenant();
  const staff = await signInAsNewStaff(page, api, mail, wait, tenant, "admin", "login");
  await expect(page.getByRole("heading", { name: "ダッシュボード" })).toBeVisible();
  await expect(page.getByTestId("user-menu")).toContainText(staff.email);
  await expect(page.getByTestId("tenant-switcher")).toContainText(tenant.name);

  // 初回の認証済みの要求で、招待中のスタッフは有効になる（ADR-C05）
  const list = await api.call<{ data: { id: string; status: string; last_seen_at: string | null }[] }>("GET", "/v1/console/staff", { token: tenant.ownerToken, tenantId: tenant.id });
  expect(list.body.data.find((s) => s.id === staff.staffId)).toMatchObject({ status: "active" });

  // ログアウトすると、保護された画面はログイン画面に戻る
  await page.getByTestId("user-menu").click();
  await page.getByRole("menuitem", { name: "ログアウト" }).click();
  await expect(page).toHaveURL(/\/console\/login/);
  await page.goto(`${CONSOLE}/console/t/${tenant.id}/products`);
  await expect(page).toHaveURL(/\/console\/login/);

  // 設定したパスワードでもう一度ログインできる
  await login(page, staff.email, staff.password);
  await expect(page).toHaveURL(new RegExp(`/console/t/${tenant.id}/dashboard$`));
});

test("FR-001: 誤ったパスワードでは、アカウントの有無がわからない文言でエラーになる", async ({ page }) => {
  await login(page, `nobody-${randomUUID().slice(0, 8)}@example.com`, "Wrong-Password1!");
  await expect(page.getByRole("alert")).toContainText("メールアドレスまたはパスワードが正しくありません");
  await expect(page).toHaveURL(/\/console\/login$/);
});

test("FR-002: パスワードを忘れたら、メールの確認コードで再設定してログインできる", async ({ page, api, world, mail, wait }) => {
  const tenant = await world.createTenant();
  const staff = await signInAsNewStaff(page, api, mail, wait, tenant, "viewer", "reset");
  await page.getByTestId("user-menu").click();
  await page.getByRole("menuitem", { name: "ログアウト" }).click();
  await expect(page).toHaveURL(/\/console\/login/);

  await page.getByRole("link", { name: "パスワードをお忘れの場合" }).click();
  await expect(page.getByRole("heading", { name: "パスワードの再設定" })).toBeVisible();
  await page.getByLabel("メールアドレス").fill(staff.email);
  await page.getByRole("button", { name: "確認コードを送る" }).click();
  const code = await latestResetCode(mail, wait, staff.email);
  const next = strongPassword();
  await page.getByLabel("確認コード").fill(code);
  await page.getByRole("textbox", { name: "新しいパスワード", exact: true }).fill(next);
  await page.getByRole("button", { name: "パスワードを再設定する" }).click();
  await expect(page).toHaveURL(/\/console\/login\?reason=reset/);

  await login(page, staff.email, next);
  await expect(page).toHaveURL(new RegExp(`/console/t/${tenant.id}/dashboard$`));
});

test("FR-004: 複数の事業者に所属していれば、事業者を選んで切り替えられる。所属のない事業者の URL は選択の画面に戻る", async ({ page, api, world, mail, wait }) => {
  const a = await world.createTenant();
  const b = await world.createTenant();
  const staff = await signInAsNewStaff(page, api, mail, wait, a, "admin", "multi");
  // 有効なアカウントを別の事業者に招待すると、すぐに有効なスタッフとして追加される（お知らせのメール）
  const added = await api.call<{ status: string }>("POST", "/v1/console/staff", {
    token: b.ownerToken, tenantId: b.id, idempotencyKey: randomUUID(), body: { email: staff.email, display_name: "複数所属", role: "viewer" },
  });
  expect(added.status, JSON.stringify(added.body)).toBe(201);
  expect(added.body.status).toBe("active");

  await page.goto(`${CONSOLE}/console/tenants?choose`);
  await expect(page.getByText(a.name)).toBeVisible();
  await page.getByText(b.name).click();
  await expect(page).toHaveURL(new RegExp(`/console/t/${b.id}/dashboard$`));
  await page.getByTestId("tenant-switcher").click();
  await page.getByRole("menuitem", { name: new RegExp(a.name) }).click();
  await expect(page).toHaveURL(new RegExp(`/console/t/${a.id}/dashboard$`));

  const other = await world.createTenant();
  await page.goto(`${CONSOLE}/console/t/${other.id}/subscriptions`);
  await expect(page).toHaveURL(/\/console\/tenants\?lost=1/);
});
