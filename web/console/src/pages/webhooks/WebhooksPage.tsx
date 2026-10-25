// Webhook の送信先の一覧と作成（FR-012）。作成の直後だけ署名鍵を表示する。
import { Button, Drawer, Group, Modal, Stack, Switch, Tabs, Text, TextInput } from "@mantine/core";
import { useForm } from "@mantine/form";
import { useDisclosure } from "@mantine/hooks";
import { IconPlus } from "@tabler/icons-react";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useNavigate, useParams } from "react-router";
import { useApi } from "../../api/hooks.ts";
import type { Endpoint, Page } from "../../api/types.ts";
import { ListTable, useCursorPager } from "../../components/ListTable.tsx";
import { CanButton, ErrorBanner, PageHeader, SecretReveal, StatusBadge } from "../../components/ui.tsx";
import { dateTime } from "../../lib/format.ts";
import { useWrite } from "../../lib/mutation.ts";
import { DeliveriesTable } from "./DeliveriesTable.tsx";
import { EventTypePicker } from "./EventTypePicker.tsx";

export function WebhooksPage({ tab }: { tab: "endpoints" | "deliveries" }) {
  const navigate = useNavigate();
  const { tenantId } = useParams();
  const [opened, { open, close }] = useDisclosure(false);
  return (
    <>
      <PageHeader
        title="Webhook"
        description="契約や請求の出来事を、自社のシステムに通知します。"
        actions={
          tab === "endpoints" && (
            <CanButton permission="webhook.manage" leftSection={<IconPlus size={16} />} onClick={open}>
              送信先を追加
            </CanButton>
          )
        }
      />
      <Tabs value={tab} onChange={(v) => navigate(`/t/${tenantId}/webhooks${v === "deliveries" ? "/deliveries" : ""}`)} mb="md">
        <Tabs.List>
          <Tabs.Tab value="endpoints">送信先</Tabs.Tab>
          <Tabs.Tab value="deliveries">配信の履歴</Tabs.Tab>
        </Tabs.List>
      </Tabs>
      {tab === "endpoints" ? <EndpointsTable /> : <DeliveriesTable />}
      <CreateEndpointDrawer opened={opened} onClose={close} />
    </>
  );
}

function EndpointsTable() {
  const api = useApi();
  const navigate = useNavigate();
  const { tenantId } = useParams();
  const pager = useCursorPager();
  const list = useQuery({
    queryKey: ["endpoints", tenantId, pager.cursor],
    queryFn: () => api.get<Page<Endpoint>>("/v1/console/webhook-endpoints", { limit: 20, starting_after: pager.cursor }),
  });
  return (
    <>
      <ErrorBanner error={list.error} onRetry={() => void list.refetch()} />
      <ListTable
        rows={list.data?.data}
        loading={list.isLoading}
        rowKey={(r) => r.id}
        onRowClick={(r) => navigate(`/t/${tenantId}/webhooks/endpoints/${r.id}`)}
        columns={[
          {
            key: "url",
            header: "URL",
            render: (r) => (
              <div>
                <Text size="sm" ff="monospace" truncate maw={420}>
                  {r.url}
                </Text>
                {r.description && (
                  <Text size="xs" c="dimmed">
                    {r.description}
                  </Text>
                )}
              </div>
            ),
          },
          { key: "events", header: "イベント", render: (r) => `${r.event_types.length} 種類` },
          { key: "status", header: "状態", render: (r) => <StatusBadge status={r.status} /> },
          { key: "created", header: "作成日時", render: (r) => dateTime(r.created_at) },
        ]}
        empty={{ title: "送信先はまだありません", hint: "「送信先を追加」から、通知を受け取る URL を登録します（10 個まで）" }}
        pager={{ page: pager.page, hasMore: !!list.data?.has_more, onNext: () => pager.next(list.data?.next_cursor ?? null), onPrev: pager.prev }}
      />
    </>
  );
}

function CreateEndpointDrawer({ opened, onClose }: { opened: boolean; onClose: () => void }) {
  const api = useApi();
  const navigate = useNavigate();
  const { tenantId } = useParams();
  const [created, setCreated] = useState<(Endpoint & { secret: string }) | null>(null);
  const form = useForm({
    initialValues: { url: "", description: "", event_types: [] as string[], enabled: true },
    validate: {
      url: (v) => (/^https?:\/\/.+/.test(v.trim()) ? null : "URL を入力してください"),
      event_types: (v) => (v.length ? null : "1 つ以上選んでください"),
    },
  });
  const save = useWrite({
    fn: (v: typeof form.values) =>
      api.post<Endpoint & { secret: string }>("/v1/console/webhook-endpoints", { url: v.url.trim(), description: v.description.trim(), event_types: v.event_types, enabled: v.enabled }),
    invalidate: [["endpoints"]],
    success: "送信先を追加しました",
    form,
    onSuccess: (r) => (form.reset(), onClose(), setCreated(r)),
  });
  return (
    <>
      <Drawer opened={opened} onClose={onClose} title="送信先を追加" position="right">
        <form onSubmit={form.onSubmit((v) => save.mutate(v))}>
          <Stack>
            <TextInput label="URL" placeholder="https://example.com/webhooks" description="インターネットから届く https の URL" withAsterisk {...form.getInputProps("url")} />
            <TextInput label="説明" placeholder="例：本番の会員システム" {...form.getInputProps("description")} />
            <EventTypePicker value={form.values.event_types} onChange={(v) => form.setFieldValue("event_types", v)} error={form.errors.event_types} />
            <Switch label="すぐに送信を始める" {...form.getInputProps("enabled", { type: "checkbox" })} />
            <Group justify="flex-end">
              <Button variant="default" onClick={onClose}>
                キャンセル
              </Button>
              <Button type="submit" loading={save.isPending}>
                追加
              </Button>
            </Group>
          </Stack>
        </form>
      </Drawer>
      <Modal opened={!!created} onClose={() => setCreated(null)} title="署名鍵" closeOnClickOutside={false} size="lg">
        {created && (
          <Stack>
            <Text size="sm">受け取った通知が本物か確かめるために、この署名鍵を自社のシステムに設定してください。</Text>
            <SecretReveal label="署名鍵" value={created.secret} />
            <Group justify="flex-end">
              <Button onClick={() => (setCreated(null), navigate(`/t/${tenantId}/webhooks/endpoints/${created.id}`))}>控えました</Button>
            </Group>
          </Stack>
        )}
      </Modal>
    </>
  );
}
