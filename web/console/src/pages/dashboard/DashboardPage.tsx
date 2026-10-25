// ダッシュボード（console-dashboard 要件 FR-001〜FR-007・設計 §7.1）：KGI（MRR・純増・目標）→ 要対応 → KPI → 推移の順に並べる。
import "@mantine/charts/styles.css";
import { BarChart, LineChart } from "@mantine/charts";
import { Anchor, Badge, Button, Group, Modal, NumberInput, Paper, Progress, Select, SimpleGrid, Skeleton, Stack, Table, Text, Title, UnstyledButton } from "@mantine/core";
import { useForm } from "@mantine/form";
import { useDisclosure } from "@mantine/hooks";
import { IconAlertTriangle, IconArrowDownRight, IconArrowUpRight, IconClockHour4, IconMinus, IconTarget } from "@tabler/icons-react";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import dayjs from "dayjs";
import { useMemo, useState, type ReactNode } from "react";
import { Link, useParams } from "react-router";
import type { ConsoleReportingComponents } from "@sp/api-client";
import { useApi } from "../../api/hooks.ts";
import { useCan } from "../../api/tenant.tsx";
import { CanButton, ErrorBanner, PageHeader, confirmDanger } from "../../components/ui.tsx";
import { compactYen, dateTime, percent, signedYen, yen } from "../../lib/format.ts";
import { useWrite } from "../../lib/mutation.ts";

type Overview = ConsoleReportingComponents["schemas"]["Overview"];
type Kpis = ConsoleReportingComponents["schemas"]["Kpis"];
type Rate = ConsoleReportingComponents["schemas"]["Rate"];

// グラフの色（dataviz の検証済み：単系列はブランドの色 #635bff、2 系列は青・オレンジ）。
const SERIES_BRAND = "brand.6";
const SERIES_1 = "#2a78d6";
const SERIES_2 = "#eb6834";

const monthLabel = (m: string) => `${Number(m.slice(0, 4))}年${Number(m.slice(5, 7))}月`;
const shortMonth = (m: string) => `${Number(m.slice(5, 7))}月`;

function monthOptions(today: string | undefined) {
  const base = dayjs(today ?? undefined).startOf("month");
  return Array.from({ length: 25 }, (_, i) => {
    const m = base.subtract(i, "month").format("YYYY-MM");
    return { value: m, label: i === 0 ? `${monthLabel(m)}（当月）` : monthLabel(m) };
  });
}

export function DashboardPage() {
  const api = useApi();
  const { tenantId } = useParams();
  const [month, setMonth] = useState<string | null>(null);
  const q = useQuery({
    queryKey: ["overview", tenantId, month],
    queryFn: () => api.get<Overview>("/v1/console/reports/overview", { month: month ?? undefined }),
    placeholderData: keepPreviousData,
    refetchInterval: 60_000, // 集計の遅れを考えて 1 分ごとに読み直す（NFR-001）
  });
  const ov = q.data;
  const options = useMemo(() => monthOptions(ov?.today), [ov?.today]);
  return (
    <>
      <PageHeader
        title="ダッシュボード"
        description={ov?.as_of ? `最終更新：${dateTime(ov.as_of)}` : "契約・請求のデータから自動で集計します"}
        actions={
          <Select
            aria-label="表示する月"
            data={options}
            value={month ?? ov?.month ?? null}
            onChange={(v) => setMonth(v === options[0]?.value ? null : v)}
            allowDeselect={false}
            w={200}
            data-testid="month-select"
          />
        }
      />
      <ErrorBanner error={q.error} onRetry={() => void q.refetch()} />
      {!ov ? (
        <Stack>
          <Skeleton h={220} />
          <Skeleton h={120} />
        </Stack>
      ) : (
        <Stack gap="lg">
          <KgiSection ov={ov} />
          <Attention ov={ov} tenantId={tenantId!} />
          <KpiSection current={ov.kpis.current} previous={ov.kpis.previous} />
          <TrendSection ov={ov} />
        </Stack>
      )}
    </>
  );
}

// ---- KGI ----

function KgiSection({ ov }: { ov: Overview }) {
  const k = ov.kgi;
  const diff = k.mrr - k.mrr_start;
  const b = k.breakdown;
  return (
    <Paper p="lg" data-testid="kgi">
      <SimpleGrid cols={{ base: 1, md: 2 }} spacing="xl">
        <Stack gap="md">
          <div>
            <Text size="sm" c="dimmed" fw={500}>
              MRR（月次経常収益）{ov.is_current ? "" : `・${monthLabel(ov.month)}末`}
            </Text>
            <Group align="baseline" gap="sm" mt={4}>
              <Text fz={34} fw={700} lh={1.1} data-testid="kgi-mrr">
                {yen(k.mrr)}
              </Text>
              <DeltaBadge value={diff} format={signedYen} good="up" suffix="前月末から" />
            </Group>
            <Text size="xs" c="dimmed" mt={4}>
              ARR {yen(k.arr)}
            </Text>
          </div>
          <GoalBlock ov={ov} />
          <div>
            <Group justify="space-between" mb={6}>
              <Text size="sm" fw={600}>
                純増 MRR
              </Text>
              <Text size="sm" fw={700} data-testid="kgi-net-new">
                {signedYen(k.net_new_mrr)}
              </Text>
            </Group>
            <Stack gap={4}>
              <BreakdownRow label="新規" value={b.new} sign={1} />
              <BreakdownRow label="増額（プラン変更）" value={b.expansion} sign={1} />
              <BreakdownRow label="減額（プラン変更）" value={b.contraction} sign={-1} />
              <BreakdownRow label="解約" value={b.churned} sign={-1} />
            </Stack>
          </div>
        </Stack>
        <div>
          <Text size="sm" fw={600} mb="xs">
            MRR の推移（12 か月）
          </Text>
          <LineChart
            h={240}
            data={ov.trend.map((t) => ({ month: shortMonth(t.month), MRR: t.mrr }))}
            dataKey="month"
            series={[{ name: "MRR", color: SERIES_BRAND }]}
            curveType="monotone"
            gridAxis="x"
            dotProps={{ r: 4 }}
            strokeWidth={2}
            valueFormatter={(v) => yen(v)}
            yAxisProps={{ tickFormatter: compactYen, width: 56 }}
          />
        </div>
      </SimpleGrid>
    </Paper>
  );
}

function BreakdownRow({ label, value, sign }: { label: string; value: number; sign: 1 | -1 }) {
  return (
    <Group justify="space-between">
      <Text size="sm" c="dimmed">
        {label}
      </Text>
      <Text size="sm" ff="monospace">
        {value === 0 ? "0円" : signedYen(sign * value)}
      </Text>
    </Group>
  );
}

function GoalBlock({ ov }: { ov: Overview }) {
  const goal = ov.kgi.goal;
  const can = useCan("goal.write");
  const [opened, { open, close }] = useDisclosure(false);
  return (
    <Paper p="sm" bg="gray.0" withBorder={false} data-testid="goal">
      {goal ? (
        <>
          <Group justify="space-between" mb={6}>
            <Group gap={6}>
              <IconTarget size={16} />
              <Text size="sm" fw={600}>
                目標 {yen(goal.amount)}
              </Text>
            </Group>
            <Group gap="xs">
              <Text size="sm" fw={700} data-testid="goal-rate">
                {percent(goal.achievement_rate)}
              </Text>
              {can.allowed && (
                <Anchor size="xs" component="button" onClick={open}>
                  変更
                </Anchor>
              )}
            </Group>
          </Group>
          <Progress value={Math.min(100, goal.achievement_rate * 100)} size="md" aria-label="目標の達成率" />
        </>
      ) : (
        <Group justify="space-between">
          <Group gap={6}>
            <IconTarget size={16} color="var(--mantine-color-gray-6)" />
            <Text size="sm" c="dimmed">
              {monthLabel(ov.month)}の目標は未設定です
            </Text>
          </Group>
          <CanButton permission="goal.write" size="xs" variant="light" onClick={open}>
            目標を設定
          </CanButton>
        </Group>
      )}
      <GoalModal ov={ov} opened={opened} onClose={close} />
    </Paper>
  );
}

function GoalModal({ ov, opened, onClose }: { ov: Overview; opened: boolean; onClose: () => void }) {
  const api = useApi();
  const goal = ov.kgi.goal;
  const form = useForm({
    initialValues: { amount: (goal?.amount ?? "") as number | string },
    validate: { amount: (v) => (typeof v === "number" && v >= 1 && v <= 1_000_000_000 ? null : "1 円以上 10 億円以下で入力してください") },
  });
  const save = useWrite({
    fn: (v: typeof form.values) => api.put(`/v1/console/mrr-goals/${ov.month}`, { amount: Number(v.amount) }, goal?.version ?? 0),
    invalidate: [["overview"]],
    success: "目標を保存しました",
    form,
    onSuccess: onClose,
  });
  const remove = useWrite({ fn: () => api.del(`/v1/console/mrr-goals/${ov.month}`), invalidate: [["overview"]], success: "目標を削除しました", onSuccess: onClose });
  return (
    <Modal opened={opened} onClose={onClose} title={`${monthLabel(ov.month)}の MRR の目標`}>
      <form onSubmit={form.onSubmit((v) => save.mutate(v))}>
        <Stack>
          <NumberInput label="目標の MRR" suffix=" 円" thousandSeparator="," min={1} hideControls withAsterisk data-autofocus {...form.getInputProps("amount")} />
          <Text size="xs" c="dimmed">
            ダッシュボードに達成率を表示し、スタッフ全員で共有します。
          </Text>
          <Group justify="space-between">
            {goal ? (
              <Button
                variant="subtle"
                color="red"
                onClick={() => confirmDanger({ title: "目標を削除しますか？", message: "この月の目標を削除します。", confirm: "削除", onConfirm: () => remove.mutate(undefined) })}
              >
                削除
              </Button>
            ) : (
              <span />
            )}
            <Button type="submit" loading={save.isPending}>
              保存
            </Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}

// ---- 要対応 ----

function Attention({ ov, tenantId }: { ov: Overview; tenantId: string }) {
  const a = ov.attention;
  const items = [
    { key: "past_due", label: "支払い遅延の契約", hint: "回収中。リトライ方針に沿って再請求します", count: a.past_due, to: `/t/${tenantId}/subscriptions?status=past_due`, icon: IconAlertTriangle, color: "orange" },
    { key: "trials", label: "7 日以内にトライアルが終わる契約", hint: "有料化の前に案内を送る好機です", count: a.trials_ending_7d, to: `/t/${tenantId}/subscriptions?status=trialing`, icon: IconClockHour4, color: "blue" },
  ];
  return (
    <SimpleGrid cols={{ base: 1, sm: 2 }} spacing="md">
      {items.map((i) => (
        <UnstyledButton key={i.key} component={Link} to={i.to} data-testid={`attention-${i.key}`}>
          <Paper p="md" style={{ height: "100%" }}>
            <Group justify="space-between" wrap="nowrap">
              <Group gap="sm" wrap="nowrap">
                <i.icon size={20} color={`var(--mantine-color-${i.color}-6)`} />
                <div>
                  <Text size="sm" fw={600}>
                    {i.label}
                  </Text>
                  <Text size="xs" c="dimmed">
                    {i.hint}
                  </Text>
                </div>
              </Group>
              <Text fz={24} fw={700} style={{ whiteSpace: "nowrap" }}>
                {i.count}
                <Text span size="sm" c="dimmed" fw={500}>
                  {" "}
                  件
                </Text>
              </Text>
            </Group>
          </Paper>
        </UnstyledButton>
      ))}
    </SimpleGrid>
  );
}

// ---- KPI ----

/** 増減のバッジ。good は良い方向（解約率は down）。色だけに頼らず矢印と符号で示す。 */
function DeltaBadge({ value, format, good, suffix }: { value: number | null; format: (n: number) => string; good: "up" | "down"; suffix?: string }) {
  if (value === null) return null;
  const better = value === 0 ? null : (value > 0) === (good === "up");
  const Icon = value === 0 ? IconMinus : value > 0 ? IconArrowUpRight : IconArrowDownRight;
  return (
    <Badge color={better === null ? "gray" : better ? "green" : "red"} leftSection={<Icon size={12} />} variant="light">
      {format(value)}
      {suffix ? ` ${suffix}` : ""}
    </Badge>
  );
}

const pt = (n: number) => `${n > 0 ? "+" : n < 0 ? "−" : ""}${Math.abs(n * 100).toFixed(1)}pt`;
const cnt = (n: number) => `${n > 0 ? "+" : n < 0 ? "−" : ""}${Math.abs(n).toLocaleString("ja-JP")}`;

function rateSub(r: Rate, unit = "件") {
  return r.denominator === 0 ? "対象なし" : `${r.numerator.toLocaleString("ja-JP")} / ${r.denominator.toLocaleString("ja-JP")} ${unit}`;
}

function rateDiff(c: Rate, p: Rate) {
  return c.value === null || p.value === null ? null : c.value - p.value;
}

interface KpiCardProps {
  testId: string;
  group: string;
  label: string;
  value: ReactNode;
  sub?: ReactNode;
  previous: ReactNode;
  delta: ReactNode;
}

function KpiCard(p: KpiCardProps) {
  return (
    <Paper p={{ base: "sm", sm: "md" }} data-testid={`kpi-${p.testId}`}>
      <Text size="xs" c="dimmed" fw={600} tt="uppercase">
        {p.group}
      </Text>
      <Text size="sm" fw={500} mt={2}>
        {p.label}
      </Text>
      <Text fz={{ base: 20, sm: 24 }} fw={700} mt={6} data-testid={`kpi-${p.testId}-value`}>
        {p.value}
      </Text>
      {p.sub && (
        <Text size="xs" c="dimmed" data-testid={`kpi-${p.testId}-sub`}>
          {p.sub}
        </Text>
      )}
      <Group gap={6} mt="sm" wrap="wrap">
        {p.delta}
        <Text size="xs" c="dimmed">
          前月 {p.previous}
        </Text>
      </Group>
    </Paper>
  );
}

function KpiSection({ current: c, previous: p }: { current: Kpis; previous: Kpis }) {
  return (
    <div>
      <Title order={3} mb="sm">
        KPI
      </Title>
      <SimpleGrid cols={{ base: 2, lg: 4 }} spacing={{ base: "xs", sm: "md" }}>
        <KpiCard
          testId="new-subscriptions"
          group="獲得"
          label="新規契約数"
          value={`${c.new_subscriptions.toLocaleString("ja-JP")} 件`}
          sub={`新規 MRR ${yen(c.new_mrr)}`}
          previous={`${p.new_subscriptions} 件`}
          delta={<DeltaBadge value={c.new_subscriptions - p.new_subscriptions} format={cnt} good="up" />}
        />
        <KpiCard
          testId="churn"
          group="継続"
          label="解約率（契約数）"
          value={percent(c.churn_rate.value)}
          sub={`${rateSub(c.churn_rate)}・解約 ${c.canceled.total} 件（支払い失敗 ${c.canceled.dunning}）`}
          previous={percent(p.churn_rate.value)}
          delta={<DeltaBadge value={rateDiff(c.churn_rate, p.churn_rate)} format={pt} good="down" />}
        />
        <KpiCard
          testId="revenue-churn"
          group="継続"
          label="収益の解約率"
          value={percent(c.revenue_churn_rate.value)}
          sub={c.revenue_churn_rate.denominator === 0 ? "対象なし" : `${yen(c.revenue_churn_rate.numerator)} / ${yen(c.revenue_churn_rate.denominator)}`}
          previous={percent(p.revenue_churn_rate.value)}
          delta={<DeltaBadge value={rateDiff(c.revenue_churn_rate, p.revenue_churn_rate)} format={pt} good="down" />}
        />
        <KpiCard
          testId="trial-conversion"
          group="転換"
          label="トライアル転換率"
          value={percent(c.trial_conversion.value)}
          sub={rateSub(c.trial_conversion)}
          previous={percent(p.trial_conversion.value)}
          delta={<DeltaBadge value={rateDiff(c.trial_conversion, p.trial_conversion)} format={pt} good="up" />}
        />
        <KpiCard
          testId="recovery"
          group="回収"
          label="支払い回復率"
          value={percent(c.recovery.value)}
          sub={rateSub(c.recovery)}
          previous={percent(p.recovery.value)}
          delta={<DeltaBadge value={rateDiff(c.recovery, p.recovery)} format={pt} good="up" />}
        />
        <KpiCard
          testId="revenue"
          group="売上"
          label="入金額"
          value={yen(c.revenue.total)}
          sub={`初回 ${yen(c.revenue.initial)}・更新 ${yen(c.revenue.renewal)}`}
          previous={yen(p.revenue.total)}
          delta={<DeltaBadge value={c.revenue.total - p.revenue.total} format={signedYen} good="up" />}
        />
        <KpiCard
          testId="arpu"
          group="売上"
          label="ARPU（有料契約あたり MRR）"
          value={c.arpu === null ? "—" : yen(c.arpu)}
          sub={`有料契約 ${c.paying.toLocaleString("ja-JP")} 件`}
          previous={p.arpu === null ? "—" : yen(p.arpu)}
          delta={<DeltaBadge value={c.arpu === null || p.arpu === null ? null : c.arpu - p.arpu} format={signedYen} good="up" />}
        />
        <KpiCard
          testId="paying"
          group="状況"
          label="有料契約数"
          value={`${c.paying.toLocaleString("ja-JP")} 件`}
          sub={`トライアル中 ${c.trialing} 件・支払い遅延 ${c.past_due} 件`}
          previous={`${p.paying} 件`}
          delta={<DeltaBadge value={c.paying - p.paying} format={cnt} good="up" />}
        />
      </SimpleGrid>
    </div>
  );
}

// ---- 推移 ----

function TrendSection({ ov }: { ov: Overview }) {
  const [showTable, setShowTable] = useState(false);
  const monthly = ov.trend.map((t) => ({ month: shortMonth(t.month), 新規: t.new_subscriptions, 解約: t.canceled, 入金額: t.revenue }));
  const daily = ov.daily.map((d) => ({ date: `${Number(d.date.slice(8, 10))}日`, MRR: d.mrr }));
  return (
    <div>
      <Group justify="space-between" mb="sm">
        <Title order={3}>推移</Title>
        <Button variant="subtle" size="xs" onClick={() => setShowTable((v) => !v)}>
          {showTable ? "グラフで表示" : "表で表示"}
        </Button>
      </Group>
      {showTable ? (
        <Paper>
          <Table.ScrollContainer minWidth={560}>
            <Table data-testid="trend-table">
              <Table.Thead bg="gray.0">
                <Table.Tr>
                  {["月", "MRR", "純増 MRR", "新規契約数", "解約数", "入金額"].map((h) => (
                    <Table.Th key={h} fz="xs" c="dimmed">
                      {h}
                    </Table.Th>
                  ))}
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {ov.trend.map((t) => (
                  <Table.Tr key={t.month}>
                    <Table.Td>{monthLabel(t.month)}</Table.Td>
                    <Table.Td ff="monospace">{yen(t.mrr)}</Table.Td>
                    <Table.Td ff="monospace">{signedYen(t.net_new_mrr)}</Table.Td>
                    <Table.Td>{t.new_subscriptions}</Table.Td>
                    <Table.Td>{t.canceled}</Table.Td>
                    <Table.Td ff="monospace">{yen(t.revenue)}</Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        </Paper>
      ) : (
        <SimpleGrid cols={{ base: 1, lg: 3 }} spacing="md">
          <ChartCard title={`日ごとの MRR（${monthLabel(ov.month)}）`}>
            <LineChart
              h={200}
              data={daily}
              dataKey="date"
              series={[{ name: "MRR", color: SERIES_BRAND }]}
              curveType="linear"
              gridAxis="x"
              withDots={false}
              strokeWidth={2}
              valueFormatter={(v) => yen(v)}
              yAxisProps={{ tickFormatter: compactYen, width: 56 }}
            />
          </ChartCard>
          <ChartCard title="新規契約数と解約数（12 か月）">
            <BarChart
              h={200}
              data={monthly}
              dataKey="month"
              series={[
                { name: "新規", color: SERIES_1 },
                { name: "解約", color: SERIES_2 },
              ]}
              withLegend
              legendProps={{ verticalAlign: "top", height: 28 }}
              gridAxis="x"
              barProps={{ radius: [4, 4, 0, 0] }}
              valueFormatter={(v) => `${v} 件`}
              yAxisProps={{ allowDecimals: false, width: 48 }}
            />
          </ChartCard>
          <ChartCard title="入金額（12 か月）">
            <BarChart
              h={200}
              data={monthly}
              dataKey="month"
              series={[{ name: "入金額", color: SERIES_BRAND }]}
              gridAxis="x"
              barProps={{ radius: [4, 4, 0, 0] }}
              valueFormatter={(v) => yen(v)}
              yAxisProps={{ tickFormatter: compactYen, width: 56 }}
            />
          </ChartCard>
        </SimpleGrid>
      )}
    </div>
  );
}

function ChartCard({ title, children }: { title: string; children: ReactNode }) {
  return (
    <Paper p="md">
      <Text size="sm" fw={600} mb="sm">
        {title}
      </Text>
      {children}
    </Paper>
  );
}
