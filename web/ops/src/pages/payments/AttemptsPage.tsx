// 結果が不明な決済（FR-006）：確定していない試行を依頼の古い順に一覧する。請求の ID を入力して再決済を依頼することもできる（FR-009）。
import { Button, Checkbox, Group } from "@mantine/core";
import { useDisclosure } from "@mantine/hooks";
import { IconCreditCardPay } from "@tabler/icons-react";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useNavigate } from "react-router";
import { useApi } from "../../api/hooks.ts";
import { useBusinessClock } from "../../api/queries.ts";
import { TenantName } from "../../components/TenantName.tsx";
import type { Attempt, Page } from "../../api/types.ts";
import { ListTable, useCursorPager } from "../../components/ListTable.tsx";
import { ErrorBanner, PageHeader, StatusBadge } from "../../components/ui.tsx";
import { dateTime, elapsed, yen } from "../../lib/format.ts";
import { CollectInvoiceModal } from "./CollectInvoiceModal.tsx";

const all = ["unknown", "processing", "pending_async"] as const;
const labels: Record<string, string> = { unknown: "結果不明", processing: "処理中", pending_async: "確認待ち" };

export function AttemptsPage() {
  const api = useApi();
  const navigate = useNavigate();
  const clock = useBusinessClock();
  const pager = useCursorPager();
  const [statuses, setStatuses] = useState<string[]>([...all]);
  const [collecting, collect] = useDisclosure(false);
  const list = useQuery({
    queryKey: ["attempts", statuses, pager.cursor],
    queryFn: () => api.get<Page<Attempt>>("/v1/ops/payment-attempts", { status: statuses, limit: 50, starting_after: pager.cursor }),
    enabled: statuses.length > 0,
  });
  return (
    <>
      <PageHeader
        title="結果が不明な決済"
        description="結果が確定していない決済です（依頼から 30 秒以上たったもの）。決済代行の管理画面で結果を確かめてから、手動で確定します。自動の照合で確定したものは一覧から消えます。"
        actions={
          <Button variant="default" leftSection={<IconCreditCardPay size={16} />} onClick={collect.open}>
            請求の再決済
          </Button>
        }
      />
      <Checkbox.Group
        value={statuses}
        onChange={(v) => {
          pager.reset();
          setStatuses(v);
        }}
        mb="md"
      >
        <Group>
          {all.map((s) => (
            <Checkbox key={s} value={s} label={labels[s]} />
          ))}
        </Group>
      </Checkbox.Group>
      <ErrorBanner error={list.error} onRetry={() => void list.refetch()} />
      <ListTable
        rows={statuses.length ? list.data?.data : []}
        loading={statuses.length > 0 && list.isLoading}
        rowKey={(r) => r.id}
        onRowClick={(r) => navigate(`/payments/attempts/${r.id}`)}
        columns={[
          { key: "tenant", header: "事業者", render: (r) => <TenantName id={r.tenant_id} /> },
          { key: "amount", header: "金額", render: (r) => yen(r.amount), align: "right" },
          { key: "status", header: "状態", render: (r) => <StatusBadge status={r.status} label={labels[r.status]} /> },
          { key: "payable", header: "対象", render: (r) => (r.payable.type === "invoice" ? "請求" : r.payable.type) },
          { key: "requested", header: "依頼日時", render: (r) => dateTime(r.requested_at) },
          { key: "elapsed", header: "経過", render: (r) => elapsed(r.requested_at, clock.now()), align: "right" },
        ]}
        empty={{ title: "結果が不明な決済はありません" }}
        pager={{ page: pager.page, hasMore: !!list.data?.has_more, onNext: () => pager.next(list.data?.next_cursor ?? null), onPrev: pager.prev }}
      />
      <CollectInvoiceModal opened={collecting} onClose={collect.close} />
    </>
  );
}
