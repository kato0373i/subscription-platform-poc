// 契約の詳細（FR-010）：契約者（事業者に渡してよい範囲）、概要、予約中の変更、請求の履歴。
import { Anchor, Badge, Group, Skeleton, Stack, Text, Title } from "@mantine/core";
import { IconArrowLeft } from "@tabler/icons-react";
import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "react-router";
import { useApi } from "../../api/hooks.ts";
import { useAllPlans } from "../../api/queries.ts";
import type { Invoice, Page, Subscription } from "../../api/types.ts";
import { intervalLabel } from "../../api/types.ts";
import { ListTable } from "../../components/ListTable.tsx";
import { CopyText, ErrorBanner, KeyValue, Section, StatusBadge } from "../../components/ui.tsx";
import { date, dateTime, yen } from "../../lib/format.ts";

const cancelReason: Record<string, string> = { customer: "契約者による解約", dunning: "支払い失敗による解約", tenant_suspended: "事業者の停止による解約" };

export function SubscriptionDetailPage() {
  const api = useApi();
  const { tenantId, id } = useParams();
  const sub = useQuery({ queryKey: ["subscription", tenantId, id], queryFn: () => api.get<Subscription>(`/v1/console/subscriptions/${id}`) });
  const invoices = useQuery({ queryKey: ["invoices", tenantId, id], queryFn: () => api.get<Page<Invoice>>(`/v1/console/subscriptions/${id}/invoices`, { limit: 50 }) });
  const plans = useAllPlans();
  const s = sub.data;
  const plan = plans.data?.find((p) => p.id === s?.plan_id);
  return (
    <>
      <Anchor component={Link} to={`/t/${tenantId}/subscriptions`} size="sm" c="dimmed">
        <Group gap={4}>
          <IconArrowLeft size={14} /> 契約
        </Group>
      </Anchor>
      <ErrorBanner error={sub.error} onRetry={() => void sub.refetch()} />
      {!s ? (
        <Skeleton h={120} mt="md" />
      ) : (
        <Stack gap={0} mt="sm">
          <Group justify="space-between" mb="lg">
            <div>
              <Group gap="sm">
                <Title order={1}>{s.customer.withdrawn ? "退会済みの契約者" : (s.customer.name ?? s.customer.email)}</Title>
                <StatusBadge status={s.status} />
              </Group>
              <Group gap="xs" mt={4}>
                <Text size="sm" c="dimmed">
                  {plan ? `${plan.name}・${yen(plan.current_price.amount)}／${intervalLabel[plan.billing_interval]}` : ""}
                </Text>
              </Group>
            </div>
            <CopyText value={s.id} />
          </Group>
          <Section title="契約者">
            <KeyValue
              items={[
                { label: "氏名", value: s.customer.withdrawn ? "退会済み" : (s.customer.name ?? "—") },
                { label: "メールアドレス", value: s.customer.withdrawn ? "退会済み" : (s.customer.email ?? "—") },
                { label: "契約者 ID", value: <CopyText value={s.customer_id} /> },
              ]}
            />
          </Section>
          <Section title="概要">
            <KeyValue
              items={[
                { label: "状態", value: <StatusBadge status={s.status} /> },
                { label: "利用権", value: s.entitled ? <Badge color="green">あり</Badge> : <Badge color="gray">なし</Badge> },
                { label: "現在の期間", value: `${date(s.current_period.start)} 〜 ${date(s.current_period.end)}` },
                { label: "次回の更新日", value: date(s.next_renewal_date) },
                { label: "トライアル", value: s.trial ? `${date(s.trial.start)} 〜 ${date(s.trial.end)}` : "なし" },
                { label: "開始日時", value: dateTime(s.created_at) },
                ...(s.scheduled_plan_change ? [{ label: "予約中のプラン変更", value: `${date((s.scheduled_plan_change as { effective_date: string }).effective_date)} から変更` }] : []),
                ...(s.scheduled_cancellation ? [{ label: "解約の予約", value: `${date((s.scheduled_cancellation as { effective_date: string }).effective_date)} で終了` }] : []),
                ...(s.cancel_reason ? [{ label: "解約理由", value: cancelReason[s.cancel_reason] ?? s.cancel_reason }, { label: "解約日時", value: dateTime(s.canceled_at) }] : []),
              ]}
            />
          </Section>
          <Title order={3} mb="sm">
            請求の履歴
          </Title>
          <ErrorBanner error={invoices.error} />
          <ListTable
            rows={invoices.data?.data}
            loading={invoices.isLoading}
            rowKey={(r) => r.id}
            columns={[
              { key: "period", header: "期間", render: (r) => `${date(r.period.start)} 〜 ${date(r.period.end)}` },
              { key: "amount", header: "金額", align: "right", render: (r) => <Text ff="monospace" size="sm">{yen(r.total)}</Text> },
              { key: "status", header: "状態", render: (r) => <StatusBadge status={r.status} /> },
              { key: "paid", header: "支払日時", render: (r) => dateTime(r.paid_at) },
              { key: "next", header: "次の回収の予定", render: (r) => (r.status === "open" ? dateTime(r.next_collection_at) : "—") },
              { key: "attempts", header: "試行", align: "right", render: (r) => r.attempt_count },
            ]}
            empty={{ title: "請求はまだありません" }}
          />
        </Stack>
      )}
    </>
  );
}
