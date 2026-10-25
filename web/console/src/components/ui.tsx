// 共通の部品（設計 §5.5・§5.6、FR-016）：ページの見出し、状態のバッジ、ID のコピー、エラーの帯、権限で無効になるボタン、一度だけ表示する秘密、キーと値。
import { ActionIcon, Alert, Badge, Box, Button, CopyButton, Group, Paper, SimpleGrid, Stack, Text, Title, Tooltip, type ButtonProps } from "@mantine/core";
import { modals } from "@mantine/modals";
import { IconAlertTriangle, IconCheck, IconCopy, IconLock } from "@tabler/icons-react";
import type { ReactNode } from "react";
import { ApiError } from "../api/client.ts";
import { errorMessage } from "../api/messages.ts";
import { useCan } from "../api/tenant.tsx";

export function PageHeader({ title, description, actions }: { title: ReactNode; description?: ReactNode; actions?: ReactNode }) {
  return (
    <Group justify="space-between" align="flex-start" mb="lg" wrap="nowrap">
      <Box>
        <Title order={1}>{title}</Title>
        {description && (
          <Text c="dimmed" size="sm" mt={4}>
            {description}
          </Text>
        )}
      </Box>
      {actions && <Group gap="xs">{actions}</Group>}
    </Group>
  );
}

const colors: Record<string, { color: string; label: string }> = {
  // 契約
  trialing: { color: "blue", label: "トライアル中" },
  active: { color: "green", label: "有効" },
  past_due: { color: "orange", label: "支払い遅延" },
  canceled: { color: "gray", label: "解約済み" },
  pending_initial: { color: "gray", label: "申込中" },
  // 請求
  open: { color: "blue", label: "未払い" },
  processing: { color: "blue", label: "処理中" },
  payment_unknown: { color: "orange", label: "確認中" },
  paid: { color: "green", label: "支払い済み" },
  uncollectible: { color: "red", label: "回収不能" },
  void: { color: "gray", label: "無効" },
  // プラン
  draft: { color: "gray", label: "下書き" },
  published: { color: "green", label: "公開中" },
  unpublished: { color: "gray", label: "公開停止" },
  // 配信
  pending: { color: "blue", label: "送信待ち" },
  succeeded: { color: "green", label: "成功" },
  failed: { color: "red", label: "失敗" },
  abandoned: { color: "gray", label: "中止" },
  // スタッフ・送信先・キー
  invited: { color: "blue", label: "招待中" },
  enabled: { color: "green", label: "有効" },
  disabled: { color: "gray", label: "無効" },
  revoked: { color: "gray", label: "失効" },
  inactive: { color: "gray", label: "無効" },
  suspended: { color: "red", label: "停止中" },
  pending_review: { color: "yellow", label: "審査中" },
};

export function StatusBadge({ status, label }: { status: string; label?: string }) {
  const c = colors[status] ?? { color: "gray", label: status };
  return <Badge color={c.color}>{label ?? c.label}</Badge>;
}

export function CopyText({ value, maw = 260 }: { value: string; maw?: number }) {
  return (
    <Group gap={4} wrap="nowrap">
      <Text ff="monospace" size="sm" truncate maw={maw} title={value}>
        {value}
      </Text>
      <CopyButton value={value} timeout={1500}>
        {({ copied, copy }) => (
          <Tooltip label={copied ? "コピーしました" : "コピー"} withArrow>
            <ActionIcon variant="subtle" color={copied ? "green" : "gray"} size="sm" onClick={(e) => (e.stopPropagation(), copy())} aria-label="コピー">
              {copied ? <IconCheck size={14} /> : <IconCopy size={14} />}
            </ActionIcon>
          </Tooltip>
        )}
      </CopyButton>
    </Group>
  );
}

/** API のエラーを帯で表示する（問い合わせ番号として相関 ID を出す。FR-016）。 */
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

/** 権限がなければ無効にし、理由をツールチップで示すボタン（FR-005）。 */
export function CanButton({ permission, children, ...rest }: ButtonProps & { permission: string; onClick?: () => void; type?: "button" | "submit" }) {
  const { allowed, reason } = useCan(permission);
  const btn = (
    <Button {...rest} disabled={!allowed || rest.disabled} leftSection={!allowed ? <IconLock size={14} /> : rest.leftSection}>
      {children}
    </Button>
  );
  return allowed ? btn : (
    <Tooltip label={reason} withArrow>
      <span>{btn}</span>
    </Tooltip>
  );
}

/** 取り消せない操作の確認ダイアログ（FR-016）。 */
export function confirmDanger(o: { title: string; message: ReactNode; confirm: string; onConfirm: () => void }) {
  modals.openConfirmModal({
    title: o.title,
    children: <Text size="sm">{o.message}</Text>,
    labels: { confirm: o.confirm, cancel: "キャンセル" },
    confirmProps: { color: "red" },
    onConfirm: o.onConfirm,
  });
}

/** 一度だけ表示する秘密の値（署名鍵・API キー。FR-012・FR-014）。 */
export function SecretReveal({ label, value }: { label: string; value: string }) {
  return (
    <Stack gap="xs">
      <Alert color="yellow" variant="light" icon={<IconAlertTriangle size={18} />}>
        この画面を閉じると再表示できません。安全な場所に控えてください。
      </Alert>
      <Paper p="sm" bg="gray.0">
        <Text size="xs" c="dimmed" mb={4}>
          {label}
        </Text>
        <CopyText value={value} maw={420} />
      </Paper>
    </Stack>
  );
}

export function KeyValue({ items }: { items: { label: string; value: ReactNode }[] }) {
  return (
    <SimpleGrid cols={{ base: 1, sm: 2 }} spacing="md" verticalSpacing="sm">
      {items.map((i) => (
        <Box key={i.label}>
          <Text size="xs" c="dimmed" fw={500}>
            {i.label}
          </Text>
          <Box mt={2} fz="sm">
            {i.value}
          </Box>
        </Box>
      ))}
    </SimpleGrid>
  );
}

export function Section({ title, actions, children }: { title: string; actions?: ReactNode; children: ReactNode }) {
  return (
    <Paper p="lg" mb="lg">
      <Group justify="space-between" mb="md">
        <Title order={3}>{title}</Title>
        {actions}
      </Group>
      {children}
    </Paper>
  );
}
