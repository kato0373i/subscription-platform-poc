// アカウントの作成（FR-001）：メールアドレス・パスワード・氏名 → 確認コードの画面へ。
import { Alert, Anchor, Button, PasswordInput, Stack, TextInput } from "@mantine/core";
import { useForm } from "@mantine/form";
import { useState } from "react";
import { Link, useNavigate } from "react-router";
import { authErrorMessage, PASSWORD_HINT, passwordProblem, useAuth } from "../../auth/AuthProvider.tsx";
import { AuthCard } from "./AuthCard.tsx";
import { useFinishLogin } from "./finish.ts";

export function SignupPage() {
  const auth = useAuth();
  const navigate = useNavigate();
  const done = useFinishLogin();
  const [error, setError] = useState<{ text: string; exists: boolean } | null>(null);
  const [busy, setBusy] = useState(false);
  const form = useForm({
    initialValues: { name: "", email: "", password: "" },
    validate: {
      name: (v) => (v.trim() ? null : "入力してください"),
      email: (v) => (/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(v.trim()) ? null : "メールアドレスの形式が正しくありません"),
      password: passwordProblem,
    },
  });
  return (
    <AuthCard title="アカウントを作る" description="メールアドレスに届く確認コードで登録を完了します。">
      <form
        onSubmit={form.onSubmit(async (v) => {
          setBusy(true);
          setError(null);
          try {
            await auth.register(v.email, v.password, v.name);
            navigate(`/signup/confirm?email=${encodeURIComponent(v.email.trim())}${done.query ? `&${done.query.slice(1)}` : ""}`);
          } catch (e) {
            setError({ text: authErrorMessage(e), exists: (e as { name?: string })?.name === "UsernameExistsException" });
          } finally {
            setBusy(false);
          }
        })}
      >
        <Stack>
          {error && (
            <Alert color="red" variant="light" role="alert">
              {error.text}
              {error.exists && (
                <Stack gap={2} mt={6}>
                  <Anchor component={Link} to={`/login${done.query}`} size="sm">ログインする</Anchor>
                  <Anchor component={Link} to={`/forgot-password${done.query}`} size="sm">パスワードを再設定する</Anchor>
                </Stack>
              )}
            </Alert>
          )}
          <TextInput label="氏名" autoComplete="name" required {...form.getInputProps("name")} />
          <TextInput label="メールアドレス" type="email" autoComplete="email" required {...form.getInputProps("email")} />
          <PasswordInput label="パスワード" description={PASSWORD_HINT} autoComplete="new-password" required {...form.getInputProps("password")} />
          <Button type="submit" loading={busy} fullWidth>
            確認コードを送る
          </Button>
          <Anchor component={Link} to={`/login${done.query}`} size="sm" ta="center">
            アカウントをお持ちの方はログイン
          </Anchor>
        </Stack>
      </form>
    </AuthCard>
  );
}
