// 契約の一覧（FR-004）：すべての事業者の契約。支払い遅延を先頭に、解約済みは折りたたむ。
import { Alert, Button, Collapse, Group, Paper, Skeleton, Stack, Text, UnstyledButton } from "@mantine/core";
import { useDisclosure } from "@mantine/hooks";
import { IconAlertTriangle, IconChevronDown, IconChevronRight, IconInbox } from "@tabler/icons-react";
import { Link } from "react-router";
import { isOpen, useSubscriptions } from "../../api/queries.ts";
import type { Subscription } from "../../api/types.ts";
import { ErrorBanner, PageHeader, StatusBadge } from "../../components/ui.tsx";
import { priceText, scheduleText, sortSubscriptions, titleOf } from "../../lib/subscription.ts";

function SubscriptionCard({ s }: { s: Subscription }) {
  const t = titleOf(s);
  return (
    <UnstyledButton component={Link} to={`/subscriptions/${s.id}`} data-testid="subscription-card">
      <Paper p="md" style={s.status === "past_due" ? { borderColor: "var(--mantine-color-orange-4)" } : undefined}>
        <Group justify="space-between" wrap="nowrap" align="flex-start">
          <div style={{ minWidth: 0 }}>
            <Text size="xs" c="dimmed">
              {t.tenant}
            </Text>
            <Text fw={600} size="sm" mt={2}>
              {t.name}
            </Text>
            <Text size="sm" mt={4}>
              {priceText(s)}
            </Text>
            <Text size="xs" c={s.status === "past_due" ? "orange.8" : "dimmed"} mt={4}>
              {scheduleText(s)}
            </Text>
          </div>
          <Group gap={4} wrap="nowrap">
            <StatusBadge status={s.status} label={s.scheduled_cancellation && s.status !== "canceled" ? "解約予定" : undefined} />
            <IconChevronRight size={16} color="var(--mantine-color-gray-5)" />
          </Group>
        </Group>
      </Paper>
    </UnstyledButton>
  );
}

export function SubscriptionsPage() {
  const q = useSubscriptions();
  const [showClosed, { toggle }] = useDisclosure(false);
  const all = sortSubscriptions(q.data ?? []);
  const open = all.filter(isOpen);
  const closed = all.filter((s) => !isOpen(s));
  const pastDue = open.filter((s) => s.status === "past_due");
  return (
    <>
      <PageHeader title="契約" description="ご利用中のサービスの契約を、まとめて確認・変更できます。" />
      <ErrorBanner error={q.error} onRetry={() => void q.refetch()} />
      {pastDue.length > 0 && (
        <Alert color="orange" variant="light" icon={<IconAlertTriangle size={18} />} mb="md" title="お支払いが確認できない契約があります" data-testid="past-due-alert">
          <Text size="sm">カードの有効期限が切れていないか確認し、支払方法を変更してください。</Text>
          <Button component={Link} to={`/subscriptions/${pastDue[0]!.id}/payment-method`} size="xs" mt="sm" color="orange">
            支払方法を変更する
          </Button>
        </Alert>
      )}
      {q.isLoading ? (
        <Stack>
          <Skeleton h={96} />
          <Skeleton h={96} />
        </Stack>
      ) : all.length === 0 ? (
        <Paper p="xl">
          <Stack align="center" gap={6}>
            <IconInbox size={32} stroke={1.5} color="var(--mantine-color-gray-5)" />
            <Text fw={600} size="sm">
              契約はまだありません
            </Text>
            <Text size="sm" c="dimmed" ta="center">
              事業者のサイトからお申し込みいただけます。
            </Text>
          </Stack>
        </Paper>
      ) : (
        <Stack gap="sm">
          {open.map((s) => (
            <SubscriptionCard key={s.id} s={s} />
          ))}
          {open.length === 0 && (
            <Text size="sm" c="dimmed">
              ご利用中の契約はありません。
            </Text>
          )}
          {closed.length > 0 && (
            <>
              <Button variant="subtle" color="gray" onClick={toggle} rightSection={<IconChevronDown size={14} style={{ transform: showClosed ? "rotate(180deg)" : undefined }} />} w="fit-content">
                解約済みの契約（{closed.length} 件）
              </Button>
              <Collapse in={showClosed}>
                <Stack gap="sm">
                  {closed.map((s) => (
                    <SubscriptionCard key={s.id} s={s} />
                  ))}
                </Stack>
              </Collapse>
            </>
          )}
        </Stack>
      )}
    </>
  );
}
