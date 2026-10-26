// パスワードの再設定（FR-002）：メールの確認コード → 新しいパスワード。アカウントの有無は明かさない。
import { Alert, Anchor, Button, PasswordInput, Stack, TextInput } from "@mantine/core";
import { useForm } from "@mantine/form";
import { useState } from "react";
import { Link, useNavigate } from "react-router";
import { authErrorMessage, useAuth } from "../../auth/AuthProvider.tsx";
import { AuthCard, PASSWORD_HINT, passwordProblem } from "./AuthCard.tsx";

export function ForgotPasswordPage() {
  const auth = useAuth();
  const navigate = useNavigate();
  const [sentTo, setSentTo] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const request = useForm({ initialValues: { email: "" } });
  const confirm = useForm({
    initialValues: { code: "", password: "" },
    validate: { code: (v) => (v.trim() ? null : "確認コードを入力してください"), password: (v) => passwordProblem(v) },
  });
  const send = async (email: string) => {
    setBusy(true);
    setError(null);
    try {
      await auth.requestReset(email);
    } catch (e) {
      // 登録されていないメールアドレスでも同じ表示にする（アカウントの有無を明かさない）
      if ((e as { name?: string }).name === "LimitExceededException") {
        setError(authErrorMessage(e));
        setBusy(false);
        return;
      }
    }
    setSentTo(email);
    setBusy(false);
  };
  if (!sentTo) {
    return (
      <AuthCard title="パスワードの再設定" description="登録しているメールアドレスに確認コードを送ります。">
        <form onSubmit={request.onSubmit((v) => send(v.email.trim()))}>
          <Stack>
            {error && <Alert color="red" variant="light" role="alert">{error}</Alert>}
            <TextInput label="メールアドレス" type="email" autoComplete="username" required {...request.getInputProps("email")} />
            <Button type="submit" loading={busy} fullWidth>
              確認コードを送る
            </Button>
            <Anchor component={Link} to="/login" size="sm" ta="center">
              ログインに戻る
            </Anchor>
          </Stack>
        </form>
      </AuthCard>
    );
  }
  return (
    <AuthCard title="パスワードの再設定" description="確認コードを送信しました（登録されている場合）。メールに記載のコードと新しいパスワードを入力してください。">
      <form
        onSubmit={confirm.onSubmit(async (v) => {
          setBusy(true);
          setError(null);
          try {
            await auth.confirmReset(sentTo, v.code, v.password);
            navigate("/login?reason=reset", { replace: true });
          } catch (e) {
            setError(authErrorMessage(e));
          } finally {
            setBusy(false);
          }
        })}
      >
        <Stack>
          {error && <Alert color="red" variant="light" role="alert">{error}</Alert>}
          <TextInput label="確認コード" autoComplete="one-time-code" required {...confirm.getInputProps("code")} />
          <PasswordInput label="新しいパスワード" description={PASSWORD_HINT} autoComplete="new-password" required {...confirm.getInputProps("password")} />
          <Button type="submit" loading={busy} fullWidth>
            パスワードを再設定する
          </Button>
          <Anchor component="button" type="button" size="sm" ta="center" onClick={() => void send(sentTo)}>
            確認コードを再送する
          </Anchor>
        </Stack>
      </form>
    </AuthCard>
  );
}
