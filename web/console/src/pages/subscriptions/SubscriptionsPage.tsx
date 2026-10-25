// 契約の一覧（FR-010）：状態のタブ・プラン・契約者 ID／メールアドレスの検索、新しい順、カーソルのページング。
import { Group, Select, Stack, Tabs, Text, TextInput } from "@mantine/core";
import { useDebouncedValue } from "@mantine/hooks";
import { IconSearch } from "@tabler/icons-react";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useNavigate, useParams, useSearchParams } from "react-router";
import { useApi } from "../../api/hooks.ts";
import { useAllPlans } from "../../api/queries.ts";
import type { Page, Subscription } from "../../api/types.ts";
import { ListTable, useCursorPager } from "../../components/ListTable.tsx";
import { CopyText, ErrorBanner, PageHeader, StatusBadge } from "../../components/ui.tsx";
import { date } from "../../lib/format.ts";

const tabs = [
  { value: "all", label: "すべて" },
  { value: "active", label: "有効" },
  { value: "trialing", label: "トライアル中" },
  { value: "past_due", label: "支払い遅延" },
  { value: "canceled", label: "解約済み" },
];

export function SubscriptionsPage() {
  const api = useApi();
  const navigate = useNavigate();
  const { tenantId } = useParams();
  const [params] = useSearchParams();
  // ダッシュボードの「要対応」から ?status= 付きで来る（console-dashboard FR-006）
  const [status, setStatus] = useState(() => (tabs.some((t) => t.value === params.get("status")) ? params.get("status")! : "all"));
  const [planId, setPlanId] = useState<string | null>(null);
  const [search, setSearch] = useState("");
  const [q] = useDebouncedValue(search.trim(), 400);
  const pager = useCursorPager();
  const plans = useAllPlans();
  const planName = (id: string) => plans.data?.find((p) => p.id === id)?.name ?? "—";
  const query: Record<string, string | undefined> = {
    status: status === "all" ? undefined : status,
    plan_id: planId ?? undefined,
    customer_id: q.startsWith("cus_") ? q : undefined,
    email: q && !q.startsWith("cus_") ? q : undefined,
    starting_after: pager.cursor,
  };
  const list = useQuery({
    queryKey: ["subscriptions", tenantId, query],
    queryFn: () => api.get<Page<Subscription>>("/v1/console/subscriptions", { ...query, limit: 20 }),
    placeholderData: keepPreviousData,
  });
  const reset = () => pager.reset();
  return (
    <>
      <PageHeader title="契約" description="契約の状態・次回の更新日・請求の履歴を確認できます。" />
      <Stack gap="md">
        <Tabs value={status} onChange={(v) => (setStatus(v ?? "all"), reset())}>
          <Tabs.List>
            {tabs.map((t) => (
              <Tabs.Tab key={t.value} value={t.value}>
                {t.label}
              </Tabs.Tab>
            ))}
          </Tabs.List>
        </Tabs>
        <Group>
          <TextInput
            placeholder="メールアドレス または 契約者 ID（cus_…）で検索"
            leftSection={<IconSearch size={16} />}
            value={search}
            onChange={(e) => (setSearch(e.currentTarget.value), reset())}
            w={{ base: "100%", sm: 360 }}
            aria-label="検索"
          />
          <Select
            placeholder="すべてのプラン"
            data={plans.data?.map((p) => ({ value: p.id, label: p.name })) ?? []}
            value={planId}
            onChange={(v) => (setPlanId(v), reset())}
            clearable
            w={{ base: "100%", sm: 240 }}
            aria-label="プラン"
          />
        </Group>
        <ErrorBanner error={list.error} onRetry={() => void list.refetch()} />
        <ListTable
          rows={list.data?.data}
          loading={list.isLoading}
          rowKey={(r) => r.id}
          onRowClick={(r) => navigate(r.id)}
          columns={[
            {
              key: "customer",
              header: "契約者",
              render: (r) => (
                <div>
                  <Text size="sm" fw={500}>
                    {r.customer.withdrawn ? "退会済み" : (r.customer.name ?? r.customer.email ?? "—")}
                  </Text>
                  <Text size="xs" c="dimmed" ff="monospace">
                    {r.customer.withdrawn ? r.customer_id : (r.customer.name ? r.customer.email : r.customer_id)}
                  </Text>
                </div>
              ),
            },
            { key: "status", header: "状態", render: (r) => <StatusBadge status={r.status} /> },
            { key: "plan", header: "プラン", render: (r) => planName(r.plan_id) },
            { key: "next", header: "次回の更新日", render: (r) => date(r.next_renewal_date) },
            { key: "created", header: "開始日", render: (r) => date(r.created_at) },
            { key: "id", header: "契約 ID", render: (r) => <CopyText value={r.id} maw={120} /> },
          ]}
          empty={{ title: "条件に一致する契約はありません", hint: q || status !== "all" || planId ? "検索や絞り込みの条件を変えてください" : "申込を受け付けると、ここに表示されます" }}
          pager={{ page: pager.page, hasMore: !!list.data?.has_more, onNext: () => pager.next(list.data?.next_cursor ?? null), onPrev: pager.prev }}
        />
      </Stack>
    </>
  );
}
