// 事業者名（事業者の詳細へのリンク）。読めなければ ID の先頭を出す（operator-console §5.2）。
import { Anchor, Text } from "@mantine/core";
import { Link } from "react-router";
import { useTenant } from "../api/queries.ts";

export function TenantName({ id }: { id: string | null | undefined }) {
  const { data } = useTenant(id);
  if (!id) return <Text size="sm" c="dimmed">—</Text>;
  return (
    <Anchor component={Link} to={`/tenants/${id}`} size="sm" onClick={(e) => e.stopPropagation()}>
      {data?.name ?? id.slice(0, 8)}
    </Anchor>
  );
}
