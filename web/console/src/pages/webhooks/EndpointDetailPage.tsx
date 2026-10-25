// 送信先の詳細（FR-012）：編集（If-Match）・有効／無効・署名鍵のローテーション・削除と、この送信先の配信の履歴。
import { Anchor, Button, Drawer, Group, Menu, Modal, Select, Skeleton, Stack, Text, TextInput, Title } from "@mantine/core";
import { useForm } from "@mantine/form";
import { useDisclosure } from "@mantine/hooks";
import { IconArrowLeft, IconDots, IconKey, IconPencil, IconPlayerPause, IconPlayerPlay, IconTrash } from "@tabler/icons-react";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useApi } from "../../api/hooks.ts";
import { useCan } from "../../api/tenant.tsx";
import type { Endpoint } from "../../api/types.ts";
import { CopyText, ErrorBanner, KeyValue, Section, SecretReveal, StatusBadge, confirmDanger } from "../../components/ui.tsx";
import { dateTime } from "../../lib/format.ts";
import { useWrite } from "../../lib/mutation.ts";
import { DeliveriesTable } from "./DeliveriesTable.tsx";
import { EventTypePicker } from "./EventTypePicker.tsx";
import { eventTypes } from "./events.ts";

export function EndpointDetailPage() {
  const api = useApi();
  const navigate = useNavigate();
  const { tenantId, endpointId } = useParams();
  const can = useCan("webhook.manage");
  const [editing, edit] = useDisclosure(false);
  const [rotating, rotate] = useDisclosure(false);
  const ep = useQuery({ queryKey: ["endpoint", tenantId, endpointId], queryFn: () => api.get<Endpoint>(`/v1/console/webhook-endpoints/${endpointId}`) });
  const e = ep.data;
  const inv = [["endpoint"], ["endpoints"]];
  const toggle = useWrite({
    fn: (enabled: boolean) => api.patch<Endpoint>(`/v1/console/webhook-endpoints/${endpointId}`, { enabled }, e!.version),
    invalidate: inv,
    success: (r) => (r.status === "enabled" ? "送信を再開しました" : "送信を止めました"),
  });
  const remove = useWrite({
    fn: () => api.del(`/v1/console/webhook-endpoints/${endpointId}`),
    invalidate: [["endpoints"]],
    success: "送信先を削除しました",
    onSuccess: () => navigate(`/t/${tenantId}/webhooks`),
  });
  const label = (v: string) => eventTypes.find((x) => x.value === v)?.label ?? v;
  return (
    <>
      <Anchor component={Link} to={`/t/${tenantId}/webhooks`} size="sm" c="dimmed">
        <Group gap={4}>
          <IconArrowLeft size={14} /> Webhook
        </Group>
      </Anchor>
      <ErrorBanner error={ep.error} onRetry={() => void ep.refetch()} />
      {!e ? (
        <Skeleton h={120} mt="md" />
      ) : (
        <Stack gap={0} mt="sm">
          <Group justify="space-between" mb="lg" wrap="nowrap">
            <div style={{ minWidth: 0 }}>
              <Group gap="sm" wrap="nowrap">
                <Title order={1} ff="monospace" fz={20} style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                  {e.url}
                </Title>
                <StatusBadge status={e.status} />
              </Group>
              {e.description && (
                <Text size="sm" c="dimmed" mt={4}>
                  {e.description}
                </Text>
              )}
            </div>
            <Group gap="xs" wrap="nowrap">
              <Button variant="default" leftSection={<IconPencil size={16} />} onClick={edit.open} disabled={!can.allowed} title={can.reason ?? undefined}>
                編集
              </Button>
              <Menu position="bottom-end" withinPortal>
                <Menu.Target>
                  <Button variant="default" px="xs" aria-label="その他の操作" disabled={!can.allowed} title={can.reason ?? undefined}>
                    <IconDots size={16} />
                  </Button>
                </Menu.Target>
                <Menu.Dropdown>
                  {e.status === "enabled" ? (
                    <Menu.Item leftSection={<IconPlayerPause size={14} />} onClick={() => toggle.mutate(false)}>
                      送信を止める
                    </Menu.Item>
                  ) : (
                    <Menu.Item leftSection={<IconPlayerPlay size={14} />} onClick={() => toggle.mutate(true)}>
                      送信を再開する
                    </Menu.Item>
                  )}
                  <Menu.Item leftSection={<IconKey size={14} />} onClick={rotate.open}>
                    署名鍵をローテーション
                  </Menu.Item>
                  <Menu.Divider />
                  <Menu.Item
                    color="red"
                    leftSection={<IconTrash size={14} />}
                    onClick={() =>
                      confirmDanger({
                        title: "送信先を削除しますか？",
                        message: "削除すると、この URL への送信は止まり、元に戻せません。配信の履歴は残ります。",
                        confirm: "削除",
                        onConfirm: () => remove.mutate(undefined),
                      })
                    }
                  >
                    削除
                  </Menu.Item>
                </Menu.Dropdown>
              </Menu>
            </Group>
          </Group>
          <Section title="送信先の情報">
            <KeyValue
              items={[
                { label: "送信先 ID", value: <CopyText value={e.id} /> },
                { label: "API のバージョン", value: e.api_version },
                { label: "署名鍵の更新日時", value: dateTime(e.secret_rotated_at ?? e.created_at) },
                { label: "古い署名鍵の期限", value: e.previous_secret_expires_at ? dateTime(e.previous_secret_expires_at) : "なし" },
                {
                  label: `送るイベント（${e.event_types.length} 種類）`,
                  value: (
                    <Stack gap={2}>
                      {e.event_types.map((t) => (
                        <Text key={t} size="sm">
                          {label(t)}{" "}
                          <Text span size="xs" c="dimmed" ff="monospace">
                            {t}
                          </Text>
                        </Text>
                      ))}
                    </Stack>
                  ),
                },
              ]}
            />
          </Section>
          <Title order={3} mb="sm">
            配信の履歴
          </Title>
          <DeliveriesTable endpointId={e.id} />
          <EditEndpointDrawer endpoint={e} opened={editing} onClose={edit.close} />
          <RotateModal endpoint={e} opened={rotating} onClose={rotate.close} />
        </Stack>
      )}
    </>
  );
}

function EditEndpointDrawer({ endpoint, opened, onClose }: { endpoint: Endpoint; opened: boolean; onClose: () => void }) {
  const api = useApi();
  const form = useForm({
    initialValues: { url: endpoint.url, description: endpoint.description, event_types: endpoint.event_types },
    validate: {
      url: (v) => (/^https?:\/\/.+/.test(v.trim()) ? null : "URL を入力してください"),
      event_types: (v) => (v.length ? null : "1 つ以上選んでください"),
    },
  });
  const save = useWrite({
    fn: (v: typeof form.values) =>
      api.patch<Endpoint>(`/v1/console/webhook-endpoints/${endpoint.id}`, { url: v.url.trim(), description: v.description.trim(), event_types: v.event_types }, endpoint.version),
    invalidate: [["endpoint"], ["endpoints"]],
    success: "送信先を更新しました",
    form,
    onSuccess: onClose,
  });
  return (
    <Drawer opened={opened} onClose={onClose} title="送信先を編集" position="right">
      <form onSubmit={form.onSubmit((v) => save.mutate(v))}>
        <Stack>
          <TextInput label="URL" withAsterisk {...form.getInputProps("url")} />
          <TextInput label="説明" {...form.getInputProps("description")} />
          <EventTypePicker value={form.values.event_types} onChange={(v) => form.setFieldValue("event_types", v)} error={form.errors.event_types} />
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

function RotateModal({ endpoint, opened, onClose }: { endpoint: Endpoint; opened: boolean; onClose: () => void }) {
  const api = useApi();
  const [grace, setGrace] = useState("24");
  const [secret, setSecret] = useState<string | null>(null);
  const rotate = useWrite({
    fn: () => api.post<{ secret: string }>(`/v1/console/webhook-endpoints/${endpoint.id}/rotate-secret`, { grace_period_hours: Number(grace) }),
    invalidate: [["endpoint"]],
    success: "署名鍵をローテーションしました",
    onSuccess: (r) => setSecret(r.secret),
  });
  const close = () => (setSecret(null), onClose());
  return (
    <Modal opened={opened} onClose={close} title="署名鍵をローテーション" closeOnClickOutside={!secret} size="lg">
      {secret ? (
        <Stack>
          <SecretReveal label="新しい署名鍵" value={secret} />
          <Group justify="flex-end">
            <Button onClick={close}>控えました</Button>
          </Group>
        </Stack>
      ) : (
        <Stack>
          <Text size="sm">新しい署名鍵を作ります。古い鍵は、指定した猶予の間だけ署名に併用されるので、その間に自社のシステムの設定を切り替えてください。</Text>
          <Select
            label="古い署名鍵の猶予"
            value={grace}
            onChange={(v) => setGrace(v ?? "24")}
            allowDeselect={false}
            data={[
              { value: "0", label: "すぐに無効にする" },
              { value: "1", label: "1 時間" },
              { value: "24", label: "24 時間" },
              { value: "72", label: "3 日" },
              { value: "168", label: "7 日" },
            ]}
          />
          <Group justify="flex-end">
            <Button variant="default" onClick={close}>
              キャンセル
            </Button>
            <Button color="red" loading={rotate.isPending} onClick={() => rotate.mutate(undefined)}>
              ローテーション
            </Button>
          </Group>
        </Stack>
      )}
    </Modal>
  );
}
