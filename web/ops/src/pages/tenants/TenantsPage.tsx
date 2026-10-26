// 事業者の一覧と登録（FR-003）：状態で絞り込み（新しい順・ページング）、名前で登録し、続けて最初のオーナーを招待する。
import { Button, Group, Modal, SegmentedControl, Stack, TextInput } from "@mantine/core";
import { useForm } from "@mantine/form";
import { useDisclosure } from "@mantine/hooks";
import { IconPlus } from "@tabler/icons-react";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useNavigate, useSearchParams } from "react-router";
import { useApi } from "../../api/hooks.ts";
import type { Page, Tenant } from "../../api/types.ts";
import { ListTable, useCursorPager } from "../../components/ListTable.tsx";
import { CopyText, ErrorBanner, PageHeader, StatusBadge } from "../../components/ui.tsx";
import { dateTime } from "../../lib/format.ts";
import { useWrite } from "../../lib/mutation.ts";
import { InviteOwnerModal } from "./InviteOwnerModal.tsx";

const filters = [
  { value: "", label: "すべて" },
  { value: "pending_review", label: "審査待ち" },
  { value: "active", label: "有効" },
  { value: "suspended", label: "停止中" },
];

export function TenantsPage() {
  const api = useApi();
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const status = params.get("status") ?? "";
  const pager = useCursorPager();
  const [creating, create] = useDisclosure(false);
  const [invitee, setInvitee] = useState<Tenant | null>(null);
  const list = useQuery({
    queryKey: ["tenants", status, pager.cursor],
    queryFn: () => api.get<Page<Tenant>>("/v1/ops/tenants", { status: status || undefined, limit: 50, starting_after: pager.cursor }),
  });
  const form = useForm({ initialValues: { name: "" }, validate: { name: (v) => (v.trim() ? null : "入力してください") } });
  const save = useWrite({
    fn: (v: { name: string }) => api.post<Tenant>("/v1/ops/tenants", { name: v.name.trim() }),
    form,
    invalidate: [["tenants"]],
    success: (t) => `${t.name} を登録しました（審査待ち）`,
    onSuccess: (t) => (form.reset(), create.close(), setInvitee(t)),
  });
  return (
    <>
      <PageHeader
        title="事業者"
        description="事業者の登録・審査・停止を行います。"
        actions={
          <Button leftSection={<IconPlus size={16} />} onClick={create.open}>
            事業者を登録
          </Button>
        }
      />
      <SegmentedControl
        mb="md"
        value={status}
        data={filters}
        onChange={(v) => {
          pager.reset();
          setParams(v ? { status: v } : {});
        }}
        data-testid="tenant-status-filter"
      />
      <ErrorBanner error={list.error} onRetry={() => void list.refetch()} />
      <ListTable
        rows={list.data?.data}
        loading={list.isLoading}
        rowKey={(r) => r.id}
        onRowClick={(r) => navigate(`/tenants/${r.id}`)}
        columns={[
          { key: "name", header: "事業者名", render: (r) => r.name },
          { key: "status", header: "状態", render: (r) => <StatusBadge status={r.status} /> },
          { key: "shop", header: "fincode のショップ ID", render: (r) => r.fincode_shop_id ?? "—" },
          { key: "id", header: "ID", render: (r) => <CopyText value={r.id} maw={140} /> },
          { key: "created", header: "登録日時", render: (r) => dateTime(r.created_at) },
        ]}
        empty={{ title: "該当する事業者はありません" }}
        pager={{ page: pager.page, hasMore: !!list.data?.has_more, onNext: () => pager.next(list.data?.next_cursor ?? null), onPrev: pager.prev }}
      />
      <Modal opened={creating} onClose={create.close} title="事業者を登録">
        <form onSubmit={form.onSubmit((v) => save.mutate(v))}>
          <Stack>
            <TextInput label="事業者名" required description="登録すると審査待ちになります。続けて最初のオーナーを招待できます。" {...form.getInputProps("name")} data-autofocus />
            <Group justify="flex-end">
              <Button variant="default" onClick={create.close}>
                キャンセル
              </Button>
              <Button type="submit" loading={save.isPending}>
                登録する
              </Button>
            </Group>
          </Stack>
        </form>
      </Modal>
      <InviteOwnerModal tenant={invitee} opened={!!invitee} onClose={() => setInvitee(null)} />
    </>
  );
}
