// 契約の支払方法の変更（FR-009）。支払い遅延の契約では、変更と同時に未払いの請求を再決済し、結果を表示する（設計 §6.1・§7）。
import { Alert, Anchor, Button, Divider, Group, Loader, Paper, Radio, Skeleton, Stack, Text, Title } from "@mantine/core";
import { IconArrowLeft, IconCircleCheck, IconCircleX, IconClock } from "@tabler/icons-react";
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useParams } from "react-router";
import { useApi } from "../../api/hooks.ts";
import { usePaymentMethods, useSubscription } from "../../api/queries.ts";
import type { Invoice, Page, PaymentMethod, Subscription } from "../../api/types.ts";
import { CardForm } from "../../components/CardForm.tsx";
import { ErrorBanner, PageHeader } from "../../components/ui.tsx";
import { cardLabel } from "../../lib/card.ts";
import { notifyError, notifyOk } from "../../lib/mutation.ts";
import { titleOf } from "../../lib/subscription.ts";

type Result = "succeeded" | "failed" | "pending" | null;

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

export function ChangePaymentMethodPage() {
  const { id } = useParams();
  const api = useApi();
  const qc = useQueryClient();
  const sub = useSubscription(id);
  const pms = usePaymentMethods();
  const [choice, setChoice] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<Result>(null);
  const s = sub.data;
  const pastDue = s?.status === "past_due";

  const latestInvoice = async () => (await api.get<Page<Invoice>>(`/v1/me/subscriptions/${id}/invoices`, { limit: 1 })).data[0];

  /** 即時の再決済の結果を待つ（試行の回数が増えるか、契約が有効に戻るまで。最大 20 秒）。 */
  const waitRecollection = async (before: number): Promise<Result> => {
    for (let i = 0; i < 20; i++) {
      await sleep(1000);
      const cur = await api.get<Subscription>(`/v1/me/subscriptions/${id}`);
      if (cur.status === "active") return "succeeded";
      if (cur.status === "canceled") return "failed";
      const inv = await latestInvoice();
      if (inv && inv.attempt_count > before && inv.status !== "processing") return inv.status === "paid" ? "succeeded" : "failed";
    }
    return "pending";
  };

  const apply = async (pmId: string) => {
    setBusy(true);
    setResult(null);
    try {
      const before = pastDue ? ((await latestInvoice())?.attempt_count ?? 0) : 0;
      const r = await api.put<{ recollection: "requested" | "none" }>(`/v1/me/subscriptions/${id}/payment-method`, { payment_method_id: pmId });
      if (r.recollection === "requested") {
        setResult("pending");
        setResult(await waitRecollection(before));
      } else {
        notifyOk("支払方法を変更しました");
      }
      await Promise.all([qc.invalidateQueries({ queryKey: ["subscription"] }), qc.invalidateQueries({ queryKey: ["subscriptions"] }), qc.invalidateQueries({ queryKey: ["payment-methods"] }), qc.invalidateQueries({ queryKey: ["invoices"] })]);
    } catch (e) {
      notifyError(e);
    } finally {
      setBusy(false);
    }
  };

  const others = (pms.data ?? []).filter((p) => p.id !== s?.payment_method_id && !p.expired);
  return (
    <>
      <Anchor component={Link} to={`/subscriptions/${id}`} size="sm" c="dimmed">
        <Group gap={4}>
          <IconArrowLeft size={14} /> 契約
        </Group>
      </Anchor>
      <PageHeader title="支払方法の変更" description={s ? `${titleOf(s).tenant}「${titleOf(s).name}」` : undefined} />
      <ErrorBanner error={sub.error ?? pms.error} />
      {result && <ResultPanel result={result} id={id!} />}
      {!s || pms.isLoading ? (
        <Skeleton h={160} />
      ) : s.status === "canceled" ? (
        <Alert color="gray">この契約は解約済みです。</Alert>
      ) : result === "succeeded" ? null : (
        <Paper p="md">
          {(() => {
            const cur = (pms.data ?? []).find((p) => p.id === s.payment_method_id);
            return cur ? (
              <Text size="sm" mb="md" data-testid="current-card">
                現在のカード：{cardLabel(cur)}
                {cur.expired && (
                  <Text span c="red" size="xs">
                    {" "}
                    有効期限切れ
                  </Text>
                )}
              </Text>
            ) : null;
          })()}
          {pastDue && !result && (
            <Alert color="orange" variant="light" mb="md">
              未払いの請求があります。支払方法を変更すると、その場でお支払いいたします。
            </Alert>
          )}
          {others.length > 0 && !adding && (
            <>
              <Radio.Group value={choice} onChange={setChoice} label="登録済みのカード">
                <Stack gap="xs" mt="xs">
                  {others.map((p: PaymentMethod) => (
                    <Radio key={p.id} value={p.id} label={cardLabel(p)} />
                  ))}
                </Stack>
              </Radio.Group>
              <Button mt="md" fullWidth disabled={!choice} loading={busy} onClick={() => choice && void apply(choice)}>
                {pastDue ? "このカードに変更して支払う" : "このカードに変更する"}
              </Button>
              <Divider my="md" label="または" labelPosition="center" />
            </>
          )}
          {adding || others.length === 0 ? (
            <>
              <Text fw={600} size="sm" mb="sm">
                新しいカードを登録
              </Text>
              <CardForm
                submitLabel={pastDue ? "登録して支払う" : "登録して変更する"}
                onCancel={others.length > 0 ? () => setAdding(false) : undefined}
                onRegistered={async (pm) => {
                  setAdding(false);
                  await apply(pm.id);
                }}
              />
            </>
          ) : (
            <Button variant="default" fullWidth onClick={() => setAdding(true)}>
              新しいカードを登録する
            </Button>
          )}
        </Paper>
      )}
    </>
  );
}

function ResultPanel({ result, id }: { result: Exclude<Result, null>; id: string }) {
  const view = {
    succeeded: { icon: IconCircleCheck, color: "green", title: "お支払いが完了しました", text: "契約は引き続きご利用いただけます。" },
    failed: { icon: IconCircleX, color: "red", title: "お支払いができませんでした", text: "別のカードをお試しください。" },
    pending: { icon: IconClock, color: "blue", title: "お支払いを確認しています", text: "しばらくしてから画面を読み直すと、結果が反映されます。" },
  }[result];
  return (
    <Paper p="lg" mb="md" data-testid="recollection-result" data-result={result}>
      <Stack align="center" gap="xs">
        {result === "pending" ? <Loader size="md" /> : <view.icon size={40} color={`var(--mantine-color-${view.color}-6)`} />}
        <Title order={3} fz={18}>
          {view.title}
        </Title>
        <Text size="sm" c="dimmed" ta="center">
          {view.text}
        </Text>
        {result === "succeeded" && (
          <Button component={Link} to={`/subscriptions/${id}`} variant="default" mt="xs">
            契約に戻る
          </Button>
        )}
      </Stack>
    </Paper>
  );
}
