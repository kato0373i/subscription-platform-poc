// 共通の部品（consumer-mypage FR-014）：見出し、状態のバッジ、エラーの帯、確認のダイアログ、キーと値、カード。
import { Alert, Badge, Box, Button, Group, Paper, Stack, Text, Title } from "@mantine/core";
import { modals } from "@mantine/modals";
import { IconAlertTriangle } from "@tabler/icons-react";
import type { ReactNode } from "react";
import { ApiError } from "../api/client.ts";
import { errorMessage } from "../api/messages.ts";

export function PageHeader({ title, description, back }: { title: ReactNode; description?: ReactNode; back?: ReactNode }) {
  return (
    <Box mb="md">
      {back}
      <Title order={1} fz={22} mt={back ? 6 : 0}>
        {title}
      </Title>
      {description && (
        <Text c="dimmed" size="sm" mt={4}>
          {description}
        </Text>
      )}
    </Box>
  );
}

const statuses: Record<string, { color: string; label: string }> = {
  trialing: { color: "blue", label: "トライアル中" },
  active: { color: "green", label: "利用中" },
  past_due: { color: "orange", label: "お支払いの確認待ち" },
  canceled: { color: "gray", label: "解約済み" },
  pending_initial: { color: "gray", label: "お申し込み中" },
  signup_failed: { color: "gray", label: "お申し込み不成立" },
  // 請求
  open: { color: "orange", label: "未払い" },
  processing: { color: "blue", label: "処理中" },
  paid: { color: "green", label: "お支払い済み" },
  uncollectible: { color: "red", label: "回収不能" },
  void: { color: "gray", label: "取り消し" },
};

export function StatusBadge({ status, label }: { status: string; label?: string }) {
  const s = statuses[status] ?? { color: "gray", label: status };
  return (
    <Badge color={s.color} variant="light" radius="sm" tt="none">
      {label ?? s.label}
    </Badge>
  );
}

/** API のエラーを帯で表示する（問い合わせ番号として相関 ID を出す）。 */
export function ErrorBanner({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
  if (!error) return null;
  const corr = error instanceof ApiError ? error.correlationId : null;
  return (
    <Alert color="red" variant="light" icon={<IconAlertTriangle size={18} />} mb="md" title={errorMessage(error)} role="alert">
      <Group justify="space-between">
        {corr ? <Text size="xs" c="dimmed">問い合わせ番号：{corr}</Text> : <span />}
        {onRetry && (
          <Button variant="subtle" size="xs" onClick={onRetry}>
            再読み込み
          </Button>
        )}
      </Group>
    </Alert>
  );
}

/** 取り消せない操作の確認（FR-014）。 */
export function confirmDanger(o: { title: string; message: ReactNode; confirm: string; onConfirm: () => void }) {
  modals.openConfirmModal({
    title: o.title,
    children: <Box fz="sm">{o.message}</Box>,
    labels: { confirm: o.confirm, cancel: "やめる" },
    confirmProps: { color: "red" },
    onConfirm: o.onConfirm,
  });
}

export function KeyValue({ items }: { items: { label: string; value: ReactNode }[] }) {
  return (
    <Stack gap="sm">
      {items.map((i) => (
        <Group key={i.label} justify="space-between" align="flex-start" wrap="nowrap" gap="md">
          <Text size="sm" c="dimmed" style={{ flexShrink: 0 }}>
            {i.label}
          </Text>
          <Box fz="sm" ta="right">
            {i.value}
          </Box>
        </Group>
      ))}
    </Stack>
  );
}

export function Card({ title, children, actions }: { title?: string; children: ReactNode; actions?: ReactNode }) {
  return (
    <Paper p="md" mb="md">
      {(title || actions) && (
        <Group justify="space-between" mb="sm">
          {title && (
            <Text fw={600} size="sm">
              {title}
            </Text>
          )}
          {actions}
        </Group>
      )}
      {children}
    </Paper>
  );
}
