// クーポンの一覧・作成・無効化（FR-009）。
import { Button, Checkbox, Drawer, Group, MultiSelect, NumberInput, Radio, SegmentedControl, Stack, Tabs, Text, TextInput } from "@mantine/core";
import { DateTimePicker } from "@mantine/dates";
import { useForm } from "@mantine/form";
import { useDisclosure } from "@mantine/hooks";
import { IconPlus } from "@tabler/icons-react";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useParams } from "react-router";
import { useApi } from "../../api/hooks.ts";
import { useAllPlans } from "../../api/queries.ts";
import type { Coupon, Page } from "../../api/types.ts";
import { ListTable, useCursorPager } from "../../components/ListTable.tsx";
import { CanButton, ErrorBanner, PageHeader, StatusBadge, confirmDanger } from "../../components/ui.tsx";
import { dateTime, yen } from "../../lib/format.ts";
import { useWrite } from "../../lib/mutation.ts";

export function discountLabel(c: Coupon) {
  const v = c.discount.type === "percent" ? `${c.discount.value}% 引き` : `${yen(c.discount.value)}引き`;
  return `${v}・${c.discount.duration_cycles} 回`;
}

export function CouponsPage() {
  const api = useApi();
  const { tenantId } = useParams();
  const [status, setStatus] = useState("active");
  const pager = useCursorPager();
  const [opened, { open, close }] = useDisclosure(false);
  const list = useQuery({
    queryKey: ["coupons", tenantId, status, pager.cursor],
    queryFn: () => api.get<Page<Coupon>>("/v1/console/coupons", { status: status === "all" ? undefined : status, limit: 20, starting_after: pager.cursor }),
  });
  const plans = useAllPlans();
  const deactivate = useWrite({
    fn: (c: Coupon) => api.patch<Coupon>(`/v1/console/coupons/${c.id}`, { status: "inactive" }),
    invalidate: [["coupons"]],
    success: "クーポンを無効にしました",
  });
  const target = (c: Coupon) =>
    c.applies_to === "all_plans" ? "すべてのプラン" : c.plan_ids.map((id) => plans.data?.find((p) => p.id === id)?.name ?? "—").join("、");
  return (
    <>
      <PageHeader
        title="クーポン"
        description="申込のときにコードを入力すると、指定した回数の請求を割り引きます。"
        actions={
          <CanButton permission="coupon.write" leftSection={<IconPlus size={16} />} onClick={open}>
            クーポンを作成
          </CanButton>
        }
      />
      <Tabs value={status} onChange={(v) => (setStatus(v ?? "active"), pager.reset())} mb="md">
        <Tabs.List>
          <Tabs.Tab value="active">有効</Tabs.Tab>
          <Tabs.Tab value="inactive">無効</Tabs.Tab>
          <Tabs.Tab value="all">すべて</Tabs.Tab>
        </Tabs.List>
      </Tabs>
      <ErrorBanner error={list.error} onRetry={() => void list.refetch()} />
      <ListTable
        rows={list.data?.data}
        loading={list.isLoading}
        rowKey={(r) => r.id}
        columns={[
          {
            key: "code",
            header: "コード",
            render: (r) => (
              <div>
                <Text size="sm" fw={600} ff="monospace">
                  {r.code}
                </Text>
                <Text size="xs" c="dimmed">
                  {r.name}
                </Text>
              </div>
            ),
          },
          { key: "discount", header: "割引", render: discountLabel },
          { key: "target", header: "対象", render: (r) => <Text size="sm" lineClamp={1} maw={200}>{target(r)}</Text> },
          { key: "used", header: "利用", align: "right", render: (r) => `${r.redeemed_count}${r.max_redemptions ? ` / ${r.max_redemptions}` : ""}` },
          { key: "expires", header: "有効期限", render: (r) => (r.expires_at ? dateTime(r.expires_at) : "なし") },
          { key: "status", header: "状態", render: (r) => <StatusBadge status={r.status} /> },
          {
            key: "actions",
            header: "",
            align: "right",
            render: (r) =>
              r.status === "active" && (
                <CanButton
                  permission="coupon.write"
                  variant="subtle"
                  color="red"
                  size="xs"
                  onClick={() =>
                    confirmDanger({
                      title: `${r.code} を無効にしますか？`,
                      message: "無効にしたクーポンは元に戻せません。すでに適用中の割引はそのまま続きます。",
                      confirm: "無効にする",
                      onConfirm: () => deactivate.mutate(r),
                    })
                  }
                >
                  無効にする
                </CanButton>
              ),
          },
        ]}
        empty={{ title: "クーポンはありません" }}
        pager={{ page: pager.page, hasMore: !!list.data?.has_more, onNext: () => pager.next(list.data?.next_cursor ?? null), onPrev: pager.prev }}
      />
      <CreateCouponDrawer opened={opened} onClose={close} />
    </>
  );
}

function CreateCouponDrawer({ opened, onClose }: { opened: boolean; onClose: () => void }) {
  const api = useApi();
  const plans = useAllPlans();
  const form = useForm({
    initialValues: {
      code: "",
      name: "",
      type: "percent",
      value: "" as number | string,
      duration_cycles: 1 as number | string,
      applies_to: "all_plans",
      plan_ids: [] as string[],
      expires_at: null as Date | null,
      limited: false,
      max_redemptions: "" as number | string,
    },
    validate: {
      code: (v) => (/^[A-Za-z0-9_-]{3,32}$/.test(v.trim()) ? null : "英数字・_・- の 3〜32 文字で入力してください"),
      name: (v) => (v.trim() ? null : "入力してください"),
      value: (v, all) =>
        typeof v !== "number" ? "入力してください" : all.type === "percent" ? (v >= 1 && v <= 100 ? null : "1〜100% で入力してください") : v >= 1 && v <= 1_000_000 ? null : "1〜1,000,000 円で入力してください",
      duration_cycles: (v) => (typeof v === "number" && v >= 1 && v <= 24 ? null : "1〜24 回で入力してください"),
      plan_ids: (v, all) => (all.applies_to === "selected_plans" && v.length === 0 ? "1 つ以上選んでください" : null),
      expires_at: (v) => (v && v <= new Date() ? "未来の日時を指定してください" : null),
      max_redemptions: (v, all) => (!all.limited || (typeof v === "number" && v >= 1) ? null : "1 以上で入力してください"),
    },
  });
  const save = useWrite({
    fn: (v: typeof form.values) =>
      api.post<Coupon>("/v1/console/coupons", {
        code: v.code.trim().toUpperCase(),
        name: v.name.trim(),
        discount: { type: v.type, value: Number(v.value), duration_cycles: Number(v.duration_cycles) },
        applies_to: v.applies_to,
        plan_ids: v.applies_to === "selected_plans" ? v.plan_ids : [],
        expires_at: v.expires_at ? v.expires_at.toISOString() : null,
        max_redemptions: v.limited ? Number(v.max_redemptions) : null,
      }),
    invalidate: [["coupons"]],
    success: "クーポンを作成しました",
    form,
    fieldMap: { "discount.value": "value", "discount.duration_cycles": "duration_cycles", "discount.type": "type" },
    onSuccess: () => (form.reset(), onClose()),
  });
  return (
    <Drawer opened={opened} onClose={onClose} title="クーポンを作成" position="right">
      <form onSubmit={form.onSubmit((v) => save.mutate(v))}>
        <Stack>
          <TextInput label="コード" placeholder="例：WELCOME10" description="申込のときに入力するコード（大文字にそろえます）" withAsterisk {...form.getInputProps("code")} />
          <TextInput label="名前" placeholder="例：新規向け 10% オフ" withAsterisk {...form.getInputProps("name")} />
          <div>
            <Text size="sm" fw={500} mb={4}>
              割引の種類
            </Text>
            <SegmentedControl data={[{ value: "percent", label: "割合" }, { value: "amount", label: "金額" }]} {...form.getInputProps("type")} />
          </div>
          <NumberInput
            label={form.values.type === "percent" ? "割引率" : "割引額"}
            suffix={form.values.type === "percent" ? " %" : " 円"}
            thousandSeparator=","
            hideControls
            withAsterisk
            {...form.getInputProps("value")}
          />
          <NumberInput label="適用する回数" suffix=" 回" min={1} max={24} description="最初の請求から何回分を割り引くか" {...form.getInputProps("duration_cycles")} />
          <Radio.Group label="対象のプラン" {...form.getInputProps("applies_to")}>
            <Stack gap={6} mt={6}>
              <Radio value="all_plans" label="すべてのプラン" />
              <Radio value="selected_plans" label="選んだプランだけ" />
            </Stack>
          </Radio.Group>
          {form.values.applies_to === "selected_plans" && (
            <MultiSelect data={plans.data?.map((p) => ({ value: p.id, label: p.name })) ?? []} placeholder="プランを選ぶ" searchable {...form.getInputProps("plan_ids")} />
          )}
          <DateTimePicker label="有効期限" placeholder="なし" clearable valueFormat="YYYY/MM/DD HH:mm" minDate={new Date()} {...form.getInputProps("expires_at")} />
          <Checkbox label="利用回数の上限を設ける" {...form.getInputProps("limited", { type: "checkbox" })} />
          {form.values.limited && <NumberInput label="上限" suffix=" 回" min={1} max={1_000_000} {...form.getInputProps("max_redemptions")} />}
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
