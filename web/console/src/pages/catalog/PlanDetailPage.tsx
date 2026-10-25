// プランの詳細（FR-008）：属性の編集（If-Match）、価格の改定と履歴、公開・公開停止、申込ページの URL。
import { Alert, Anchor, Button, Drawer, Group, NumberInput, Skeleton, Stack, Text, TextInput, Textarea, Title } from "@mantine/core";
import { useForm } from "@mantine/form";
import { useDisclosure } from "@mantine/hooks";
import { modals } from "@mantine/modals";
import { IconArrowLeft, IconCurrencyYen, IconEye, IconEyeOff, IconInfoCircle, IconPencil } from "@tabler/icons-react";
import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "react-router";
import { useApi } from "../../api/hooks.ts";
import { useTenant } from "../../api/tenant.tsx";
import type { Page, Plan, PriceVersion, Product } from "../../api/types.ts";
import { intervalLabel } from "../../api/types.ts";
import { ListTable } from "../../components/ListTable.tsx";
import { CanButton, CopyText, ErrorBanner, KeyValue, Section, StatusBadge, confirmDanger } from "../../components/ui.tsx";
import { dateTime, yen } from "../../lib/format.ts";
import { useWrite } from "../../lib/mutation.ts";

export function checkoutUrl(planId: string) {
  return `${window.location.origin}/checkout?plan_id=${encodeURIComponent(planId)}`;
}

export function PlanDetailPage() {
  const api = useApi();
  const tenant = useTenant();
  const { tenantId, planId } = useParams();
  const [editing, edit] = useDisclosure(false);
  const [pricing, price] = useDisclosure(false);
  const plan = useQuery({ queryKey: ["plan", tenantId, planId], queryFn: () => api.get<Plan>(`/v1/console/plans/${planId}`) });
  const versions = useQuery({
    queryKey: ["price-versions", tenantId, planId],
    queryFn: () => api.get<Page<PriceVersion>>(`/v1/console/plans/${planId}/price-versions`, { limit: 100 }),
  });
  const p = plan.data;
  const product = useQuery({
    queryKey: ["product", tenantId, p?.product_id],
    queryFn: () => api.get<Product>(`/v1/console/products/${p!.product_id}`),
    enabled: !!p,
  });
  const inv = [["plan"], ["plans"]];
  const publish = useWrite({ fn: () => api.post<Plan>(`/v1/console/plans/${planId}/publish`), invalidate: inv, success: "プランを公開しました" });
  const unpublish = useWrite({ fn: () => api.post<Plan>(`/v1/console/plans/${planId}/unpublish`), invalidate: inv, success: "プランの公開を停止しました" });

  const onPublish = () =>
    modals.openConfirmModal({
      title: "プランを公開しますか？",
      children: <Text size="sm">公開すると、申込ページから申し込めるようになります。</Text>,
      labels: { confirm: "公開する", cancel: "キャンセル" },
      onConfirm: () => publish.mutate(undefined),
    });
  const onUnpublish = () =>
    confirmDanger({
      title: "公開を停止しますか？",
      message: "新しい申込を受け付けなくなります。契約中の人の更新・請求はそのまま続きます。",
      confirm: "公開を停止",
      onConfirm: () => unpublish.mutate(undefined),
    });

  return (
    <>
      <Anchor component={Link} to={p ? `/t/${tenantId}/products/${p.product_id}` : `/t/${tenantId}/products`} size="sm" c="dimmed">
        <Group gap={4}>
          <IconArrowLeft size={14} /> {product.data?.name ?? "商品"}
        </Group>
      </Anchor>
      <ErrorBanner error={plan.error} onRetry={() => void plan.refetch()} />
      {!p ? (
        <Skeleton h={120} mt="md" />
      ) : (
        <Stack gap={0} mt="sm">
          <Group justify="space-between" mb="lg" align="flex-start">
            <div>
              <Group gap="sm">
                <Title order={1}>{p.name}</Title>
                <StatusBadge status={p.status} />
              </Group>
              <Text size="lg" mt={4} fw={500}>
                {yen(p.current_price.amount)}
                <Text span c="dimmed" size="sm">
                  ／{intervalLabel[p.billing_interval]}（税込）
                </Text>
              </Text>
            </div>
            <Group gap="xs">
              {p.status === "published" ? (
                <CanButton permission="catalog.write" variant="default" leftSection={<IconEyeOff size={16} />} onClick={onUnpublish} loading={unpublish.isPending}>
                  公開を停止
                </CanButton>
              ) : (
                <CanButton
                  permission="catalog.write"
                  leftSection={<IconEye size={16} />}
                  onClick={onPublish}
                  loading={publish.isPending}
                  disabled={tenant.tenant_status !== "active"}
                >
                  公開する
                </CanButton>
              )}
            </Group>
          </Group>
          {tenant.tenant_status === "pending_review" && p.status !== "published" && (
            <Alert color="yellow" variant="light" icon={<IconInfoCircle size={18} />} mb="lg">
              事業者の審査が終わるまで、プランは公開できません。
            </Alert>
          )}
          {p.status === "published" && (
            <Section title="申込ページ">
              <Text size="sm" c="dimmed" mb="xs">
                この URL を自社のサイトに載せると、このプランに申し込めます。
              </Text>
              <CopyText value={checkoutUrl(p.id)} maw={560} />
            </Section>
          )}
          <Section
            title="プランの情報"
            actions={
              <CanButton permission="catalog.write" variant="default" size="xs" leftSection={<IconPencil size={14} />} onClick={edit.open}>
                編集
              </CanButton>
            }
          >
            <KeyValue
              items={[
                { label: "プラン名", value: p.name },
                { label: "請求の周期", value: p.billing_interval === "month" ? "毎月" : "毎年" },
                { label: "トライアル", value: p.trial_days ? `${p.trial_days} 日` : "なし" },
                { label: "公開日時", value: dateTime(p.published_at) },
                { label: "説明", value: p.description || <Text c="dimmed" size="sm">なし</Text> },
                { label: "プラン ID", value: <CopyText value={p.id} /> },
              ]}
            />
          </Section>
          <Group justify="space-between" mb="sm">
            <Title order={3}>価格の履歴</Title>
            <CanButton permission="catalog.write" size="xs" variant="default" leftSection={<IconCurrencyYen size={14} />} onClick={price.open}>
              価格を改定
            </CanButton>
          </Group>
          <ErrorBanner error={versions.error} />
          <ListTable
            rows={versions.data?.data}
            loading={versions.isLoading}
            rowKey={(r) => r.price_version_id}
            columns={[
              { key: "no", header: "版", render: (r) => `第 ${r.version_no} 版${r.price_version_id === p.current_price.price_version_id ? "（現在）" : ""}` },
              { key: "amount", header: "金額（税込）", align: "right", render: (r) => <Text ff="monospace" size="sm">{yen(r.amount)}</Text> },
              { key: "tax", header: "税率", render: (r) => `${Math.round(Number(r.tax_rate) * 100)}%` },
              { key: "from", header: "適用開始", render: (r) => dateTime(r.effective_from) },
            ]}
            empty={{ title: "価格の履歴はありません" }}
          />
          <EditPlanDrawer plan={p} opened={editing} onClose={edit.close} />
          <PriceDrawer plan={p} opened={pricing} onClose={price.close} />
        </Stack>
      )}
    </>
  );
}

function EditPlanDrawer({ plan, opened, onClose }: { plan: Plan; opened: boolean; onClose: () => void }) {
  const api = useApi();
  const form = useForm({
    initialValues: { name: plan.name, description: plan.description, trial_days: plan.trial_days as number | string },
    validate: {
      name: (v) => (v.trim() ? null : "入力してください"),
      trial_days: (v) => (typeof v === "number" && v >= 0 && v <= 365 ? null : "0〜365 日で入力してください"),
    },
  });
  const save = useWrite({
    fn: (v: typeof form.values) =>
      api.patch<Plan>(`/v1/console/plans/${plan.id}`, { name: v.name.trim(), description: v.description.trim(), trial_days: Number(v.trial_days) }, plan.version),
    invalidate: [["plan"], ["plans"]],
    success: "プランを更新しました",
    form,
    onSuccess: onClose,
  });
  return (
    <Drawer opened={opened} onClose={onClose} title="プランを編集" position="right" size="md">
      <form onSubmit={form.onSubmit((v) => save.mutate(v))}>
        <Stack>
          <TextInput label="プラン名" withAsterisk {...form.getInputProps("name")} />
          <Textarea label="説明" autosize minRows={3} {...form.getInputProps("description")} />
          <NumberInput label="トライアル" suffix=" 日" min={0} max={365} description="変更は新しい申込から適用されます" {...form.getInputProps("trial_days")} />
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>
              キャンセル
            </Button>
            <Button type="submit" loading={save.isPending}>
              保存
            </Button>
          </Group>
        </Stack>
      </form>
    </Drawer>
  );
}

function PriceDrawer({ plan, opened, onClose }: { plan: Plan; opened: boolean; onClose: () => void }) {
  const api = useApi();
  const form = useForm({
    initialValues: { amount: plan.current_price.amount as number | string },
    validate: { amount: (v) => (typeof v === "number" && v >= 1 && v <= 1_000_000 ? null : "1〜1,000,000 円で入力してください") },
  });
  const save = useWrite({
    fn: (v: typeof form.values) => api.post<PriceVersion>(`/v1/console/plans/${plan.id}/price-versions`, { amount: Number(v.amount) }),
    invalidate: [["plan"], ["plans"], ["price-versions"]],
    success: "価格を改定しました",
    form,
    onSuccess: onClose,
  });
  return (
    <Drawer opened={opened} onClose={onClose} title="価格を改定" position="right" size="md">
      <form onSubmit={form.onSubmit((v) => save.mutate(v))}>
        <Stack>
          <Text size="sm">
            現在の価格：<b>{yen(plan.current_price.amount)}</b>
          </Text>
          <NumberInput label="新しい価格（税込）" suffix=" 円" thousandSeparator="," min={1} max={1_000_000} hideControls withAsterisk {...form.getInputProps("amount")} />
          <Alert color="blue" variant="light" icon={<IconInfoCircle size={18} />}>
            新しい価格は、改定後の新しい申込から適用されます。契約中の人は契約時の価格のまま変わりません。
          </Alert>
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>
              キャンセル
            </Button>
            <Button type="submit" loading={save.isPending}>
              改定する
            </Button>
          </Group>
        </Stack>
      </form>
    </Drawer>
  );
}
