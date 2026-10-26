// 監査ログ（FR-011）：事業者・操作・操作した人・期間で絞り込み（新しい順・ページング）、行を開くと変更の前後・結果・相関 ID・経路を表示する。
import { Button, Code, Drawer, Group, Paper, SimpleGrid, Stack, Text, TextInput } from "@mantine/core";
import { useForm } from "@mantine/form";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useSearchParams } from "react-router";
import { useApi } from "../../api/hooks.ts";
import { TenantName } from "../../components/TenantName.tsx";
import type { AuditRecord, Page } from "../../api/types.ts";
import { ListTable, useCursorPager } from "../../components/ListTable.tsx";
import { CopyText, ErrorBanner, KeyValue, PageHeader, StatusBadge } from "../../components/ui.tsx";
import { dateTime } from "../../lib/format.ts";

const keys = ["tenant_id", "action", "actor_id", "occurred_from", "occurred_to"] as const;
const actorLabel: Record<string, string> = { operator: "運営者", staff: "スタッフ", tenant_api: "API キー", system: "システム" };

/** datetime-local（JST の入力）→ RFC 3339。 */
function toRfc3339(v: string): string | undefined {
  return v ? `${v}:00+09:00` : undefined;
}

export function AuditPage() {
  const api = useApi();
  const [params, setParams] = useSearchParams();
  const pager = useCursorPager();
  const [selected, setSelected] = useState<AuditRecord | null>(null);
  const filter = Object.fromEntries(keys.map((k) => [k, params.get(k) ?? ""])) as Record<(typeof keys)[number], string>;
  const form = useForm({ initialValues: filter });
  const list = useQuery({
    queryKey: ["audit", filter, pager.cursor],
    queryFn: () =>
      api.get<Page<AuditRecord>>("/v1/ops/audit-records", {
        tenant_id: filter.tenant_id || undefined,
        action: filter.action || undefined,
        actor_id: filter.actor_id || undefined,
        occurred_from: toRfc3339(filter.occurred_from),
        occurred_to: toRfc3339(filter.occurred_to),
        limit: 50,
        starting_after: pager.cursor,
      }),
  });
  return (
    <>
      <PageHeader title="監査ログ" description="運営者・スタッフ・API キーの書き込みの操作の記録です（新しい順）。" />
      <Paper p="md" mb="md">
        <form
          onSubmit={form.onSubmit((v) => {
            pager.reset();
            setParams(Object.fromEntries(Object.entries(v).filter(([, x]) => x.trim()).map(([k, x]) => [k, x.trim()])));
          })}
        >
          <SimpleGrid cols={5} spacing="sm">
            <TextInput label="事業者 ID" placeholder="UUID" {...form.getInputProps("tenant_id")} />
            <TextInput label="操作" placeholder="例：tenant.suspend" {...form.getInputProps("action")} />
            <TextInput label="操作した人の ID" {...form.getInputProps("actor_id")} />
            <TextInput type="datetime-local" label="期間（から）" {...form.getInputProps("occurred_from")} />
            <TextInput type="datetime-local" label="期間（まで）" {...form.getInputProps("occurred_to")} />
          </SimpleGrid>
          <Group justify="flex-end" mt="sm" gap="xs">
            <Button
              variant="default"
              onClick={() => {
                form.setValues(Object.fromEntries(keys.map((k) => [k, ""])));
                pager.reset();
                setParams({});
              }}
            >
              クリア
            </Button>
            <Button type="submit">検索</Button>
          </Group>
        </form>
      </Paper>
      <ErrorBanner error={list.error} onRetry={() => void list.refetch()} />
      <ListTable
        rows={list.data?.data}
        loading={list.isLoading}
        rowKey={(r) => r.id}
        onRowClick={setSelected}
        columns={[
          { key: "at", header: "日時", render: (r) => dateTime(r.occurred_at) },
          { key: "action", header: "操作", render: (r) => <Text size="sm" ff="monospace">{r.action}</Text> },
          { key: "actor", header: "操作した人", render: (r) => <Text size="sm">{actorLabel[r.actor.type] ?? r.actor.type}：{r.actor.id}</Text> },
          { key: "tenant", header: "事業者", render: (r) => <TenantName id={r.tenant_id} /> },
          { key: "target", header: "対象", render: (r) => <Text size="sm">{r.target.type}</Text> },
          { key: "outcome", header: "結果", render: (r) => <StatusBadge status={r.outcome} label={r.outcome === "succeeded" ? "成功" : "拒否"} /> },
        ]}
        empty={{ title: "該当する記録はありません" }}
        pager={{ page: pager.page, hasMore: !!list.data?.has_more, onNext: () => pager.next(list.data?.next_cursor ?? null), onPrev: pager.prev }}
      />
      <Drawer opened={!!selected} onClose={() => setSelected(null)} title="監査の記録" size="lg">
        {selected && (
          <Stack>
            <KeyValue
              items={[
                { label: "操作", value: <Text size="sm" ff="monospace">{selected.action}</Text> },
                { label: "結果", value: <StatusBadge status={selected.outcome} label={selected.outcome === "succeeded" ? "成功" : "拒否"} /> },
                { label: "日時", value: dateTime(selected.occurred_at) },
                { label: "操作した人", value: `${actorLabel[selected.actor.type] ?? selected.actor.type}：${selected.actor.id}（${selected.actor.role}）` },
                { label: "事業者", value: <TenantName id={selected.tenant_id} /> },
                { label: "対象", value: <Group gap={6}>{selected.target.type}<CopyText value={selected.target.id} maw={200} /></Group> },
                { label: "経路", value: `${selected.http_method} ${selected.route}` },
                { label: "接続元", value: selected.source_ip ?? "—" },
                { label: "相関 ID", value: <CopyText value={selected.correlation_id} /> },
                { label: "リクエスト ID", value: <CopyText value={selected.request_id} /> },
              ]}
            />
            <Change label="変更前" value={selected.before} />
            <Change label="変更後" value={selected.after} />
          </Stack>
        )}
      </Drawer>
    </>
  );
}

function Change({ label, value }: { label: string; value: Record<string, unknown> | null }) {
  return (
    <div>
      <Text size="xs" c="dimmed" fw={500} mb={4}>
        {label}
      </Text>
      {value ? <Code block data-testid={`audit-${label === "変更前" ? "before" : "after"}`}>{JSON.stringify(value, null, 2)}</Code> : <Text size="sm" c="dimmed">—</Text>}
    </div>
  );
}
