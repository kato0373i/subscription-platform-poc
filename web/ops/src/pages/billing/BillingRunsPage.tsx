// 更新処理（FR-008）：課金日を選び、投入の記録と更新の結果の集計を表示する。確認つきで再実行できる（終わっていない更新を再開し、二重に請求しない）。
import { Button, Paper, SimpleGrid, Text, TextInput } from "@mantine/core";
import { IconPlayerPlay } from "@tabler/icons-react";
import { useQuery } from "@tanstack/react-query";
import { useSearchParams } from "react-router";
import { useApi } from "../../api/hooks.ts";
import { useBusinessClock } from "../../api/queries.ts";
import type { BillingRuns } from "../../api/types.ts";
import { ListTable } from "../../components/ListTable.tsx";
import { ErrorBanner, PageHeader, Section, StatusBadge, confirmDanger } from "../../components/ui.tsx";
import { date, dateTime } from "../../lib/format.ts";
import { useWrite } from "../../lib/mutation.ts";

type Run = BillingRuns["runs"][number];
const triggerLabel: Record<string, string> = { scheduled: "定時", rerun: "手動の再実行" };
const runStatus: Record<string, { status: string; label: string }> = {
  dispatching: { status: "running", label: "投入中" },
  dispatched: { status: "completed", label: "投入済み" },
  failed: { status: "failed", label: "失敗" },
};

const outcomeItems: { key: keyof BillingRuns["outcomes"]; label: string; color?: string }[] = [
  { key: "paid", label: "成功" },
  { key: "payment_failed", label: "支払い失敗", color: "orange" },
  { key: "ended", label: "終了（解約など）" },
  { key: "in_progress", label: "処理中", color: "blue" },
  { key: "failed", label: "失敗", color: "red" },
];

export function BillingRunsPage() {
  const api = useApi();
  const clock = useBusinessClock();
  const [params, setParams] = useSearchParams();
  const runDate = params.get("date") ?? (clock.ready ? clock.today : "");
  const q = useQuery({
    queryKey: ["billing-runs", runDate],
    queryFn: () => api.get<BillingRuns>(`/v1/ops/billing-runs/${runDate}`),
    enabled: !!runDate,
    refetchInterval: (s) => (s.state.data?.runs.some((r) => r.status === "dispatching") ? 5000 : false),
  });
  const rerun = useWrite({
    fn: () => api.post(`/v1/ops/billing-runs/${runDate}/rerun`),
    invalidate: [["billing-runs", runDate]],
    success: "再実行を受け付けました",
  });
  const o = q.data?.outcomes;
  return (
    <>
      <PageHeader
        title="更新処理"
        description="課金日ごとの、契約の更新（請求と決済）の投入と結果です。"
        actions={
          <Button
            leftSection={<IconPlayerPlay size={16} />}
            disabled={!runDate}
            loading={rerun.isPending}
            onClick={() =>
              confirmDanger({
                title: "更新処理を再実行",
                message: `${date(runDate)} の更新処理を再実行します。投入済みで終わっていない更新を再開し、まだ投入していない更新を投入します。支払い済みの請求をもう一度請求することはありません。`,
                confirm: "再実行する",
                onConfirm: () => rerun.mutate(undefined),
              })
            }
          >
            再実行
          </Button>
        }
      />
      <TextInput type="date" label="課金日" value={runDate} onChange={(e) => e.currentTarget.value && setParams({ date: e.currentTarget.value })} w={200} mb="lg" />
      <ErrorBanner error={q.error} onRetry={() => void q.refetch()} />
      <Section title="更新の結果">
        <SimpleGrid cols={5}>
          {outcomeItems.map((i) => (
            <Paper key={i.key} p="md" withBorder={false} bg="gray.0">
              <Text size="xs" c="dimmed">
                {i.label}
              </Text>
              <Text fz={22} fw={700} c={o && o[i.key] > 0 ? i.color : undefined} data-testid={`outcome-${i.key}`}>
                {o ? o[i.key] : "—"}
              </Text>
            </Paper>
          ))}
        </SimpleGrid>
      </Section>
      <Text fw={600} mb="sm">
        投入の記録
      </Text>
      <ListTable<Run>
        rows={q.data?.runs}
        loading={q.isLoading}
        rowKey={(r) => r.id}
        columns={[
          { key: "seq", header: "回", render: (r) => `${r.seq} 回目` },
          { key: "trigger", header: "きっかけ", render: (r) => triggerLabel[r.trigger] ?? r.trigger },
          { key: "status", header: "状態", render: (r) => <StatusBadge {...(runStatus[r.status] ?? { status: r.status, label: r.status })} /> },
          { key: "enqueued", header: "投入件数", render: (r) => `${r.enqueued_count} 件`, align: "right" },
          { key: "resume", header: "再開した件数", render: (r) => `${r.resume_count} 件`, align: "right" },
          { key: "started", header: "開始", render: (r) => dateTime(r.started_at) },
          { key: "finished", header: "終了", render: (r) => dateTime(r.finished_at) },
        ]}
        empty={{ title: "この課金日の更新処理はまだ実行されていません" }}
      />
    </>
  );
}
