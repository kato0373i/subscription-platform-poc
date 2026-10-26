// 退会済みのアカウントでログインしたとき（削除の前に画面を閉じた場合。ADR-M03）：アカウントの削除を案内する。
import { Button, Stack, Text } from "@mantine/core";
import { useState } from "react";
import { useAuth } from "../auth/AuthProvider.tsx";
import { AuthCard } from "./auth/AuthCard.tsx";

export function WithdrawnPage() {
  const auth = useAuth();
  const [busy, setBusy] = useState(false);
  return (
    <AuthCard title="退会済みのアカウントです">
      <Stack>
        <Text size="sm">このアカウントは退会の手続きが済んでいます。ログインの情報を削除すると、同じメールアドレスで新しく登録できます。</Text>
        <Button
          color="red"
          variant="light"
          loading={busy}
          onClick={async () => {
            setBusy(true);
            try {
              await auth.deleteAccount();
            } catch {
              await auth.logout("withdrawn");
            } finally {
              setBusy(false);
            }
          }}
        >
          ログインの情報を削除する
        </Button>
      </Stack>
    </AuthCard>
  );
}
