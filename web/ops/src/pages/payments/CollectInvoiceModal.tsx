// 請求の再決済（FR-009）：確認つきで、請求の回収を依頼する。請求の ID を渡さなければ入力させる。
import { Alert, Button, Group, Modal, Stack, Text, TextInput } from "@mantine/core";
import { useForm } from "@mantine/form";
import { useApi } from "../../api/hooks.ts";
import { useWrite } from "../../lib/mutation.ts";

const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export function CollectInvoiceModal({ invoiceId, opened, onClose }: { invoiceId?: string; opened: boolean; onClose: () => void }) {
  const api = useApi();
  const form = useForm({
    initialValues: { invoice_id: "", note: "" },
    validate: { invoice_id: (v) => (invoiceId || uuid.test(v.trim()) ? null : "請求の ID（UUID）を入力してください") },
  });
  const collect = useWrite({
    fn: (v: typeof form.values) => api.post(`/v1/ops/invoices/${invoiceId ?? v.invoice_id.trim()}/collect`, v.note.trim() ? { note: v.note.trim() } : {}),
    form,
    invalidate: [["attempts"], ["attempt"]],
    success: "再決済を依頼しました。結果は数秒後に反映されます",
    onSuccess: () => (form.reset(), onClose()),
  });
  return (
    <Modal opened={opened} onClose={onClose} title="請求の再決済">
      <form onSubmit={form.onSubmit((v) => collect.mutate(v))}>
        <Stack>
          <Alert color="yellow" variant="light">
            契約者の登録済みの支払い方法で、もう一度決済します。決済中・支払い済みの請求は再決済できません。
          </Alert>
          {invoiceId ? (
            <Text size="sm">
              請求 <Text span ff="monospace">{invoiceId}</Text>
            </Text>
          ) : (
            <TextInput label="請求の ID" required {...form.getInputProps("invoice_id")} data-autofocus />
          )}
          <TextInput label="メモ（任意）" maxLength={500} {...form.getInputProps("note")} />
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>
              キャンセル
            </Button>
            <Button type="submit" loading={collect.isPending}>
              再決済する
            </Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}
