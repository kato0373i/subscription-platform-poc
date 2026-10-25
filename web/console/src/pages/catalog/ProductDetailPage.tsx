// 商品の詳細（FR-008）：名前・説明の編集（If-Match）と、その商品のプランの一覧・作成。
import { Anchor, Button, Drawer, Group, NumberInput, SegmentedControl, Skeleton, Stack, Text, TextInput, Textarea, Title } from "@mantine/core";
import { useForm } from "@mantine/form";
import { useDisclosure } from "@mantine/hooks";
import { IconArrowLeft, IconPencil, IconPlus } from "@tabler/icons-react";
import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "react-router";
import { useApi } from "../../api/hooks.ts";
import type { Page, Plan, Product } from "../../api/types.ts";
import { intervalLabel } from "../../api/types.ts";
import { ListTable } from "../../components/ListTable.tsx";
import { CanButton, CopyText, ErrorBanner, KeyValue, Section, StatusBadge } from "../../components/ui.tsx";
import { dateTime, yen } from "../../lib/format.ts";
import { useWrite } from "../../lib/mutation.ts";

export function ProductDetailPage() {
  const api = useApi();
  const navigate = useNavigate();
  const { tenantId, productId } = useParams();
  const [editing, edit] = useDisclosure(false);
  const [creating, create] = useDisclosure(false);
  const product = useQuery({ queryKey: ["product", tenantId, productId], queryFn: () => api.get<Product>(`/v1/console/products/${productId}`) });
  const plans = useQuery({
    queryKey: ["plans", tenantId, { product_id: productId }],
    queryFn: () => api.get<Page<Plan>>("/v1/console/plans", { product_id: productId, limit: 100 }),
  });
  const p = product.data;
  return (
    <>
      <Anchor component={Link} to={`/t/${tenantId}/products`} size="sm" c="dimmed">
        <Group gap={4}>
          <IconArrowLeft size={14} /> 商品・プラン
        </Group>
      </Anchor>
      <ErrorBanner error={product.error} onRetry={() => void product.refetch()} />
      {!p ? (
        <Skeleton h={120} mt="md" />
      ) : (
        <Stack gap={0} mt="sm">
          <Group justify="space-between" mb="lg">
            <Title order={1}>{p.name}</Title>
            <CopyText value={p.id} />
          </Group>
          <Section
            title="商品の情報"
            actions={
              <CanButton permission="catalog.write" variant="default" size="xs" leftSection={<IconPencil size={14} />} onClick={edit.open}>
                編集
              </CanButton>
            }
          >
            <KeyValue
              items={[
                { label: "商品名", value: p.name },
                { label: "説明", value: p.description || <Text c="dimmed" size="sm">なし</Text> },
                { label: "作成日時", value: dateTime(p.created_at) },
                { label: "更新日時", value: dateTime(p.updated_at) },
              ]}
            />
          </Section>
          <Group justify="space-between" mb="sm">
            <Title order={3}>プラン</Title>
            <CanButton permission="catalog.write" size="xs" leftSection={<IconPlus size={14} />} onClick={create.open}>
              プランを作成
            </CanButton>
          </Group>
          <ErrorBanner error={plans.error} />
          <ListTable
            rows={plans.data?.data}
            loading={plans.isLoading}
            rowKey={(r) => r.id}
            onRowClick={(r) => navigate(`/t/${tenantId}/plans/${r.id}`)}
            columns={[
              { key: "name", header: "プラン", render: (r) => <Text size="sm" fw={500}>{r.name}</Text> },
              { key: "status", header: "状態", render: (r) => <StatusBadge status={r.status} /> },
              { key: "price", header: "料金", align: "right", render: (r) => `${yen(r.current_price.amount)}／${intervalLabel[r.billing_interval]}` },
              { key: "trial", header: "トライアル", render: (r) => (r.trial_days ? `${r.trial_days} 日` : "なし") },
              { key: "updated", header: "更新日時", render: (r) => dateTime(r.updated_at) },
            ]}
            empty={{ title: "プランはまだありません", hint: "料金と請求の周期を決めてプランを作成します" }}
          />
          <EditProductDrawer product={p} opened={editing} onClose={edit.close} />
          <CreatePlanDrawer productId={p.id} opened={creating} onClose={create.close} onCreated={(pl) => navigate(`/t/${tenantId}/plans/${pl.id}`)} />
        </Stack>
      )}
    </>
  );
}

function EditProductDrawer({ product, opened, onClose }: { product: Product; opened: boolean; onClose: () => void }) {
  const api = useApi();
  const form = useForm({
    initialValues: { name: product.name, description: product.description },
    validate: { name: (v) => (v.trim() ? null : "入力してください") },
  });
  const save = useWrite({
    fn: (v: typeof form.values) => api.patch<Product>(`/v1/console/products/${product.id}`, { name: v.name.trim(), description: v.description.trim() }, product.version),
    invalidate: [["product"], ["products"]],
    success: "商品を更新しました",
    form,
    onSuccess: onClose,
  });
  return (
    <Drawer opened={opened} onClose={onClose} title="商品を編集" position="right" size="md">
      <form onSubmit={form.onSubmit((v) => save.mutate(v))}>
        <Stack>
          <TextInput label="商品名" withAsterisk {...form.getInputProps("name")} />
          <Textarea label="説明" autosize minRows={3} {...form.getInputProps("description")} />
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

function CreatePlanDrawer({ productId, opened, onClose, onCreated }: { productId: string; opened: boolean; onClose: () => void; onCreated: (p: Plan) => void }) {
  const api = useApi();
  const form = useForm({
    initialValues: { name: "", description: "", billing_interval: "month", trial_days: 0 as number | string, amount: "" as number | string },
    validate: {
      name: (v) => (v.trim() ? null : "入力してください"),
      amount: (v) => (typeof v === "number" && v >= 1 && v <= 1000000 ? null : "1〜1,000,000 円で入力してください"),
      trial_days: (v) => (typeof v === "number" && v >= 0 && v <= 365 ? null : "0〜365 日で入力してください"),
    },
  });
  const save = useWrite({
    fn: (v: typeof form.values) =>
      api.post<Plan>("/v1/console/plans", {
        product_id: productId,
        name: v.name.trim(),
        description: v.description.trim(),
        billing_interval: v.billing_interval,
        trial_days: Number(v.trial_days),
        pricing_scheme: "fixed",
        amount: Number(v.amount),
      }),
    invalidate: [["plans"]],
    success: "プランを下書きで作成しました",
    form,
    onSuccess: (p) => (form.reset(), onClose(), onCreated(p)),
  });
  return (
    <Drawer opened={opened} onClose={onClose} title="プランを作成" position="right" size="md">
      <form onSubmit={form.onSubmit((v) => save.mutate(v))}>
        <Stack>
          <TextInput label="プラン名" placeholder="例：月額プラン" withAsterisk {...form.getInputProps("name")} />
          <Textarea label="説明" autosize minRows={2} {...form.getInputProps("description")} />
          <div>
            <Text size="sm" fw={500} mb={4}>
              請求の周期
            </Text>
            <SegmentedControl data={[{ value: "month", label: "毎月" }, { value: "year", label: "毎年" }]} {...form.getInputProps("billing_interval")} />
          </div>
          <NumberInput label="料金（税込）" suffix=" 円" thousandSeparator="," min={1} hideControls withAsterisk {...form.getInputProps("amount")} />
          <NumberInput label="トライアル" suffix=" 日" min={0} max={365} description="0 日でトライアルなし" {...form.getInputProps("trial_days")} />
          <Text size="xs" c="dimmed">
            作成したプランは下書きです。内容を確認してから公開してください。
          </Text>
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>
              キャンセル
            </Button>
            <Button type="submit" loading={save.isPending}>
              作成
            </Button>
          </Group>
        </Stack>
      </form>
    </Drawer>
  );
}
