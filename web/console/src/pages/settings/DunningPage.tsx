// 支払い失敗のリトライ方針（FR-011）：最初の失敗から何日後に再請求するか（0〜5 回、1〜60 日、昇順）。If-Match で保存する。
import { ActionIcon, Alert, Button, Group, NumberInput, Paper, Skeleton, Stack, Text, Timeline } from "@mantine/core";
import { useForm } from "@mantine/form";
import { IconInfoCircle, IconPlus, IconTrash } from "@tabler/icons-react";
import { useQuery } from "@tanstack/react-query";
import { useEffect } from "react";
import { useParams } from "react-router";
import { useApi } from "../../api/hooks.ts";
import { useCan } from "../../api/tenant.tsx";
import type { DunningPolicy } from "../../api/types.ts";
import { CanButton, ErrorBanner, PageHeader, Section } from "../../components/ui.tsx";
import { dateTime } from "../../lib/format.ts";
import { useWrite } from "../../lib/mutation.ts";

export function DunningPage() {
  const api = useApi();
  const { tenantId } = useParams();
  const can = useCan("dunning.write");
  const policy = useQuery({ queryKey: ["dunning", tenantId], queryFn: () => api.get<DunningPolicy>("/v1/console/dunning-policy") });
  const form = useForm({
    initialValues: { days: [] as (number | string)[] },
    validate: {
      days: (v) => {
        const n = v.map(Number);
        if (v.some((x) => typeof x !== "number" || x < 1 || x > 60)) return "1〜60 日で入力してください";
        if (n.some((x, i) => i > 0 && x <= n[i - 1]!)) return "前の回より後の日数にしてください";
        return null;
      },
    },
  });
  useEffect(() => {
    if (!policy.data) return;
    form.setInitialValues({ days: policy.data.retry_offsets_days });
    form.setValues({ days: policy.data.retry_offsets_days });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [policy.data]);
  const save = useWrite({
    fn: (v: typeof form.values) => api.put<DunningPolicy>("/v1/console/dunning-policy", { retry_offsets_days: v.days.map(Number) }, policy.data!.version),
    invalidate: [["dunning"]],
    success: "リトライ方針を保存しました",
    form,
    fieldMap: { retry_offsets_days: "days" },
  });
  const days = form.values.days;
  return (
    <>
      <PageHeader title="リトライ方針" description="更新の支払いが失敗したとき、何日後に再び請求するかを決めます。最後の再請求も失敗すると、契約は解約されます。" />
      <ErrorBanner error={policy.error} onRetry={() => void policy.refetch()} />
      {!policy.data ? (
        <Skeleton h={200} />
      ) : (
        <form onSubmit={form.onSubmit((v) => save.mutate(v))}>
          <Section title="再請求のスケジュール">
            <Group align="flex-start" gap={48} wrap="wrap">
              <Stack gap="sm" w={320}>
                {days.map((_, i) => (
                  <Group key={i} gap="xs" wrap="nowrap">
                    <Text size="sm" w={72} c="dimmed">
                      {i + 1} 回目
                    </Text>
                    <NumberInput
                      prefix="最初の失敗から "
                      suffix=" 日後"
                      min={1}
                      max={60}
                      w={200}
                      disabled={!can.allowed}
                      aria-label={`${i + 1} 回目の再請求`}
                      {...form.getInputProps(`days.${i}`)}
                    />
                    <ActionIcon variant="subtle" color="gray" disabled={!can.allowed} onClick={() => form.removeListItem("days", i)} aria-label={`${i + 1} 回目を削除`}>
                      <IconTrash size={16} />
                    </ActionIcon>
                  </Group>
                ))}
                {days.length < 5 && (
                  <Button
                    variant="subtle"
                    size="xs"
                    leftSection={<IconPlus size={14} />}
                    w="fit-content"
                    disabled={!can.allowed}
                    onClick={() => form.insertListItem("days", Math.min(60, Number(days[days.length - 1] ?? 0) + 3))}
                  >
                    再請求を追加
                  </Button>
                )}
                {form.errors.days && (
                  <Text size="xs" c="red">
                    {form.errors.days}
                  </Text>
                )}
              </Stack>
              <Paper p="md" bg="gray.0" miw={260}>
                <Text size="xs" c="dimmed" fw={600} mb="sm">
                  プレビュー
                </Text>
                <Timeline bulletSize={14} lineWidth={2} active={days.length + 1}>
                  <Timeline.Item title="更新日：支払いが失敗">
                    <Text size="xs" c="dimmed">契約は「支払い遅延」になります</Text>
                  </Timeline.Item>
                  {days.map((d, i) => (
                    <Timeline.Item key={i} title={`${d || "?"} 日後：${i + 1} 回目の再請求`} />
                  ))}
                  <Timeline.Item title="すべて失敗したら解約" color="red">
                    <Text size="xs" c="dimmed">
                      {days.length ? "最後の再請求が失敗した時点" : "再請求しないので、最初の失敗で解約"}
                    </Text>
                  </Timeline.Item>
                </Timeline>
              </Paper>
            </Group>
          </Section>
          <Alert color="blue" variant="light" icon={<IconInfoCircle size={18} />} mb="lg">
            変更は、これから支払いが失敗する請求に適用されます。すでに再請求中の請求のスケジュールは変わりません。
            {policy.data.is_default ? "（現在は既定の方針です）" : policy.data.updated_at ? `（最終更新：${dateTime(policy.data.updated_at)}）` : ""}
          </Alert>
          <Group justify="flex-end">
            <Button variant="default" disabled={!form.isDirty()} onClick={() => form.reset()}>
              元に戻す
            </Button>
            <CanButton permission="dunning.write" type="submit" loading={save.isPending} disabled={!form.isDirty()}>
              保存
            </CanButton>
          </Group>
        </form>
      )}
    </>
  );
}
