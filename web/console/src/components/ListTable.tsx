// 一覧（設計 §5.6）：罫線の少ない表、行のクリックで詳細へ、空の状態、カーソル方式のページング（前のページはカーソルの履歴で戻る）。
import { Button, Group, Paper, Skeleton, Stack, Table, Text } from "@mantine/core";
import { IconInbox } from "@tabler/icons-react";
import { useState, type ReactNode } from "react";

export interface Column<T> {
  key: string;
  header: string;
  render: (row: T) => ReactNode;
  width?: number | string;
  align?: "left" | "right";
}

export interface Page<T> {
  data: T[];
  has_more: boolean;
  next_cursor: string | null;
}

/** カーソルの履歴（前へ・次へ）。 */
export function useCursorPager() {
  const [stack, setStack] = useState<(string | undefined)[]>([undefined]);
  const cursor = stack[stack.length - 1];
  return {
    cursor,
    page: stack.length,
    next: (c: string | null) => c && setStack((s) => [...s, c]),
    prev: () => setStack((s) => (s.length > 1 ? s.slice(0, -1) : s)),
    reset: () => setStack([undefined]),
  };
}

export function ListTable<T>(p: {
  columns: Column<T>[];
  rows: T[] | undefined;
  rowKey: (r: T) => string;
  loading?: boolean;
  onRowClick?: (r: T) => void;
  empty: { title: string; hint?: ReactNode };
  pager?: { page: number; hasMore: boolean; onNext: () => void; onPrev: () => void };
}) {
  return (
    <Paper style={{ overflow: "hidden" }}>
      <Table.ScrollContainer minWidth={640} type="native">
      <Table highlightOnHover={!!p.onRowClick} verticalSpacing="sm">
        <Table.Thead bg="gray.0">
          <Table.Tr>
            {p.columns.map((c) => (
              <Table.Th key={c.key} w={c.width} ta={c.align} fz="xs" c="dimmed" fw={600}>
                {c.header}
              </Table.Th>
            ))}
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {p.loading &&
            Array.from({ length: 5 }).map((_, i) => (
              <Table.Tr key={i}>
                {p.columns.map((c) => (
                  <Table.Td key={c.key}>
                    <Skeleton h={14} />
                  </Table.Td>
                ))}
              </Table.Tr>
            ))}
          {!p.loading &&
            p.rows?.map((r) => (
              <Table.Tr key={p.rowKey(r)} onClick={p.onRowClick ? () => p.onRowClick!(r) : undefined} style={p.onRowClick ? { cursor: "pointer" } : undefined}>
                {p.columns.map((c) => (
                  <Table.Td key={c.key} ta={c.align}>
                    {c.render(r)}
                  </Table.Td>
                ))}
              </Table.Tr>
            ))}
        </Table.Tbody>
      </Table>
      </Table.ScrollContainer>
      {!p.loading && (p.rows?.length ?? 0) === 0 && (
        <Stack align="center" gap={6} py={48}>
          <IconInbox size={32} stroke={1.5} color="var(--mantine-color-gray-5)" />
          <Text fw={600} size="sm">
            {p.empty.title}
          </Text>
          {p.empty.hint && (
            <Text size="sm" c="dimmed">
              {p.empty.hint}
            </Text>
          )}
        </Stack>
      )}
      {p.pager && (p.pager.page > 1 || p.pager.hasMore) && (
        <Group justify="flex-end" gap="xs" p="sm" style={{ borderTop: "1px solid var(--mantine-color-gray-2)" }}>
          <Button variant="default" size="xs" disabled={p.pager.page <= 1} onClick={p.pager.onPrev}>
            前へ
          </Button>
          <Button variant="default" size="xs" disabled={!p.pager.hasMore} onClick={p.pager.onNext}>
            次へ
          </Button>
        </Group>
      )}
    </Paper>
  );
}
