// 共通の部品（事業者コンソールと同じ形。operator-console FR-012・ADR-O01）：ページの見出し、状態のバッジ、ID のコピー、エラーの帯、確認、キーと値。
import { ActionIcon, Alert, Badge, Box, Button, CopyButton, Group, Paper, SimpleGrid, Text, Title, Tooltip } from "@mantine/core";
import { modals } from "@mantine/modals";
import { IconAlertTriangle, IconCheck, IconCopy } from "@tabler/icons-react";
import type { ReactNode } from "react";
import { ApiError } from "../api/client.ts";
import { errorMessage } from "../api/messages.ts";

export function PageHeader({ title, description, actions }: { title: ReactNode; description?: ReactNode; actions?: ReactNode }) {
  return (
    <Group justify="space-between" align="flex-start" mb="lg" gap="sm">
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
  pending_review: { color: "yellow", label: "審査待ち" },
  // 決済の試行
  unknown: { color: "orange", label: "結果不明" },
  pending_async: { color: "blue", label: "確認待ち" },
  // 照合の不整合
  acknowledged: { color: "gray", label: "対応済み" },
  // 一括解約・更新処理
  running: { color: "blue", label: "実行中" },
  completed: { color: "green", label: "完了" },
  // 監査
  denied: { color: "red", label: "拒否" },
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
