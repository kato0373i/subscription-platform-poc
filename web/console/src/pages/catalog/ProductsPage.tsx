// 商品の一覧と作成（FR-008）。
import { Button, Modal, Stack, Text, TextInput, Textarea } from "@mantine/core";
import { useForm } from "@mantine/form";
import { useDisclosure } from "@mantine/hooks";
import { IconPlus } from "@tabler/icons-react";
import { useQuery } from "@tanstack/react-query";
import { useNavigate, useParams } from "react-router";
import { useApi } from "../../api/hooks.ts";
import { useAllPlans } from "../../api/queries.ts";
import type { Page, Product } from "../../api/types.ts";
import { ListTable, useCursorPager } from "../../components/ListTable.tsx";
import { CanButton, CopyText, ErrorBanner, PageHeader } from "../../components/ui.tsx";
import { dateTime } from "../../lib/format.ts";
import { useWrite } from "../../lib/mutation.ts";

export function ProductsPage() {
  const api = useApi();
  const navigate = useNavigate();
  const { tenantId } = useParams();
  const pager = useCursorPager();
  const [opened, { open, close }] = useDisclosure(false);
  const list = useQuery({
    queryKey: ["products", tenantId, pager.cursor],
    queryFn: () => api.get<Page<Product>>("/v1/console/products", { limit: 20, starting_after: pager.cursor }),
  });
  const plans = useAllPlans();
  const count = (id: string) => plans.data?.filter((p) => p.product_id === id).length ?? 0;
  return (
    <>
      <PageHeader
        title="商品・プラン"
        description="商品の下に、料金と請求の周期を持つプランを作ります。公開したプランは申込ページから申し込めます。"
        actions={
          <CanButton permission="catalog.write" leftSection={<IconPlus size={16} />} onClick={open}>
            商品を作成
          </CanButton>
        }
      />
      <ErrorBanner error={list.error} onRetry={() => void list.refetch()} />
      <ListTable
        rows={list.data?.data}
        loading={list.isLoading}
        rowKey={(r) => r.id}
        onRowClick={(r) => navigate(r.id)}
        columns={[
          {
            key: "name",
            header: "商品",
            render: (r) => (
              <div>
                <Text size="sm" fw={500}>
                  {r.name}
                </Text>
                {r.description && (
                  <Text size="xs" c="dimmed" lineClamp={1}>
                    {r.description}
                  </Text>
                )}
              </div>
            ),
          },
          { key: "plans", header: "プラン", align: "right", render: (r) => `${count(r.id)} 件` },
          { key: "updated", header: "更新日時", render: (r) => dateTime(r.updated_at) },
          { key: "id", header: "商品 ID", render: (r) => <CopyText value={r.id} maw={120} /> },
        ]}
        empty={{ title: "商品はまだありません", hint: "「商品を作成」から最初の商品を登録しましょう" }}
        pager={{ page: pager.page, hasMore: !!list.data?.has_more, onNext: () => pager.next(list.data?.next_cursor ?? null), onPrev: pager.prev }}
      />
      <CreateProductModal opened={opened} onClose={close} onCreated={(p) => navigate(p.id)} />
    </>
  );
}

function CreateProductModal({ opened, onClose, onCreated }: { opened: boolean; onClose: () => void; onCreated: (p: Product) => void }) {
  const api = useApi();
  const form = useForm({
    initialValues: { name: "", description: "" },
    validate: { name: (v) => (v.trim() ? null : "入力してください") },
  });
  const create = useWrite({
    fn: (v: typeof form.values) => api.post<Product>("/v1/console/products", { name: v.name.trim(), description: v.description.trim() }),
    invalidate: [["products"]],
    success: "商品を作成しました",
    form,
    onSuccess: (p) => (form.reset(), onClose(), onCreated(p)),
  });
  return (
    <Modal opened={opened} onClose={onClose} title="商品を作成">
      <form onSubmit={form.onSubmit((v) => create.mutate(v))}>
        <Stack>
          <TextInput label="商品名" placeholder="例：プレミアム会員" withAsterisk {...form.getInputProps("name")} />
          <Textarea label="説明" autosize minRows={2} {...form.getInputProps("description")} />
          <Button type="submit" loading={create.isPending}>
            作成
          </Button>
        </Stack>
      </form>
    </Modal>
  );
}
