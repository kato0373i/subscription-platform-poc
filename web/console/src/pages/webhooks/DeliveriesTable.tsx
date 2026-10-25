// 配信の履歴（FR-013）：状態・送信先・イベントの種類で絞り込む。
import { Group, Select, Text } from "@mantine/core";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useNavigate, useParams } from "react-router";
import { useApi } from "../../api/hooks.ts";
import type { Delivery, Endpoint, Page } from "../../api/types.ts";
import { ListTable, useCursorPager } from "../../components/ListTable.tsx";
import { ErrorBanner, StatusBadge } from "../../components/ui.tsx";
import { dateTime } from "../../lib/format.ts";
import { errorKindLabel, eventTypes } from "./events.ts";

export function responseLabel(d: Pick<Delivery, "last_response">) {
  const r = d.last_response;
  if (!r) return "—";
  if (r.status_code) return `HTTP ${r.status_code}`;
  return r.error_kind ? (errorKindLabel[r.error_kind] ?? r.error_kind) : "—";
}

export function DeliveriesTable({ endpointId }: { endpointId?: string }) {
  const api = useApi();
  const navigate = useNavigate();
  const { tenantId } = useParams();
  const pager = useCursorPager();
  const [status, setStatus] = useState<string | null>(null);
  const [ep, setEp] = useState<string | null>(endpointId ?? null);
  const [type, setType] = useState<string | null>(null);
  const endpoints = useQuery({
    queryKey: ["endpoints", tenantId, "all"],
    queryFn: () => api.get<Page<Endpoint>>("/v1/console/webhook-endpoints", { limit: 100 }).then((r) => r.data),
    enabled: !endpointId,
  });
  const q = { status: status ?? undefined, endpoint_id: ep ?? undefined, event_type: type ?? undefined, starting_after: pager.cursor };
  const list = useQuery({
    queryKey: ["deliveries", tenantId, q],
    queryFn: () => api.get<Page<Delivery>>("/v1/console/webhook-deliveries", { ...q, limit: 20 }),
    placeholderData: keepPreviousData,
  });
  const epUrl = (id: string) => endpoints.data?.find((e) => e.id === id)?.url ?? id;
  return (
    <>
      <Group mb="md">
        <Select
          placeholder="すべての状態"
          aria-label="状態"
          clearable
          w={{ base: "100%", sm: 160 }}
          value={status}
          onChange={(v) => (setStatus(v), pager.reset())}
          data={[
            { value: "pending", label: "送信待ち" },
            { value: "succeeded", label: "成功" },
            { value: "failed", label: "失敗" },
            { value: "abandoned", label: "中止" },
          ]}
        />
        {!endpointId && (
          <Select
            placeholder="すべての送信先"
            aria-label="送信先"
            clearable
            w={{ base: "100%", sm: 300 }}
            value={ep}
            onChange={(v) => (setEp(v), pager.reset())}
            data={endpoints.data?.map((e) => ({ value: e.id, label: e.url })) ?? []}
          />
        )}
        <Select
          placeholder="すべてのイベント"
          aria-label="イベント"
          clearable
          searchable
          w={{ base: "100%", sm: 260 }}
          value={type}
          onChange={(v) => (setType(v), pager.reset())}
          data={eventTypes.map((e) => ({ value: e.value, label: e.label }))}
        />
      </Group>
      <ErrorBanner error={list.error} onRetry={() => void list.refetch()} />
      <ListTable
        rows={list.data?.data}
        loading={list.isLoading}
        rowKey={(r) => r.id}
        onRowClick={(r) => navigate(`/t/${tenantId}/webhooks/deliveries/${r.id}`)}
        columns={[
          {
            key: "event",
            header: "イベント",
            render: (r) => (
              <div>
                <Text size="sm" ff="monospace">
                  {r.event.type}
                </Text>
                <Text size="xs" c="dimmed" ff="monospace">
                  {r.event.id}
                </Text>
              </div>
            ),
          },
          { key: "status", header: "状態", render: (r) => <StatusBadge status={r.status} /> },
          ...(endpointId ? [] : [{ key: "endpoint", header: "送信先", render: (r: Delivery) => <Text size="sm" truncate maw={220}>{epUrl(r.endpoint_id)}</Text> }]),
          { key: "response", header: "最後の応答", render: responseLabel },
          { key: "attempts", header: "試行", align: "right" as const, render: (r) => r.attempt_count },
          { key: "created", header: "作成日時", render: (r) => dateTime(r.created_at) },
        ]}
        empty={{ title: "配信はまだありません" }}
        pager={{ page: pager.page, hasMore: !!list.data?.has_more, onNext: () => pager.next(list.data?.next_cursor ?? null), onPrev: pager.prev }}
      />
    </>
  );
}
