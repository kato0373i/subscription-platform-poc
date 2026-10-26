// パスワードの再設定（FR-002）：確認コードを送る → コードと新しいパスワード。登録していないメールアドレスでも同じ表示。
import { Alert, Anchor, Button, PasswordInput, Stack, TextInput } from "@mantine/core";
import { useForm } from "@mantine/form";
import { useState } from "react";
import { Link, useNavigate } from "react-router";
import { authErrorMessage, PASSWORD_HINT, passwordProblem, useAuth } from "../../auth/AuthProvider.tsx";
import { AuthCard } from "./AuthCard.tsx";
import { useFinishLogin } from "./finish.ts";

export function ForgotPasswordPage() {
  const auth = useAuth();
  const navigate = useNavigate();
  const done = useFinishLogin();
  const [email, setEmail] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const request = useForm({ initialValues: { email: "" } });
  const confirm = useForm({ initialValues: { code: "", password: "" }, validate: { password: passwordProblem } });
  if (!email) {
    return (
      <AuthCard title="パスワードの再設定" description="登録しているメールアドレスに確認コードを送ります。">
        <form
          onSubmit={request.onSubmit(async (v) => {
            setBusy(true);
            setError(null);
            try {
              await auth.requestReset(v.email);
            } catch (e) {
              const name = (e as { name?: string })?.name;
              if (name !== "UserNotFoundException") {
                setError(authErrorMessage(e));
                setBusy(false);
                return;
              }
            }
            setBusy(false);
            setEmail(v.email.trim());
          })}
        >
          <Stack>
            {error && <Alert color="red" variant="light" role="alert">{error}</Alert>}
            <TextInput label="メールアドレス" type="email" autoComplete="username" required {...request.getInputProps("email")} />
            <Button type="submit" loading={busy} fullWidth>
              確認コードを送る
            </Button>
            <Anchor component={Link} to={`/login${done.query}`} size="sm" ta="center">
              ログインに戻る
            </Anchor>
          </Stack>
        </form>
      </AuthCard>
    );
  }
  return (
    <AuthCard title="新しいパスワード" description={`${email} に確認コードを送りました（登録がある場合）。`}>
      <form
        onSubmit={confirm.onSubmit(async (v) => {
          setBusy(true);
          setError(null);
          try {
            await auth.confirmReset(email, v.code, v.password);
            navigate(`/login?notice=reset&email=${encodeURIComponent(email)}${done.query ? `&${done.query.slice(1)}` : ""}`, { replace: true });
          } catch (e) {
            setError(authErrorMessage(e));
          } finally {
            setBusy(false);
          }
        })}
      >
        <Stack>
          {error && <Alert color="red" variant="light" role="alert">{error}</Alert>}
          <TextInput label="確認コード" inputMode="numeric" autoComplete="one-time-code" required {...confirm.getInputProps("code")} />
          <PasswordInput label="新しいパスワード" description={PASSWORD_HINT} autoComplete="new-password" required {...confirm.getInputProps("password")} />
          <Button type="submit" loading={busy} fullWidth>
            パスワードを再設定する
          </Button>
        </Stack>
      </form>
    </AuthCard>
  );
}
