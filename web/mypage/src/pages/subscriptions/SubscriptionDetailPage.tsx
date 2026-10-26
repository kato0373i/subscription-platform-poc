// 契約の詳細（FR-005〜007）：内容・予約中の変更・支払方法・請求の履歴・プラン変更・解約。操作は契約の状態で出し分ける（要件 §7）。
import { Alert, Anchor, Button, Group, Modal, Radio, Skeleton, Stack, Text, Title } from "@mantine/core";
import { useDisclosure } from "@mantine/hooks";
import { IconAlertTriangle, IconArrowLeft, IconCalendarEvent, IconInfoCircle } from "@tabler/icons-react";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useParams } from "react-router";
import { useApi } from "../../api/hooks.ts";
import { usePaymentMethods, useSubscription } from "../../api/queries.ts";
import type { Invoice, Page, PlanOption, Subscription } from "../../api/types.ts";
import { intervalLabel } from "../../api/types.ts";
import { Card, ErrorBanner, KeyValue, StatusBadge, confirmDanger } from "../../components/ui.tsx";
import { date, yen } from "../../lib/format.ts";
import { useWrite } from "../../lib/mutation.ts";
import { priceText, titleOf } from "../../lib/subscription.ts";

const invalidate = [["subscription"], ["subscriptions"], ["plan-options"]];

export function SubscriptionDetailPage() {
  const { id } = useParams();
  const q = useSubscription(id);
  const s = q.data;
  return (
    <>
      <Anchor component={Link} to="/" size="sm" c="dimmed">
        <Group gap={4}>
          <IconArrowLeft size={14} /> 契約
        </Group>
      </Anchor>
      <ErrorBanner error={q.error} onRetry={() => void q.refetch()} />
      {!s ? q.isLoading && <Skeleton h={200} mt="md" /> : <Detail s={s} />}
    </>
  );
}

function Detail({ s }: { s: Subscription }) {
  const api = useApi();
  const t = titleOf(s);
  const pms = usePaymentMethods();
  const pm = pms.data?.find((p) => p.id === s.payment_method_id);
  const revokeCancel = useWrite({ fn: () => api.del(`/v1/me/subscriptions/${s.id}/scheduled-cancellation`), invalidate, success: "解約の予約を取り消しました" });
  const revokeChange = useWrite({ fn: () => api.del(`/v1/me/subscriptions/${s.id}/scheduled-plan-change`), invalidate, success: "プラン変更の予約を取り消しました" });
  const open = s.status === "trialing" || s.status === "active";
  return (
    <Stack gap={0} mt="sm">
      <Text size="xs" c="dimmed">
        {t.tenant}
      </Text>
      <Group justify="space-between" align="flex-start" wrap="nowrap" mb="md">
        <Title order={1} fz={20}>
          {t.name}
        </Title>
        <StatusBadge status={s.status} label={s.scheduled_cancellation && s.status !== "canceled" ? "解約予定" : undefined} />
      </Group>

      {s.status === "past_due" && (
        <Alert color="orange" variant="light" icon={<IconAlertTriangle size={18} />} mb="md" title="お支払いが確認できていません">
          <Text size="sm">支払方法を変更すると、その場で未払いの請求をお支払いいただけます。</Text>
          <Button component={Link} to={`/subscriptions/${s.id}/payment-method`} size="xs" mt="sm" color="orange">
            支払方法を変更する
          </Button>
        </Alert>
      )}
      {s.scheduled_cancellation && s.status !== "canceled" && (
        <Alert color="gray" variant="light" icon={<IconCalendarEvent size={18} />} mb="md" title="解約を予約しています" data-testid="cancellation-alert">
          <Text size="sm">
            {date(s.current_period.end)} まで利用できます。{date(s.scheduled_cancellation.effective_date)} に解約されます。
          </Text>
          {s.scheduled_cancellation.revocable && (
            <Button variant="default" size="xs" mt="sm" loading={revokeCancel.isPending} onClick={() => revokeCancel.mutate(undefined)}>
              解約を取り消す
            </Button>
          )}
        </Alert>
      )}
      {s.scheduled_plan_change && (
        <Alert color="blue" variant="light" icon={<IconInfoCircle size={18} />} mb="md" title="プランの変更を予約しています" data-testid="plan-change-alert">
          <PlanChangeText s={s} />
          <Button variant="default" size="xs" mt="sm" loading={revokeChange.isPending} onClick={() => revokeChange.mutate(undefined)}>
            変更を取り消す
          </Button>
        </Alert>
      )}

      <Card title="契約の内容">
        <KeyValue
          items={[
            { label: "料金（税込）", value: priceText(s) },
            ...(s.status !== "canceled" ? [{ label: "現在の期間", value: `${date(s.current_period.start)} 〜 ${date(s.current_period.end)}` }] : []),
            ...(s.next_renewal_date && !s.scheduled_cancellation ? [{ label: "次回の更新日", value: date(s.next_renewal_date) }] : []),
            ...(s.trial ? [{ label: "トライアル", value: `${date(s.trial.start)} 〜 ${date(s.trial.end)}` }] : []),
            ...(s.applied_discount ? [{ label: "割引", value: `あと ${s.applied_discount.remaining_cycles} 回` }] : []),
            ...(s.status === "canceled" ? [{ label: "解約日", value: date(s.canceled_at) }] : []),
            { label: "契約日", value: date(s.created_at) },
          ]}
        />
      </Card>

      {s.status !== "canceled" && (
        <Card
          title="支払方法"
          actions={
            <Anchor component={Link} to={`/subscriptions/${s.id}/payment-method`} size="sm">
              変更
            </Anchor>
          }
        >
          <Text size="sm" data-testid="subscription-card-info">
            {pm ? `${pm.display.brand.toUpperCase()} •••• ${pm.display.last4}（${pm.display.exp_month}/${String(pm.display.exp_year).slice(-2)}）` : "—"}
            {pm?.expired && (
              <Text span c="red" size="xs">
                {" "}
                有効期限切れ
              </Text>
            )}
          </Text>
        </Card>
      )}

      {open && !s.scheduled_cancellation && <PlanChange s={s} />}
      <Invoices id={s.id} />
      {s.status !== "canceled" && !s.scheduled_cancellation && <CancelButton s={s} />}
    </Stack>
  );
}

function PlanChangeText({ s }: { s: Subscription }) {
  const api = useApi();
  const opts = useQuery({ queryKey: ["plan-options", s.id], queryFn: () => api.get<{ data: PlanOption[] }>(`/v1/me/subscriptions/${s.id}/plan-change-options`) });
  const to = opts.data?.data.find((o) => o.plan_id === s.scheduled_plan_change?.plan_id);
  return (
    <Text size="sm">
      {date(s.scheduled_plan_change?.effective_date)} から{to ? `「${to.name}」（${yen(to.amount)}／${intervalLabel[to.billing_interval]}）` : "新しいプラン"}に変わります。
    </Text>
  );
}

function PlanChange({ s }: { s: Subscription }) {
  const api = useApi();
  const [opened, { open, close }] = useDisclosure(false);
  const [choice, setChoice] = useState<string | null>(null);
  const opts = useQuery({ queryKey: ["plan-options", s.id], queryFn: () => api.get<{ data: PlanOption[] }>(`/v1/me/subscriptions/${s.id}/plan-change-options`) });
  const save = useWrite({
    fn: (planId: string) => api.put(`/v1/me/subscriptions/${s.id}/scheduled-plan-change`, { plan_id: planId }),
    invalidate,
    success: "プランの変更を予約しました",
    onSuccess: close,
  });
  const list = (opts.data?.data ?? []).filter((o) => o.plan_id !== s.scheduled_plan_change?.plan_id);
  if (list.length === 0) return null; // 変更先がなければ出さない（FR-007）
  const picked = list.find((o) => o.plan_id === choice);
  return (
    <Card title="プランの変更">
      <Text size="sm" c="dimmed" mb="sm">
        同じサービスの別のプランに、次回の更新日から切り替えられます。日割りの請求はありません。
      </Text>
      <Button variant="default" onClick={open} fullWidth>
        プランを選ぶ
      </Button>
      <Modal opened={opened} onClose={close} title="プランの変更" fullScreen={false}>
        <Radio.Group value={choice} onChange={setChoice}>
          <Stack gap="sm">
            {list.map((o) => (
              <Radio key={o.plan_id} value={o.plan_id} label={`${o.name}（${yen(o.amount)}／${intervalLabel[o.billing_interval]}）`} />
            ))}
          </Stack>
        </Radio.Group>
        {picked && (
          <Alert color="blue" variant="light" mt="md">
            {date(picked.effective_date)} から {yen(picked.amount)}／{intervalLabel[picked.billing_interval]} になります。今日の請求はありません。
          </Alert>
        )}
        <Button mt="md" fullWidth disabled={!picked} loading={save.isPending} onClick={() => picked && save.mutate(picked.plan_id)}>
          変更を予約する
        </Button>
      </Modal>
    </Card>
  );
}

function Invoices({ id }: { id: string }) {
  const api = useApi();
  const q = useQuery({ queryKey: ["invoices", id], queryFn: () => api.get<Page<Invoice>>(`/v1/me/subscriptions/${id}/invoices`, { limit: 24 }) });
  return (
    <Card title="請求の履歴">
      <ErrorBanner error={q.error} />
      {q.isLoading ? (
        <Skeleton h={60} />
      ) : (q.data?.data.length ?? 0) === 0 ? (
        <Text size="sm" c="dimmed">
          請求はまだありません。
        </Text>
      ) : (
        <Stack gap="xs" data-testid="invoices">
          {q.data!.data.map((inv) => (
            <Stack key={inv.id} gap={2} py={6} style={{ borderBottom: "1px solid var(--mantine-color-gray-1)" }}>
              <Group justify="space-between" wrap="nowrap" gap="xs">
                <Text size="sm">
                  {date(inv.period.start)} 〜 {date(inv.period.end)}
                </Text>
                <Text size="sm" fw={600} style={{ whiteSpace: "nowrap" }}>
                  {yen(inv.total)}
                </Text>
              </Group>
              <Group justify="space-between" wrap="nowrap" gap="xs">
                <Text size="xs" c="dimmed">
                  {inv.paid_at ? `${date(inv.paid_at)} にお支払い` : inv.status === "open" ? "未払い" : ""}
                </Text>
                <StatusBadge status={inv.status} />
              </Group>
            </Stack>
          ))}
        </Stack>
      )}
    </Card>
  );
}

function CancelButton({ s }: { s: Subscription }) {
  const api = useApi();
  const cancel = useWrite({ fn: () => api.put(`/v1/me/subscriptions/${s.id}/scheduled-cancellation`), invalidate, success: s.status === "past_due" ? "解約しました" : "解約を予約しました" });
  const message =
    s.status === "past_due" ? (
      <Text size="sm">すぐに解約され、未払いの請求の再請求は止まります。サービスはご利用いただけなくなります。</Text>
    ) : s.status === "trialing" ? (
      <Text size="sm">トライアルの終了（{date(s.trial?.end)}）まで利用でき、料金は一切かかりません。</Text>
    ) : (
      <Text size="sm">
        {date(s.current_period.end)} まで利用でき、{date(s.next_renewal_date)} の更新で請求されずに解約されます。期間の終了まではいつでも取り消せます。
      </Text>
    );
  return (
    <Button
      variant="subtle"
      color="red"
      mt="xs"
      loading={cancel.isPending}
      onClick={() => confirmDanger({ title: "解約しますか？", message, confirm: s.status === "past_due" ? "解約する" : "解約を予約する", onConfirm: () => cancel.mutate(undefined) })}
    >
      この契約を解約する
    </Button>
  );
}
