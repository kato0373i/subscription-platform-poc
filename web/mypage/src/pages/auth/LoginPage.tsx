// ログイン（FR-001）。登録の確認コードが未入力のアカウントは確認の画面へ。
import { Alert, Anchor, Button, Divider, PasswordInput, Stack, Text, TextInput } from "@mantine/core";
import { useForm } from "@mantine/form";
import { useEffect, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { authErrorMessage, useAuth } from "../../auth/AuthProvider.tsx";
import { AuthCard } from "./AuthCard.tsx";
import { useFinishLogin } from "./finish.ts";

const notices: Record<string, string> = {
  reset: "パスワードを再設定しました。新しいパスワードでログインしてください。",
  withdrawn: "退会しました。ご利用ありがとうございました。",
  logout: "ログアウトしました。",
};

export function LoginPage() {
  const auth = useAuth();
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const done = useFinishLogin();
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const form = useForm({ initialValues: { email: params.get("email") ?? "", password: "" } });
  const notice = notices[params.get("notice") ?? ""];
  const fromCheckout = done.ret?.startsWith("/checkout");

  useEffect(() => {
    if (auth.status === "signedIn") void done.finish();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [auth.status]);

  return (
    <AuthCard title="ログイン" description={fromCheckout ? "ログインすると、お申し込みの画面に戻ります。" : undefined}>
      <form
        onSubmit={form.onSubmit(async (v) => {
          setBusy(true);
          setError(null);
          try {
            const r = await auth.login(v.email, v.password);
            if (r === "confirm") {
              await auth.resendCode(v.email).catch(() => undefined);
              navigate(`/signup/confirm?email=${encodeURIComponent(v.email.trim())}${done.query ? `&${done.query.slice(1)}` : ""}`);
            }
          } catch (e) {
            setError(authErrorMessage(e));
          } finally {
            setBusy(false);
          }
        })}
      >
        <Stack>
          {notice && !error && <Alert color="blue" variant="light">{notice}</Alert>}
          {error && <Alert color="red" variant="light" role="alert">{error}</Alert>}
          <TextInput label="メールアドレス" type="email" autoComplete="username" required {...form.getInputProps("email")} />
          <PasswordInput label="パスワード" autoComplete="current-password" required {...form.getInputProps("password")} />
          <Button type="submit" loading={busy} fullWidth>
            ログイン
          </Button>
          <Anchor component={Link} to={`/forgot-password${done.query}`} size="sm" ta="center">
            パスワードをお忘れの場合
          </Anchor>
          <Divider label="はじめての方" labelPosition="center" />
          <Button component={Link} to={`/signup${done.query}`} variant="default" fullWidth>
            アカウントを作る
          </Button>
          <Text size="xs" c="dimmed" ta="center">
            1 つのアカウントで、複数のサービスの契約をまとめて管理できます。
          </Text>
        </Stack>
      </form>
    </AuthCard>
  );
}
