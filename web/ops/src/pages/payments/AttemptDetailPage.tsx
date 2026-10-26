// 決済の試行の詳細と手動の確定（FR-006）、請求の再決済（FR-009）。契約者は ID だけを出す（NFR-004）。
import { Alert, Anchor, Button, Checkbox, Group, Modal, Radio, Select, Stack, Textarea } from "@mantine/core";
import { useForm } from "@mantine/form";
import { useDisclosure } from "@mantine/hooks";
import { IconArrowLeft, IconCreditCardPay, IconGavel } from "@tabler/icons-react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { Link, useParams } from "react-router";
import { ApiError } from "../../api/client.ts";
import { useApi } from "../../api/hooks.ts";
import { TenantName } from "../../components/TenantName.tsx";
import type { Attempt } from "../../api/types.ts";
import { CopyText, ErrorBanner, KeyValue, PageHeader, Section, StatusBadge } from "../../components/ui.tsx";
import { dateTime, yen } from "../../lib/format.ts";
import { useWrite } from "../../lib/mutation.ts";
import { CollectInvoiceModal } from "./CollectInvoiceModal.tsx";

const unresolved = ["unknown", "processing", "pending_async"];
const statusLabel: Record<string, string> = { unknown: "結果不明", processing: "処理中", pending_async: "確認待ち", succeeded: "成功", failed: "失敗" };
const resolutionLabel: Record<string, string> = { manual: "手動", reconcile: "自動の照合", provider: "決済代行の応答", webhook: "決済代行の通知" };
const failureCodes = [
  { value: "card_declined", label: "card_declined（カード会社が拒否）" },
  { value: "insufficient_funds", label: "insufficient_funds（残高・限度額の不足）" },
  { value: "card_expired", label: "card_expired（有効期限切れ）" },
  { value: "processing_error", label: "processing_error（決済代行の処理エラー）" },
  { value: "not_processed", label: "not_processed（決済されていない）" },
];

export function AttemptDetailPage() {
  const api = useApi();
  const { attemptId } = useParams();
  const [resolving, resolve] = useDisclosure(false);
  const [collecting, collect] = useDisclosure(false);
  const q = useQuery({ queryKey: ["attempt", attemptId], queryFn: () => api.get<Attempt>(`/v1/ops/payment-attempts/${attemptId}`) });
  const a = q.data;
  return (
    <>
      <Anchor component={Link} to="/payments/attempts" size="sm" mb="xs" display="inline-flex" style={{ alignItems: "center", gap: 4 }}>
        <IconArrowLeft size={14} /> 結果が不明な決済
      </Anchor>
      <PageHeader
        title={a ? `決済 ${yen(a.amount)}` : "決済"}
        description={a && <CopyText value={a.id} />}
        actions={
          a && (
            <>
              {a.payable.type === "invoice" && (
                <Button variant="default" leftSection={<IconCreditCardPay size={16} />} onClick={collect.open}>
                  請求を再決済
                </Button>
              )}
              {unresolved.includes(a.status) && (
                <Button leftSection={<IconGavel size={16} />} onClick={resolve.open}>
                  手動で確定
                </Button>
              )}
            </>
          )
        }
      />
      <ErrorBanner error={q.error} onRetry={() => void q.refetch()} />
      {a && (
        <>
          <Section title="試行">
            <KeyValue
              items={[
                { label: "状態", value: <span data-testid="attempt-status"><StatusBadge status={a.status} label={statusLabel[a.status]} /></span> },
                { label: "事業者", value: <TenantName id={a.tenant_id} /> },
                { label: "金額", value: yen(a.amount) },
                { label: "対象", value: <Group gap={6}>{a.payable.type === "invoice" ? "請求" : a.payable.type}<CopyText value={a.payable.id} maw={200} /></Group> },
                { label: "契約者（ID）", value: <CopyText value={a.customer_account_id} /> },
                { label: "支払い方法", value: `${a.method_type}（${a.payment_method_id.slice(0, 8)}…）` },
                { label: "決済代行", value: a.provider },
                { label: "決済代行の参照（オーダー ID）", value: a.provider_order_ref ? <CopyText value={a.provider_order_ref} /> : "—" },
                { label: "回収の回", value: `${a.collection_seq} 回目` },
                { label: "照会の回数", value: `${a.check_count} 回` },
                { label: "依頼日時", value: dateTime(a.requested_at) },
                { label: "確定日時", value: dateTime(a.finalized_at) },
              ]}
            />
          </Section>
          {(a.failure_code || a.resolution) && (
            <Section title="結果">
              <KeyValue
                items={[
                  { label: "失敗の理由", value: a.failure_code ?? "—" },
                  { label: "再試行できるか", value: a.retryable === null ? "—" : a.retryable ? "できる" : "できない" },
                  { label: "決済代行のコード", value: a.provider_raw_code ?? "—" },
                  { label: "確定の方法", value: a.resolution ? (resolutionLabel[a.resolution] ?? a.resolution) : "—" },
                  { label: "確定した運営者", value: a.resolved_by ?? "—" },
                  { label: "根拠のメモ", value: a.resolve_note ?? "—" },
                ]}
              />
            </Section>
          )}
          <ResolveModal attempt={a} opened={resolving} onClose={resolve.close} />
          <CollectInvoiceModal invoiceId={a.payable.id} opened={collecting} onClose={collect.close} />
        </>
      )}
    </>
  );
}

function ResolveModal({ attempt, opened, onClose }: { attempt: Attempt; opened: boolean; onClose: () => void }) {
  const api = useApi();
  const qc = useQueryClient();
  const [mismatch, setMismatch] = useState(false);
  const form = useForm({
    initialValues: { outcome: "", failure_code: "card_declined", retryable: true, note: "", checked: false, force: false },
    validate: {
      outcome: (v) => (v ? null : "結果を選んでください"),
      note: (v) => (v.trim() ? null : "根拠を入力してください"),
      checked: (v) => (v ? null : "決済代行で確認した結果であることを確かめてください"),
    },
  });
  const save = useWrite({
    fn: (v: typeof form.values) =>
      api.post<Attempt>(`/v1/ops/payment-attempts/${attempt.id}/resolve`, {
        outcome: v.outcome,
        note: v.note.trim(),
        ...(v.outcome === "failed" ? { failure_code: v.failure_code, retryable: v.retryable } : {}),
        ...(v.force ? { force: true } : {}),
      }),
    form,
    invalidate: [["attempt", attempt.id], ["attempts"]],
    success: (r) => `決済を「${statusLabel[r.status]}」として確定しました`,
    onSuccess: () => (form.reset(), setMismatch(false), onClose()),
  });
  const err = save.error instanceof ApiError ? save.error : null;
  useEffect(() => {
    // 開いた後に自動の照合で確定していたら、詳細と一覧を読み直して閉じる（FR-006）
    if (err?.code === "attempt_already_finalized") {
      void qc.invalidateQueries({ queryKey: ["attempt", attempt.id] });
      void qc.invalidateQueries({ queryKey: ["attempts"] });
      onClose();
    }
    if (err?.code === "outcome_mismatch") setMismatch(true);
  }, [err, qc, attempt.id, onClose]);
  return (
    <Modal opened={opened} onClose={onClose} title="決済を手動で確定" size="lg">
      <form onSubmit={form.onSubmit((v) => save.mutate(v))}>
        <Stack>
          <Alert color="yellow" variant="light">
            確定は取り消せません。成功にすると請求は支払い済みに、失敗にすると支払いの失敗（リトライ方針に従う）になります。
          </Alert>
          <Radio.Group label="決済代行で確認した結果" required {...form.getInputProps("outcome")}>
            <Group mt={6}>
              <Radio value="succeeded" label="成功（決済された）" />
              <Radio value="failed" label="失敗（決済されていない）" />
            </Group>
          </Radio.Group>
          {form.values.outcome === "failed" && (
            <Group grow align="flex-start">
              <Select label="失敗の理由" data={failureCodes} allowDeselect={false} {...form.getInputProps("failure_code")} />
              <Checkbox mt={30} label="再試行できる（リトライの対象にする）" {...form.getInputProps("retryable", { type: "checkbox" })} />
            </Group>
          )}
          <Textarea label="根拠のメモ" required autosize minRows={2} maxLength={500} placeholder="例：fincode の管理画面で、オーダー ID の決済が未処理であることを確認" {...form.getInputProps("note")} />
          {mismatch && (
            <Checkbox
              color="red"
              label="決済代行の結果と食い違うが、入力した結果で確定する（理由をメモに書いてください）"
              {...form.getInputProps("force", { type: "checkbox" })}
            />
          )}
          <Checkbox label="決済代行の管理画面などで確認した結果です" {...form.getInputProps("checked", { type: "checkbox" })} />
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>
              キャンセル
            </Button>
            <Button type="submit" loading={save.isPending}>
              確定する
            </Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}
