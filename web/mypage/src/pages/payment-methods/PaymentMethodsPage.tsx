// 支払方法（FR-008）：カードの一覧（使っている契約の数）・登録・削除。使っているカードは削除できない。
import { Alert, Badge, Button, Group, Paper, Skeleton, Stack, Text } from "@mantine/core";
import { IconCreditCard, IconPlus } from "@tabler/icons-react";
import { useState } from "react";
import { Link } from "react-router";
import { useApi } from "../../api/hooks.ts";
import { isOpen, usePaymentMethods, useSubscriptions } from "../../api/queries.ts";
import type { PaymentMethod } from "../../api/types.ts";
import { CardForm } from "../../components/CardForm.tsx";
import { ErrorBanner, PageHeader, confirmDanger } from "../../components/ui.tsx";
import { cardLabel } from "../../lib/card.ts";
import { notifyOk, useWrite } from "../../lib/mutation.ts";
import { titleOf } from "../../lib/subscription.ts";
import { useQueryClient } from "@tanstack/react-query";

export function PaymentMethodsPage() {
  const api = useApi();
  const qc = useQueryClient();
  const pms = usePaymentMethods();
  const subs = useSubscriptions();
  const [adding, setAdding] = useState(false);
  const [blocked, setBlocked] = useState<PaymentMethod | null>(null);
  const remove = useWrite({ fn: (pm: PaymentMethod) => api.del(`/v1/me/payment-methods/${pm.id}`), invalidate: [["payment-methods"]], success: "カードを削除しました" });
  const usedBy = (pm: PaymentMethod) => (subs.data ?? []).filter((s) => isOpen(s) && s.payment_method_id === pm.id);
  return (
    <>
      <PageHeader title="支払方法" description="登録したカードは、別のサービスのお申し込みにも使えます。" />
      <ErrorBanner error={pms.error} onRetry={() => void pms.refetch()} />
      {blocked && (
        <Alert color="orange" variant="light" mb="md" withCloseButton onClose={() => setBlocked(null)} title="このカードは削除できません" data-testid="pm-in-use">
          <Text size="sm">次の契約のお支払いに使われています。先に契約の支払方法を変更してください。</Text>
          <Stack gap={2} mt="xs">
            {usedBy(blocked).map((s) => (
              <Text key={s.id} size="sm" component={Link} to={`/subscriptions/${s.id}/payment-method`} c="brand.7">
                {titleOf(s).tenant}「{titleOf(s).name}」の支払方法を変更する
              </Text>
            ))}
          </Stack>
        </Alert>
      )}
      {pms.isLoading ? (
        <Skeleton h={120} />
      ) : (
        <Stack gap="sm">
          {(pms.data ?? []).map((pm) => {
            const used = usedBy(pm);
            return (
              <Paper key={pm.id} p="md" data-testid="payment-method">
                <Group justify="space-between" wrap="nowrap">
                  <Group gap="sm" wrap="nowrap">
                    <IconCreditCard size={22} stroke={1.5} />
                    <div>
                      <Text size="sm" fw={600}>
                        {cardLabel(pm)}
                      </Text>
                      <Group gap={6} mt={2}>
                        {pm.expired && <Badge color="red" variant="light" size="sm">有効期限切れ</Badge>}
                        <Text size="xs" c="dimmed">
                          {used.length > 0 ? `${used.length} 件の契約で使用中` : "未使用"}
                        </Text>
                      </Group>
                    </div>
                  </Group>
                  <Button
                    variant="subtle"
                    color="red"
                    size="xs"
                    onClick={() =>
                      used.length > 0
                        ? setBlocked(pm)
                        : confirmDanger({ title: "カードを削除しますか？", message: `${cardLabel(pm)} を削除します。`, confirm: "削除する", onConfirm: () => remove.mutate(pm) })
                    }
                  >
                    削除
                  </Button>
                </Group>
              </Paper>
            );
          })}
          {(pms.data ?? []).length === 0 && (
            <Text size="sm" c="dimmed">
              登録しているカードはありません。
            </Text>
          )}
          <Paper p="md">
            {adding ? (
              <CardForm
                submitLabel="登録する"
                onCancel={() => setAdding(false)}
                onRegistered={async () => {
                  setAdding(false);
                  await qc.invalidateQueries({ queryKey: ["payment-methods"] });
                  notifyOk("カードを登録しました");
                }}
              />
            ) : (
              <Button variant="default" fullWidth leftSection={<IconPlus size={16} />} onClick={() => setAdding(true)}>
                カードを登録する
              </Button>
            )}
          </Paper>
        </Stack>
      )}
    </>
  );
}
