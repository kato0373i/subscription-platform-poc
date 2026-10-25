// API キー（FR-014）：発行（平文はこの応答だけ）・一覧・失効。
import { Button, Group, Modal, Stack, Tabs, Text, TextInput } from "@mantine/core";
import { useForm } from "@mantine/form";
import { useDisclosure } from "@mantine/hooks";
import { IconPlus } from "@tabler/icons-react";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useParams } from "react-router";
import { useApi } from "../../api/hooks.ts";
import type { ApiKey, Page } from "../../api/types.ts";
import { ListTable, useCursorPager } from "../../components/ListTable.tsx";
import { CanButton, ErrorBanner, PageHeader, SecretReveal, StatusBadge, confirmDanger } from "../../components/ui.tsx";
import { dateTime } from "../../lib/format.ts";
import { useWrite } from "../../lib/mutation.ts";

export function ApiKeysPage() {
  const api = useApi();
  const { tenantId } = useParams();
  const [status, setStatus] = useState("active");
  const pager = useCursorPager();
  const [opened, { open, close }] = useDisclosure(false);
  const [issued, setIssued] = useState<string | null>(null);
  const list = useQuery({
    queryKey: ["api-keys", tenantId, status, pager.cursor],
    queryFn: () => api.get<Page<ApiKey>>("/v1/console/api-keys", { status: status === "all" ? undefined : status, limit: 20, starting_after: pager.cursor }),
  });
  const revoke = useWrite({ fn: (k: ApiKey) => api.del(`/v1/console/api-keys/${k.id}`), invalidate: [["api-keys"]], success: "API キーを失効させました" });
  const form = useForm({ initialValues: { name: "" }, validate: { name: (v) => (v.trim() ? null : "入力してください") } });
  const create = useWrite({
    fn: (v: typeof form.values) => api.post<{ api_key: string }>("/v1/console/api-keys", { name: v.name.trim() }),
    invalidate: [["api-keys"]],
    success: "API キーを発行しました",
    form,
    onSuccess: (r) => (form.reset(), close(), setIssued(r.api_key)),
  });
  return (
    <>
      <PageHeader
        title="API キー"
        description="自社のサーバーから契約の照会・解約などの API を呼ぶためのキーです。有効なキーは 10 個まで発行できます。"
        actions={
          <CanButton permission="apikey.manage" leftSection={<IconPlus size={16} />} onClick={open}>
            API キーを発行
          </CanButton>
        }
      />
      <Tabs value={status} onChange={(v) => (setStatus(v ?? "active"), pager.reset())} mb="md">
        <Tabs.List>
          <Tabs.Tab value="active">有効</Tabs.Tab>
          <Tabs.Tab value="revoked">失効</Tabs.Tab>
          <Tabs.Tab value="all">すべて</Tabs.Tab>
        </Tabs.List>
      </Tabs>
      <ErrorBanner error={list.error} onRetry={() => void list.refetch()} />
      <ListTable
        rows={list.data?.data}
        loading={list.isLoading}
        rowKey={(r) => r.id}
        columns={[
          { key: "name", header: "名前", render: (r) => <Text size="sm" fw={500}>{r.name}</Text> },
          { key: "prefix", header: "キー", render: (r) => <Text size="sm" ff="monospace">{r.key_prefix}…</Text> },
          { key: "status", header: "状態", render: (r) => <StatusBadge status={r.status === "active" ? "enabled" : "revoked"} /> },
          { key: "created", header: "発行日時", render: (r) => dateTime(r.created_at) },
          { key: "revoked", header: "失効日時", render: (r) => dateTime(r.revoked_at) },
          {
            key: "actions",
            header: "",
            align: "right",
            render: (r) =>
              r.status === "active" && (
                <CanButton
                  permission="apikey.manage"
                  variant="subtle"
                  color="red"
                  size="xs"
                  onClick={() =>
                    confirmDanger({
                      title: `「${r.name}」を失効させますか？`,
                      message: "失効させたキーを使う API の呼び出しは、すぐに失敗するようになります。元に戻せません。",
                      confirm: "失効させる",
                      onConfirm: () => revoke.mutate(r),
                    })
                  }
                >
                  失効させる
                </CanButton>
              ),
          },
        ]}
        empty={{ title: "API キーはありません" }}
        pager={{ page: pager.page, hasMore: !!list.data?.has_more, onNext: () => pager.next(list.data?.next_cursor ?? null), onPrev: pager.prev }}
      />
      <Modal opened={opened} onClose={close} title="API キーを発行">
        <form onSubmit={form.onSubmit((v) => create.mutate(v))}>
          <Stack>
            <TextInput label="名前" placeholder="例：本番の会員システム" description="用途がわかる名前を付けてください" withAsterisk {...form.getInputProps("name")} />
            <Button type="submit" loading={create.isPending}>
              発行
            </Button>
          </Stack>
        </form>
      </Modal>
      <Modal opened={!!issued} onClose={() => setIssued(null)} title="API キー" closeOnClickOutside={false} size="lg">
        {issued && (
          <Stack>
            <SecretReveal label="API キー" value={issued} />
            <Group justify="flex-end">
              <Button onClick={() => setIssued(null)}>控えました</Button>
            </Group>
          </Stack>
        )}
      </Modal>
    </>
  );
}
