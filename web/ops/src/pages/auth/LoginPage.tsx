// ログイン（FR-001）。初回（仮パスワード）は新しいパスワードの設定へ。
import { Alert, Anchor, Button, PasswordInput, Stack, TextInput } from "@mantine/core";
import { useForm } from "@mantine/form";
import { useState } from "react";
import { Link, Navigate, useNavigate, useSearchParams } from "react-router";
import { authErrorMessage, useAuth } from "../../auth/AuthProvider.tsx";
import { AuthCard } from "./AuthCard.tsx";

const reasons: Record<string, string> = {
  idle: "一定時間操作がなかったためログアウトしました。",
  expired: "ログインの有効期限が切れました。もう一度ログインしてください。",
  reset: "パスワードを再設定しました。新しいパスワードでログインしてください。",
};

export function LoginPage() {
  const auth = useAuth();
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const form = useForm({ initialValues: { email: "", password: "" } });
  if (auth.status === "signedIn") return <Navigate to="/" replace />;
  const notice = reasons[params.get("reason") ?? ""];
  return (
    <AuthCard title="ログイン">
      <form
        onSubmit={form.onSubmit(async (v) => {
          setBusy(true);
          setError(null);
          try {
            const next = await auth.login(v.email, v.password);
            navigate(next === "new_password" ? "/login/new-password" : "/", { replace: true });
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
          <Anchor component={Link} to="/forgot-password" size="sm" ta="center">
            パスワードをお忘れの場合
          </Anchor>
        </Stack>
      </form>
    </AuthCard>
  );
}
