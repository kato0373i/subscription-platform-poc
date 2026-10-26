// 照合の不整合（FR-007）：営業日・状態で絞り込み、詳細を開いて、メモを入力して対応済みにする。メモは監査ログから読む（ADR-O02）。
import { Anchor, Button, Code, Drawer, Group, SegmentedControl, Stack, Text, TextInput, Textarea } from "@mantine/core";
import { useForm } from "@mantine/form";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useSearchParams } from "react-router";
import { useApi } from "../../api/hooks.ts";
import { TenantName } from "../../components/TenantName.tsx";
import type { AuditRecord, Discrepancy, Page } from "../../api/types.ts";
import { ListTable, useCursorPager } from "../../components/ListTable.tsx";
import { CopyText, ErrorBanner, KeyValue, PageHeader, StatusBadge } from "../../components/ui.tsx";
import { date, dateTime } from "../../lib/format.ts";
import { useWrite } from "../../lib/mutation.ts";

export const kindLabel: Record<string, string> = {
  provider_succeeded_local_not: "決済代行は成功・自社は未確定",
  local_succeeded_provider_not: "自社は成功・決済代行に記録なし",
  amount_mismatch: "金額の不一致",
  orphan_provider_charge: "自社に試行がない決済",
  conflicting_final_outcome: "確定した結果の食い違い",
};
const statusLabel = (s: string) => (s === "open" ? "未対応" : "対応済み");

export function ReconciliationPage() {
  const api = useApi();
  const [params, setParams] = useSearchParams();
  const status = params.get("status") ?? "open";
  const businessDate = params.get("business_date") ?? "";
  const pager = useCursorPager();
  const [selected, setSelected] = useState<Discrepancy | null>(null);
  const list = useQuery({
    queryKey: ["reconciliation", status, businessDate, pager.cursor],
    queryFn: () =>
      api.get<Page<Discrepancy>>("/v1/ops/reconciliation-reports", { status: status || undefined, business_date: businessDate || undefined, limit: 50, starting_after: pager.cursor }),
  });
  const setFilter = (k: string, v: string) => {
    pager.reset();
    const next = new URLSearchParams(params);
    next.set(k, v);
    setParams(next);
  };
  return (
    <>
      <PageHeader title="照合の不整合" description="日次の照合で見つかった、決済代行と自社の記録の食い違いです。調査を終えたら、メモを残して対応済みにします。" />
      <Group mb="md" align="flex-end">
        <SegmentedControl
          value={status}
          data={[
            { value: "open", label: "未対応" },
            { value: "acknowledged", label: "対応済み" },
            { value: "", label: "すべて" },
          ]}
          onChange={(v) => setFilter("status", v)}
        />
        <TextInput type="date" label="営業日" value={businessDate} onChange={(e) => setFilter("business_date", e.currentTarget.value)} w={180} />
      </Group>
      <ErrorBanner error={list.error} onRetry={() => void list.refetch()} />
      <ListTable
        rows={list.data?.data}
        loading={list.isLoading}
        rowKey={(r) => r.id}
        onRowClick={setSelected}
        columns={[
          { key: "date", header: "営業日", render: (r) => date(r.business_date) },
          { key: "kind", header: "種類", render: (r) => kindLabel[r.kind] ?? r.kind },
          { key: "tenant", header: "事業者", render: (r) => <TenantName id={r.tenant_id} /> },
          { key: "ref", header: "決済代行の参照", render: (r) => <Text size="sm" ff="monospace">{r.provider_order_ref}</Text> },
          { key: "status", header: "状態", render: (r) => <StatusBadge status={r.status} label={statusLabel(r.status)} /> },
          { key: "detected", header: "検出日時", render: (r) => dateTime(r.detected_at) },
        ]}
        empty={{ title: "該当する不整合はありません" }}
        pager={{ page: pager.page, hasMore: !!list.data?.has_more, onNext: () => pager.next(list.data?.next_cursor ?? null), onPrev: pager.prev }}
      />
      <Drawer opened={!!selected} onClose={() => setSelected(null)} title="照合の不整合" size="lg">
        {selected && <DiscrepancyDetail d={selected} onDone={() => setSelected(null)} />}
      </Drawer>
    </>
  );
}

function DiscrepancyDetail({ d, onDone }: { d: Discrepancy; onDone: () => void }) {
  const api = useApi();
  const form = useForm({ initialValues: { note: "" }, validate: { note: (v) => (v.trim() ? null : "入力してください") } });
  const ack = useWrite({
    fn: (v: { note: string }) => api.post(`/v1/ops/reconciliation-reports/${d.id}/acknowledge`, { note: v.note.trim() }),
    form,
    invalidate: [["reconciliation"]],
    success: "対応済みにしました",
    onSuccess: onDone,
  });
  // 対応済みのメモ・運営者・日時は監査ログにある（ADR-O02）
  const audit = useQuery({
    queryKey: ["audit", "reconciliation.acknowledge", d.id],
    queryFn: () => api.get<Page<AuditRecord>>("/v1/ops/audit-records", { action: "reconciliation.acknowledge", occurred_from: d.detected_at, limit: 100 }),
    enabled: d.status === "acknowledged",
    select: (p) => p.data.find((r) => r.target.id === d.id && r.outcome === "succeeded"),
  });
  return (
    <Stack>
      <KeyValue
        items={[
          { label: "種類", value: kindLabel[d.kind] ?? d.kind },
          { label: "状態", value: <StatusBadge status={d.status} label={statusLabel(d.status)} /> },
          { label: "営業日", value: date(d.business_date) },
          { label: "決済代行", value: d.provider },
          { label: "事業者", value: <TenantName id={d.tenant_id} /> },
          { label: "決済代行の参照", value: <CopyText value={d.provider_order_ref} /> },
          {
            label: "関連する決済",
            value: d.attempt_id ? (
              <Anchor component={Link} to={`/payments/attempts/${d.attempt_id}`} size="sm">
                試行の詳細を開く
              </Anchor>
            ) : (
              "—"
            ),
          },
          { label: "検出日時", value: dateTime(d.detected_at) },
        ]}
      />
      <div>
        <Text size="xs" c="dimmed" fw={500} mb={4}>
          詳細
        </Text>
        <Code block>{JSON.stringify(d.detail, null, 2)}</Code>
      </div>
      {d.status === "open" ? (
        <form onSubmit={form.onSubmit((v) => ack.mutate(v))}>
          <Stack>
            <Textarea label="対応のメモ" required autosize minRows={3} maxLength={1000} placeholder="例：手動で成功として確定済み。二重請求なし" {...form.getInputProps("note")} />
            <Group justify="flex-end">
              <Button type="submit" loading={ack.isPending}>
                対応済みにする
              </Button>
            </Group>
          </Stack>
        </form>
      ) : (
        <div data-testid="ack-note">
          <Text size="xs" c="dimmed" fw={500} mb={4}>
            対応の記録
          </Text>
          {audit.data ? (
            <Stack gap={2}>
              <Text size="sm">{String(audit.data.after?.note ?? "")}</Text>
              <Text size="xs" c="dimmed">
                {audit.data.actor.id}・{dateTime(audit.data.occurred_at)}
              </Text>
            </Stack>
          ) : (
            <Text size="sm" c="dimmed">
              {audit.isLoading ? "読み込み中…" : "記録が見つかりません"}
            </Text>
          )}
        </div>
      )}
    </Stack>
  );
}
