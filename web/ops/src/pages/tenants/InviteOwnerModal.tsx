// オーナーの招待（FR-003）：メールアドレス・氏名で、事業者の最初のオーナーを招待する（招待メールが届く。business-console FR-006）。
import { Button, Group, Modal, Stack, Text, TextInput } from "@mantine/core";
import { useForm } from "@mantine/form";
import { useApi } from "../../api/hooks.ts";
import type { Tenant } from "../../api/types.ts";
import { useWrite } from "../../lib/mutation.ts";

export function InviteOwnerModal({ tenant, opened, onClose }: { tenant: Tenant | null; opened: boolean; onClose: () => void }) {
  const api = useApi();
  const form = useForm({
    initialValues: { email: "", display_name: "" },
    validate: {
      email: (v) => (/^\S+@\S+\.\S+$/.test(v.trim()) ? null : "メールアドレスの形式が正しくありません"),
      display_name: (v) => (v.trim() ? null : "入力してください"),
    },
  });
  const invite = useWrite({
    fn: (v: typeof form.values) => api.post(`/v1/ops/tenants/${tenant!.id}/staff`, { email: v.email.trim(), display_name: v.display_name.trim(), role: "owner" }),
    form,
    success: "オーナーに招待メールを送りました",
    onSuccess: () => (form.reset(), onClose()),
  });
  return (
    <Modal opened={opened} onClose={onClose} title="オーナーを招待">
      <form onSubmit={form.onSubmit((v) => invite.mutate(v))}>
        <Stack>
          <Text size="sm" c="dimmed">
            {tenant?.name} の最初のオーナーを招待します。招待メールから事業者コンソールにログインできます（招待の有効期限は 7 日）。
          </Text>
          <TextInput label="メールアドレス" required {...form.getInputProps("email")} data-autofocus />
          <TextInput label="氏名" required {...form.getInputProps("display_name")} />
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>
              あとで
            </Button>
            <Button type="submit" loading={invite.isPending}>
              招待する
            </Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}
