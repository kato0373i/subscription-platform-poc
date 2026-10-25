// 初回ログインの新しいパスワードの設定（FR-001）。
import { Alert, Button, PasswordInput, Stack } from "@mantine/core";
import { useForm } from "@mantine/form";
import { useState } from "react";
import { Navigate, useNavigate } from "react-router";
import { authErrorMessage, useAuth } from "../../auth/AuthProvider.tsx";
import { AuthCard, PASSWORD_HINT, passwordProblem } from "./AuthCard.tsx";

export function NewPasswordPage() {
  const auth = useAuth();
  const navigate = useNavigate();
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const form = useForm({
    initialValues: { password: "", confirm: "" },
    validate: { password: (v) => passwordProblem(v), confirm: (v, all) => (v === all.password ? null : "パスワードが一致しません") },
  });
  if (auth.status === "signedIn") return <Navigate to="/tenants" replace />;
  if (!auth.email) return <Navigate to="/login" replace />;
  return (
    <AuthCard title="新しいパスワードの設定" description="仮パスワードでログインしました。続けるには新しいパスワードを設定してください。">
      <form
        onSubmit={form.onSubmit(async (v) => {
          setBusy(true);
          setError(null);
          try {
            await auth.completeNewPassword(v.password);
            navigate("/tenants", { replace: true });
          } catch (e) {
            setError(authErrorMessage(e));
          } finally {
            setBusy(false);
          }
        })}
      >
        <Stack>
          {error && <Alert color="red" variant="light" role="alert">{error}</Alert>}
          <PasswordInput label="新しいパスワード" description={PASSWORD_HINT} autoComplete="new-password" required {...form.getInputProps("password")} />
          <PasswordInput label="新しいパスワード（確認）" autoComplete="new-password" required {...form.getInputProps("confirm")} />
          <Button type="submit" loading={busy} fullWidth>
            設定してはじめる
          </Button>
        </Stack>
      </form>
    </AuthCard>
  );
}
