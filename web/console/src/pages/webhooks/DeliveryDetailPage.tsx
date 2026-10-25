// 配信の詳細（FR-013）：試行の一覧・送った本文、失敗・中止の配信の手動再送。
import { Anchor, Code, Group, Paper, ScrollArea, Skeleton, Stack, Text, Title } from "@mantine/core";
import { IconArrowLeft, IconRefresh } from "@tabler/icons-react";
import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "react-router";
import { useApi } from "../../api/hooks.ts";
import type { Attempt, Delivery } from "../../api/types.ts";
import { ListTable } from "../../components/ListTable.tsx";
import { CanButton, CopyText, ErrorBanner, KeyValue, Section, StatusBadge } from "../../components/ui.tsx";
import { dateTime } from "../../lib/format.ts";
import { useWrite } from "../../lib/mutation.ts";
import { errorKindLabel } from "./events.ts";

type Detail = Delivery & { attempts: Attempt[]; payload: Record<string, unknown> };

export function DeliveryDetailPage() {
  const api = useApi();
  const navigate = useNavigate();
  const { tenantId, deliveryId } = useParams();
  const q = useQuery({ queryKey: ["delivery", tenantId, deliveryId], queryFn: () => api.get<Detail>(`/v1/console/webhook-deliveries/${deliveryId}`) });
  const d = q.data;
  const redeliver = useWrite({
    fn: () => api.post<Delivery>(`/v1/console/webhook-deliveries/${deliveryId}/redeliver`),
    invalidate: [["deliveries"]],
    success: "再送を受け付けました",
    onSuccess: (r) => navigate(`/t/${tenantId}/webhooks/deliveries/${r.id}`),
  });
  return (
    <>
      <Anchor component={Link} to={`/t/${tenantId}/webhooks/deliveries`} size="sm" c="dimmed">
        <Group gap={4}>
          <IconArrowLeft size={14} /> 配信の履歴
        </Group>
      </Anchor>
      <ErrorBanner error={q.error} onRetry={() => void q.refetch()} />
      {!d ? (
        <Skeleton h={120} mt="md" />
      ) : (
        <Stack gap={0} mt="sm">
          <Group justify="space-between" mb="lg">
            <Group gap="sm">
              <Title order={1} ff="monospace" fz={20}>
                {d.event.type}
              </Title>
              <StatusBadge status={d.status} />
            </Group>
            {(d.status === "failed" || d.status === "abandoned" || d.status === "succeeded") && (
              <CanButton permission="webhook.redeliver" variant="default" leftSection={<IconRefresh size={16} />} loading={redeliver.isPending} onClick={() => redeliver.mutate(undefined)}>
                再送
              </CanButton>
            )}
          </Group>
          <Section title="配信の情報">
            <KeyValue
              items={[
                { label: "イベント ID", value: <CopyText value={d.event.id} /> },
                { label: "配信 ID", value: <CopyText value={d.id} /> },
                { label: "種類", value: d.trigger === "manual" ? "手動の再送" : "自動" },
                {
                  label: "送信先",
                  value: (
                    <Anchor component={Link} to={`/t/${tenantId}/webhooks/endpoints/${d.endpoint_id}`} size="sm">
                      送信先を開く
                    </Anchor>
                  ),
                },
                { label: "イベントの発生日時", value: dateTime(d.event.created_at) },
                { label: "最後の試行", value: dateTime(d.last_attempted_at) },
                ...(d.status === "pending" ? [{ label: "次の試行の予定", value: dateTime(d.next_attempt_at) }, { label: "再試行の期限", value: dateTime(d.deadline_at) }] : []),
                ...(d.redelivery_of
                  ? [
                      {
                        label: "再送元",
                        value: (
                          <Anchor component={Link} to={`/t/${tenantId}/webhooks/deliveries/${d.redelivery_of}`} size="sm">
                            元の配信を開く
                          </Anchor>
                        ),
                      },
                    ]
                  : []),
              ]}
            />
          </Section>
          <Title order={3} mb="sm">
            試行
          </Title>
          <ListTable
            rows={d.attempts}
            rowKey={(r) => String(r.attempt_no)}
            columns={[
              { key: "no", header: "#", render: (r) => r.attempt_no, width: 48 },
              { key: "at", header: "日時", render: (r) => dateTime(r.requested_at) },
              {
                key: "result",
                header: "結果",
                render: (r) =>
                  r.status_code ? (
                    <StatusBadge status={r.status_code >= 200 && r.status_code < 300 ? "succeeded" : "failed"} label={`HTTP ${r.status_code}`} />
                  ) : (
                    <StatusBadge status="failed" label={r.error_kind ? (errorKindLabel[r.error_kind] ?? r.error_kind) : "失敗"} />
                  ),
              },
              { key: "ms", header: "所要時間", align: "right", render: (r) => `${r.duration_ms} ms` },
              { key: "body", header: "応答の抜粋", render: (r) => <Text size="xs" ff="monospace" lineClamp={2} maw={320}>{r.response_excerpt ?? "—"}</Text> },
            ]}
            empty={{ title: "まだ送信していません" }}
          />
          <Title order={3} mt="lg" mb="sm">
            送った本文
          </Title>
          <Paper p={0}>
            <ScrollArea.Autosize mah={420}>
              <Code block fz="xs" bg="transparent" p="md">
                {JSON.stringify(d.payload, null, 2)}
              </Code>
            </ScrollArea.Autosize>
          </Paper>
        </Stack>
      )}
    </>
  );
}
