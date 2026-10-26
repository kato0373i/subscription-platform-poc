// ホーム（FR-002）：要対応の件数。各一覧の 1 ページ目（100 件）で数え、満杯なら「100+」（設計 §5.2）。
import { Group, Paper, SimpleGrid, Skeleton, Text, ThemeIcon, UnstyledButton } from "@mantine/core";
import { IconAlertTriangle, IconBuildingStore, IconCalendarRepeat, IconCircleCheck, IconQuestionMark, IconScale } from "@tabler/icons-react";
import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { useNavigate } from "react-router";
import { useApi } from "../api/hooks.ts";
import { useBusinessClock } from "../api/queries.ts";
import type { Attempt, BillingRuns, Discrepancy, Page, Tenant } from "../api/types.ts";
import { ErrorBanner, PageHeader } from "../components/ui.tsx";
import { date } from "../lib/format.ts";

const LIMIT = 100;
const count = (p: Page<unknown> | undefined) => (p ? (p.has_more ? `${LIMIT}+` : String(p.data.length)) : undefined);

function Tile(p: { label: string; value: string | undefined; unit: string; attention: boolean; icon: typeof IconAlertTriangle; to: string; testid: string; sub?: ReactNode }) {
  const navigate = useNavigate();
  const hot = p.attention && p.value !== undefined && p.value !== "0";
  return (
    <UnstyledButton onClick={() => navigate(p.to)} data-testid={p.testid}>
      <Paper p="lg" style={hot ? { borderColor: "var(--mantine-color-orange-4)", background: "var(--mantine-color-orange-0)" } : undefined}>
        <Group justify="space-between" align="flex-start">
          <Text size="sm" c="dimmed" fw={500}>
            {p.label}
          </Text>
          <ThemeIcon variant="light" color={hot ? "orange" : "gray"} size="md">
            <p.icon size={16} />
          </ThemeIcon>
        </Group>
        {p.value === undefined ? (
          <Skeleton h={32} w={60} mt="sm" />
        ) : (
          <Group gap={4} align="baseline" mt="sm">
            <Text fz={28} fw={700} data-testid={`${p.testid}-value`}>
              {p.value}
            </Text>
            <Text size="sm" c="dimmed">
              {p.unit}
            </Text>
          </Group>
        )}
        {p.sub}
      </Paper>
    </UnstyledButton>
  );
}

export function HomePage() {
  const api = useApi();
  const clock = useBusinessClock();
  const today = clock.today;
  const pending = useQuery({ queryKey: ["tenants", "pending_review", "count"], queryFn: () => api.get<Page<Tenant>>("/v1/ops/tenants", { status: "pending_review", limit: LIMIT }) });
  const suspended = useQuery({ queryKey: ["tenants", "suspended", "count"], queryFn: () => api.get<Page<Tenant>>("/v1/ops/tenants", { status: "suspended", limit: LIMIT }) });
  const attempts = useQuery({ queryKey: ["attempts", "count"], queryFn: () => api.get<Page<Attempt>>("/v1/ops/payment-attempts", { limit: LIMIT }) });
  const recon = useQuery({ queryKey: ["reconciliation", "open", "count"], queryFn: () => api.get<Page<Discrepancy>>("/v1/ops/reconciliation-reports", { status: "open", limit: LIMIT }) });
  const runs = useQuery({ queryKey: ["billing-runs", today], queryFn: () => api.get<BillingRuns>(`/v1/ops/billing-runs/${today}`), enabled: clock.ready });
  const error = pending.error ?? suspended.error ?? attempts.error ?? recon.error ?? runs.error;
  const o = runs.data?.outcomes;
  const runProblem = o ? o.failed + o.in_progress : undefined;
  return (
    <>
      <PageHeader title="ホーム" description="対応が必要なものの件数です。クリックすると一覧が開きます。" />
      <ErrorBanner error={error} />
      <SimpleGrid cols={3} spacing="lg">
        <Tile label="審査待ちの事業者" value={count(pending.data)} unit="社" attention icon={IconBuildingStore} to="/tenants?status=pending_review" testid="home-pending" />
        <Tile label="結果が不明な決済" value={count(attempts.data)} unit="件" attention icon={IconQuestionMark} to="/payments/attempts" testid="home-attempts" />
        <Tile label="未対応の照合の不整合" value={count(recon.data)} unit="件" attention icon={IconScale} to="/payments/reconciliation?status=open" testid="home-reconciliation" />
        <Tile
          label={`今日（${date(today)}）の更新処理`}
          value={runProblem === undefined ? undefined : String(runProblem)}
          unit="件が失敗・処理中"
          attention
          icon={runProblem ? IconCalendarRepeat : IconCircleCheck}
          to={`/billing-runs?date=${today}`}
          testid="home-billing"
          sub={
            o && (
              <Text size="xs" c="dimmed" mt={4}>
                成功 {o.paid}・支払い失敗 {o.payment_failed}・終了 {o.ended}・処理中 {o.in_progress}・失敗 {o.failed}
              </Text>
            )
          }
        />
        <Tile label="停止中の事業者" value={count(suspended.data)} unit="社" attention={false} icon={IconBuildingStore} to="/tenants?status=suspended" testid="home-suspended" />
      </SimpleGrid>
    </>
  );
}
