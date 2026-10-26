// Webhook の配信の検索と再送（FR-010）：事業者の配信を状態で絞り込み、失敗・中止の配信を再送する（確認つき）。
import { Anchor, Button, SegmentedControl, Text } from "@mantine/core";
import { IconArrowLeft, IconRefresh } from "@tabler/icons-react";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useParams } from "react-router";
import { useApi } from "../../api/hooks.ts";
import { useTenant } from "../../api/queries.ts";
import type { Delivery, Page } from "../../api/types.ts";
import { ListTable, useCursorPager } from "../../components/ListTable.tsx";
import { CopyText, ErrorBanner, PageHeader, StatusBadge, confirmDanger } from "../../components/ui.tsx";
import { dateTime } from "../../lib/format.ts";
import { useWrite } from "../../lib/mutation.ts";

const filters = [
  { value: "", label: "すべて" },
  { value: "failed", label: "失敗" },
  { value: "abandoned", label: "中止" },
  { value: "pending", label: "送信待ち" },
  { value: "succeeded", label: "成功" },
];

export function TenantWebhooksPage() {
  const api = useApi();
  const { tenantId } = useParams();
  const tenant = useTenant(tenantId);
  const [status, setStatus] = useState("failed");
  const pager = useCursorPager();
  const list = useQuery({
    queryKey: ["deliveries", tenantId, status, pager.cursor],
    queryFn: () => api.get<Page<Delivery>>("/v1/ops/webhook-deliveries", { tenant_id: tenantId, status: status || undefined, limit: 50, starting_after: pager.cursor }),
  });
  const redeliver = useWrite({
    fn: (d: Delivery) => api.post<Delivery>(`/v1/ops/webhook-deliveries/${d.id}/redeliver`),
    invalidate: [["deliveries", tenantId]],
    success: "再送の配信を作りました",
  });
  return (
    <>
      <Anchor component={Link} to={`/tenants/${tenantId}`} size="sm" mb="xs" display="inline-flex" style={{ alignItems: "center", gap: 4 }}>
        <IconArrowLeft size={14} /> {tenant.data?.name ?? "事業者"}
      </Anchor>
      <PageHeader title="Webhook の配信" description="失敗・中止した配信を、事業者の送信先に送り直せます。" />
      <SegmentedControl
        mb="md"
        value={status}
        data={filters}
        onChange={(v) => {
          pager.reset();
          setStatus(v);
        }}
      />
      <ErrorBanner error={list.error} onRetry={() => void list.refetch()} />
      <ListTable
        rows={list.data?.data}
        loading={list.isLoading}
        rowKey={(r) => r.id}
        columns={[
          {
            key: "event",
            header: "イベント",
            render: (r) => (
              <div>
                <Text size="sm" ff="monospace">
                  {r.event.type}
                </Text>
                <Text size="xs" c="dimmed">
                  {dateTime(r.event.created_at)}
                </Text>
              </div>
            ),
          },
          { key: "status", header: "状態", render: (r) => <StatusBadge status={r.status} /> },
          { key: "trigger", header: "種類", render: (r) => (r.trigger === "manual" ? "手動の再送" : "自動") },
          { key: "attempts", header: "試行", render: (r) => `${r.attempt_count} 回`, align: "right" },
          { key: "resp", header: "最後の応答", render: (r) => (r.last_response ? (r.last_response.status_code ?? r.last_response.error_kind ?? "—") : "—") },
          { key: "last", header: "最後の試行", render: (r) => dateTime(r.last_attempted_at) },
          { key: "id", header: "配信 ID", render: (r) => <CopyText value={r.id} maw={110} /> },
          {
            key: "actions",
            header: "",
            align: "right",
            render: (r) =>
              (r.status === "failed" || r.status === "abandoned") && (
                <Button
                  size="xs"
                  variant="light"
                  leftSection={<IconRefresh size={14} />}
                  loading={redeliver.isPending && redeliver.variables?.id === r.id}
                  onClick={() =>
                    confirmDanger({
                      title: "配信を再送",
                      message: `${r.event.type} を事業者の送信先に送り直します。事業者のシステムが同じイベントを再び受け取ります。`,
                      confirm: "再送する",
                      onConfirm: () => redeliver.mutate(r),
                    })
                  }
                >
                  再送
                </Button>
              ),
          },
        ]}
        empty={{ title: "該当する配信はありません" }}
        pager={{ page: pager.page, hasMore: !!list.data?.has_more, onNext: () => pager.next(list.data?.next_cursor ?? null), onPrev: pager.prev }}
      />
    </>
  );
}
