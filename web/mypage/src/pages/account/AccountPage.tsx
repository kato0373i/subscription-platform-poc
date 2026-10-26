// アカウント（FR-010・FR-013）：登録の情報、パスワードの変更、ログアウト、退会。
// 退会は有効な契約が残っていればできない。確定したら Cognito の自分のアカウントも削除する（ADR-M03）。
import { Alert, Button, Modal, PasswordInput, Skeleton, Stack, Text, TextInput } from "@mantine/core";
import { useForm } from "@mantine/form";
import { useDisclosure } from "@mantine/hooks";
import { useState } from "react";
import { Link } from "react-router";
import { useApi } from "../../api/hooks.ts";
import { errorMessage } from "../../api/messages.ts";
import { isOpen, useMe, useSubscriptions } from "../../api/queries.ts";
import { authErrorMessage, PASSWORD_HINT, passwordProblem, useAuth } from "../../auth/AuthProvider.tsx";
import { Card, ErrorBanner, KeyValue, PageHeader } from "../../components/ui.tsx";
import { notifyOk } from "../../lib/mutation.ts";
import { titleOf } from "../../lib/subscription.ts";

export function AccountPage() {
  const auth = useAuth();
  const me = useMe();
  const subs = useSubscriptions();
  const [pw, pwModal] = useDisclosure(false);
  const [wd, wdModal] = useDisclosure(false);
  const open = (subs.data ?? []).filter(isOpen);
  return (
    <>
      <PageHeader title="アカウント" />
      <ErrorBanner error={me.error} />
      <Card title="登録の情報">
        {me.data ? (
          <KeyValue
            items={[
              { label: "氏名", value: me.data.name },
              { label: "メールアドレス", value: me.data.email },
            ]}
          />
        ) : (
          <Skeleton h={48} />
        )}
      </Card>
      <Card title="ログイン">
        <Stack gap="sm">
          <Button variant="default" onClick={pwModal.open}>
            パスワードを変更する
          </Button>
          <Button
            variant="default"
            onClick={() => void auth.logout("logout")}
          >
            ログアウト
          </Button>
        </Stack>
      </Card>
      <Card title="退会">
        {open.length > 0 ? (
          <Alert color="gray" variant="light" data-testid="withdraw-blocked">
            <Text size="sm">ご利用中の契約があるため退会できません。先に次の契約を解約してください（解約済みになると退会できます）。</Text>
            <Stack gap={2} mt="xs">
              {open.map((s) => (
                <Text key={s.id} size="sm" component={Link} to={`/subscriptions/${s.id}`} c="brand.7">
                  {titleOf(s).tenant}「{titleOf(s).name}」
                </Text>
              ))}
            </Stack>
          </Alert>
        ) : (
          <>
            <Text size="sm" c="dimmed" mb="sm">
              退会すると、アカウントと登録したカードは使えなくなり、元に戻せません。請求の記録は法令に基づき保存されます。
            </Text>
            <Button color="red" variant="light" onClick={wdModal.open} disabled={subs.isLoading}>
              退会する
            </Button>
          </>
        )}
      </Card>
      <PasswordModal opened={pw} onClose={pwModal.close} />
      {me.data && <WithdrawModal opened={wd} onClose={wdModal.close} email={me.data.email} />}
    </>
  );
}

function PasswordModal({ opened, onClose }: { opened: boolean; onClose: () => void }) {
  const auth = useAuth();
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const form = useForm({ initialValues: { current: "", next: "" }, validate: { next: passwordProblem } });
  return (
    <Modal opened={opened} onClose={onClose} title="パスワードの変更">
      <form
        onSubmit={form.onSubmit(async (v) => {
          setBusy(true);
          setError(null);
          try {
            await auth.changePassword(v.current, v.next);
            notifyOk("パスワードを変更しました");
            form.reset();
            onClose();
          } catch (e) {
            setError((e as { name?: string })?.name === "NotAuthorizedException" ? "現在のパスワードが正しくありません。" : authErrorMessage(e));
          } finally {
            setBusy(false);
          }
        })}
      >
        <Stack>
          {error && <Alert color="red" variant="light" role="alert">{error}</Alert>}
          <PasswordInput label="現在のパスワード" autoComplete="current-password" required {...form.getInputProps("current")} />
          <PasswordInput label="新しいパスワード" description={PASSWORD_HINT} autoComplete="new-password" required {...form.getInputProps("next")} />
          <Button type="submit" loading={busy}>
            変更する
          </Button>
        </Stack>
      </form>
    </Modal>
  );
}

function WithdrawModal({ opened, onClose, email }: { opened: boolean; onClose: () => void; email: string }) {
  const api = useApi();
  const auth = useAuth();
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const form = useForm({ initialValues: { email: "" }, validate: { email: (v) => (v.trim().toLowerCase() === email.toLowerCase() ? null : "登録しているメールアドレスを入力してください") } });
  return (
    <Modal opened={opened} onClose={onClose} title="退会の確認">
      <form
        onSubmit={form.onSubmit(async () => {
          setBusy(true);
          setError(null);
          try {
            await api.del("/v1/me");
            await auth.deleteAccount(); // ログインの画面（退会しました）へは RequireAuth が移す
          } catch (e) {
            setError(errorMessage(e));
          } finally {
            setBusy(false);
          }
        })}
      >
        <Stack>
          <Text size="sm">退会は取り消せません。確認のため、登録しているメールアドレスを入力してください。</Text>
          {error && <Alert color="red" variant="light" role="alert">{error}</Alert>}
          <TextInput label="メールアドレス" type="email" autoComplete="off" required {...form.getInputProps("email")} />
          <Button type="submit" color="red" loading={busy}>
            退会する
          </Button>
        </Stack>
      </form>
    </Modal>
  );
}
