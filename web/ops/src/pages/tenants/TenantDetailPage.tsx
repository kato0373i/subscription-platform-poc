// 事業者の詳細（FR-004・FR-005）：承認（ショップ ID）・停止（理由と事業者名の確認）・オーナーの招待・一括解約の進み具合（実行中は 5 秒ごとに読み直す）。
import { Alert, Anchor, Button, Group, Modal, Stack, Table, Text, TextInput, Textarea } from "@mantine/core";
import { useForm } from "@mantine/form";
import { useDisclosure } from "@mantine/hooks";
import { IconAlertTriangle, IconBan, IconCheck, IconUserPlus, IconWebhook } from "@tabler/icons-react";
import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "react-router";
import { useApi } from "../../api/hooks.ts";
import type { BulkJob, Tenant } from "../../api/types.ts";
import { CopyText, ErrorBanner, KeyValue, PageHeader, Section, StatusBadge } from "../../components/ui.tsx";
import { dateTime } from "../../lib/format.ts";
import { useWrite } from "../../lib/mutation.ts";
import { InviteOwnerModal } from "./InviteOwnerModal.tsx";

export function TenantDetailPage() {
  const api = useApi();
  const { tenantId } = useParams();
  const [approving, approve] = useDisclosure(false);
  const [suspending, suspend] = useDisclosure(false);
  const [inviting, invite] = useDisclosure(false);
  const tenant = useQuery({ queryKey: ["tenant", tenantId], queryFn: () => api.get<Tenant>(`/v1/ops/tenants/${tenantId}`), staleTime: 0 });
  const t = tenant.data;
  return (
    <>
      <PageHeader
        title={t?.name ?? "事業者"}
        description={t && <CopyText value={t.id} />}
        actions={
          t && (
            <>
              <Button variant="default" component={Link} to={`/tenants/${t.id}/webhooks`} leftSection={<IconWebhook size={16} />}>
                Webhook の配信
              </Button>
              {t.status !== "suspended" && (
                <Button variant="default" leftSection={<IconUserPlus size={16} />} onClick={invite.open}>
                  オーナーを招待
                </Button>
              )}
              {t.status === "pending_review" && (
                <Button leftSection={<IconCheck size={16} />} onClick={approve.open}>
                  承認する
                </Button>
              )}
              {t.status === "active" && (
                <Button color="red" variant="light" leftSection={<IconBan size={16} />} onClick={suspend.open}>
                  停止する
                </Button>
              )}
            </>
          )
        }
      />
      <ErrorBanner error={tenant.error} onRetry={() => void tenant.refetch()} />
      {t && (
        <>
          <Section title="基本情報">
            <KeyValue
              items={[
                { label: "状態", value: <span data-testid="tenant-status"><StatusBadge status={t.status} /></span> },
                { label: "fincode のショップ ID", value: t.fincode_shop_id ?? "—" },
                { label: "登録日時", value: dateTime(t.created_at) },
                { label: "承認日時", value: dateTime(t.approved_at) },
                ...(t.status === "suspended" ? [{ label: "停止日時", value: dateTime(t.suspended_at) }, { label: "停止の理由", value: t.suspend_reason ?? "—" }] : []),
              ]}
            />
          </Section>
          {t.status === "suspended" && <BulkJobs tenantId={t.id} />}
          <ApproveModal tenant={t} opened={approving} onClose={approve.close} />
          <SuspendModal tenant={t} opened={suspending} onClose={suspend.close} />
          <InviteOwnerModal tenant={t} opened={inviting} onClose={invite.close} />
        </>
      )}
    </>
  );
}

function ApproveModal({ tenant, opened, onClose }: { tenant: Tenant; opened: boolean; onClose: () => void }) {
  const api = useApi();
  const form = useForm({ initialValues: { fincode_shop_id: "" }, validate: { fincode_shop_id: (v) => (v.trim() ? null : "入力してください") } });
  const save = useWrite({
    fn: (v: typeof form.values) => api.post<Tenant>(`/v1/ops/tenants/${tenant.id}/approve`, { fincode_shop_id: v.fincode_shop_id.trim() }),
    form,
    invalidate: [["tenant", tenant.id], ["tenants"]],
    success: `${tenant.name} を承認しました`,
    onSuccess: () => (form.reset(), onClose()),
  });
  return (
    <Modal opened={opened} onClose={onClose} title="事業者を承認">
      <form onSubmit={form.onSubmit((v) => save.mutate(v))}>
        <Stack>
          <Text size="sm">{tenant.name} を承認し、有効にします。承認後、事業者はプランを公開して販売を始められます。</Text>
          <TextInput label="fincode のショップ ID" required description="fincode の審査で発行されたショップ ID" {...form.getInputProps("fincode_shop_id")} data-autofocus />
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>
              キャンセル
            </Button>
            <Button type="submit" loading={save.isPending}>
              承認する
            </Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}

function SuspendModal({ tenant, opened, onClose }: { tenant: Tenant; opened: boolean; onClose: () => void }) {
  const api = useApi();
  const form = useForm({
    initialValues: { reason: "", confirm: "" },
    validate: {
      reason: (v) => (v.trim() ? null : "入力してください"),
      confirm: (v) => (v === tenant.name ? null : "事業者名が一致しません"),
    },
  });
  const save = useWrite({
    fn: (v: typeof form.values) => api.post<Tenant>(`/v1/ops/tenants/${tenant.id}/suspend`, { reason: v.reason.trim() }),
    form,
    invalidate: [["tenant", tenant.id], ["tenants"], ["bulk-jobs", tenant.id]],
    success: `${tenant.name} を停止しました`,
    onSuccess: () => (form.reset(), onClose()),
  });
  return (
    <Modal opened={opened} onClose={onClose} title="事業者を停止">
      <form onSubmit={form.onSubmit((v) => save.mutate(v))}>
        <Stack>
          <Alert color="red" variant="light" icon={<IconAlertTriangle size={18} />}>
            停止すると、この事業者のすべての契約の一括解約が始まります（期間の終了で解約、または即時解約）。停止は取り消せません。
          </Alert>
          <Textarea label="停止の理由" required autosize minRows={2} {...form.getInputProps("reason")} />
          <TextInput label={`確認のため、事業者名「${tenant.name}」を入力してください`} required {...form.getInputProps("confirm")} />
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>
              キャンセル
            </Button>
            <Button type="submit" color="red" loading={save.isPending}>
              停止する
            </Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}

function BulkJobs({ tenantId }: { tenantId: string }) {
  const api = useApi();
  const jobs = useQuery({
    queryKey: ["bulk-jobs", tenantId],
    queryFn: () => api.get<{ data: BulkJob[] }>("/v1/ops/bulk-cancellation-jobs", { tenant_id: tenantId }),
    // 実行中（またはジョブがまだ作られていない）あいだは 5 秒ごとに読み直す（FR-005）
    refetchInterval: (q) => (!q.state.data || q.state.data.data.length === 0 || q.state.data.data.some((j) => j.status === "running") ? 5000 : false),
  });
  return (
    <Section title="一括解約">
      <ErrorBanner error={jobs.error} />
      {jobs.data?.data.length === 0 && (
        <Text size="sm" c="dimmed">
          一括解約のジョブを準備しています…
        </Text>
      )}
      <Stack>
        {jobs.data?.data.map((j) => (
          <div key={j.id} data-testid="bulk-job">
            <Group gap="sm" mb="xs">
              <StatusBadge status={j.status} />
              <Text size="sm" c="dimmed">
                開始 {dateTime(j.started_at)}
                {j.completed_at && `・完了 ${dateTime(j.completed_at)}`}・{j.sweep_round} 回目の走査
              </Text>
            </Group>
            <KeyValue
              items={[
                { label: "処理済み", value: <span data-testid="bulk-processed">{j.counts.processed} 件</span> },
                { label: "期間の終了で解約", value: `${j.counts.scheduled_at_period_end} 件` },
                { label: "即時解約", value: `${j.counts.canceled_immediately} 件` },
                { label: "対象外（解約済みなど）", value: `${j.counts.skipped} 件` },
                { label: "失敗", value: <Text c={j.counts.failed ? "red" : undefined} size="sm">{j.counts.failed} 件</Text> },
              ]}
            />
            {j.failures.length > 0 && (
              <Table mt="md" fz="sm">
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>失敗した契約</Table.Th>
                    <Table.Th>エラー</Table.Th>
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {j.failures.map((f) => (
                    <Table.Tr key={f.subscription_id}>
                      <Table.Td>
                        <CopyText value={f.subscription_id} />
                      </Table.Td>
                      <Table.Td>{f.error}</Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            )}
          </div>
        ))}
      </Stack>
      <Anchor component={Link} to={`/audit?tenant_id=${tenantId}&action=tenant.suspend`} size="sm" mt="md" display="block">
        停止の監査ログを見る
      </Anchor>
    </Section>
  );
}
