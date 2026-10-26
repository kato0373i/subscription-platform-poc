// 登録の確認コードの入力（FR-001）。確定したら自動でログインし、元の画面（申込ページなど）へ。
import { Alert, Anchor, Button, Stack, TextInput } from "@mantine/core";
import { useForm } from "@mantine/form";
import { notifications } from "@mantine/notifications";
import { useState } from "react";
import { Link, useSearchParams } from "react-router";
import { authErrorMessage, useAuth } from "../../auth/AuthProvider.tsx";
import { AuthCard } from "./AuthCard.tsx";
import { useFinishLogin } from "./finish.ts";

export function ConfirmSignupPage() {
  const auth = useAuth();
  const [params] = useSearchParams();
  const done = useFinishLogin();
  const email = params.get("email") ?? "";
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const form = useForm({ initialValues: { code: "" }, validate: { code: (v) => (/^\d{6}$/.test(v.trim()) ? null : "6 桁の数字を入力してください") } });
  return (
    <AuthCard title="確認コードの入力" description={`${email} に届いた 6 桁の確認コードを入力してください。`}>
      <form
        onSubmit={form.onSubmit(async (v) => {
          setBusy(true);
          setError(null);
          try {
            await auth.confirmRegistration(email, v.code);
            await done.finish();
          } catch (e) {
            const name = (e as { name?: string })?.name;
            // 自動ログインができない場合（別のブラウザで確認したなど）はログインの画面へ
            if (name === "AutoSignInException") window.location.assign(`/mypage/login?email=${encodeURIComponent(email)}${done.query ? `&${done.query.slice(1)}` : ""}`);
            else setError(authErrorMessage(e));
          } finally {
            setBusy(false);
          }
        })}
      >
        <Stack>
          {error && <Alert color="red" variant="light" role="alert">{error}</Alert>}
          <TextInput label="確認コード" inputMode="numeric" autoComplete="one-time-code" maxLength={6} required {...form.getInputProps("code")} />
          <Button type="submit" loading={busy} fullWidth>
            登録を完了する
          </Button>
          <Anchor
            component="button"
            type="button"
            size="sm"
            ta="center"
            onClick={async () => {
              try {
                await auth.resendCode(email);
                notifications.show({ color: "green", message: "確認コードを再送しました" });
              } catch (e) {
                setError(authErrorMessage(e));
              }
            }}
          >
            確認コードを再送する
          </Anchor>
          <Anchor component={Link} to={`/login${done.query}`} size="sm" ta="center">
            ログインに戻る
          </Anchor>
        </Stack>
      </form>
    </AuthCard>
  );
}
