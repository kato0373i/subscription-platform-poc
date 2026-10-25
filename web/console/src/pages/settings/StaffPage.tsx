// スタッフ（FR-006・FR-007）：招待・招待の再送・ロールの変更（If-Match）・削除。オーナーは最低 1 名。
import { Badge, Button, Drawer, Group, Menu, Radio, Stack, Text, TextInput } from "@mantine/core";
import { useForm } from "@mantine/form";
import { useDisclosure } from "@mantine/hooks";

import { IconDots, IconMail, IconTrash, IconUserPlus, IconUserCog } from "@tabler/icons-react";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useParams } from "react-router";
import { useApi } from "../../api/hooks.ts";
import { useCan } from "../../api/tenant.tsx";
import type { Page, Staff } from "../../api/types.ts";
import { useAuth } from "../../auth/AuthProvider.tsx";
import { ListTable, useCursorPager } from "../../components/ListTable.tsx";
import { CanButton, ErrorBanner, PageHeader, StatusBadge, confirmDanger } from "../../components/ui.tsx";
import { dateTime } from "../../lib/format.ts";
import { useWrite } from "../../lib/mutation.ts";

export const roleLabel: Record<string, string> = { owner: "オーナー", admin: "管理者", viewer: "閲覧者" };
const roleHelp: Record<string, string> = {
  owner: "すべての操作（スタッフ・API キー・Webhook の管理を含む）",
  admin: "商品・プラン・クーポン・リトライ方針の変更、Webhook の再送",
  viewer: "閲覧だけ",
};

function RoleRadios(props: { value: string; onChange: (v: string) => void }) {
  return (
    <Radio.Group label="ロール" {...props}>
      <Stack gap="xs" mt={6}>
        {(["owner", "admin", "viewer"] as const).map((r) => (
          <Radio key={r} value={r} label={roleLabel[r]} description={roleHelp[r]} />
        ))}
      </Stack>
    </Radio.Group>
  );
}

export function StaffPage() {
  const api = useApi();
  const auth = useAuth();
  const { tenantId } = useParams();
  const can = useCan("staff.manage");
  const pager = useCursorPager();
  const [inviting, invite] = useDisclosure(false);
  const [editing, setEditing] = useState<Staff | null>(null);
  const list = useQuery({
    queryKey: ["staff", tenantId, pager.cursor],
    queryFn: () => api.get<Page<Staff>>("/v1/console/staff", { limit: 50, starting_after: pager.cursor }),
  });
  const resend = useWrite({ fn: (s: Staff) => api.post<Staff>(`/v1/console/staff/${s.id}/resend-invitation`), invalidate: [["staff"]], success: "招待メールを再送しました" });
  const remove = useWrite({ fn: (s: Staff) => api.del(`/v1/console/staff/${s.id}`), invalidate: [["staff"]], success: "スタッフを削除しました" });
  const me = auth.email?.toLowerCase();
  return (
    <>
      <PageHeader
        title="スタッフ"
        description="このコンソールを使う人と、そのロールを管理します。"
        actions={
          <CanButton permission="staff.manage" leftSection={<IconUserPlus size={16} />} onClick={invite.open}>
            スタッフを招待
          </CanButton>
        }
      />
      <ErrorBanner error={list.error} onRetry={() => void list.refetch()} />
      <ListTable
        rows={list.data?.data}
        loading={list.isLoading}
        rowKey={(r) => r.id}
        columns={[
          {
            key: "name",
            header: "名前",
            render: (r) => (
              <div>
                <Group gap={6}>
                  <Text size="sm" fw={500}>
                    {r.display_name}
                  </Text>
                  {r.email.toLowerCase() === me && (
                    <Badge size="xs" variant="outline" color="gray">
                      自分
                    </Badge>
                  )}
                </Group>
                <Text size="xs" c="dimmed">
                  {r.email}
                </Text>
              </div>
            ),
          },
          { key: "role", header: "ロール", render: (r) => roleLabel[r.role] },
          {
            key: "status",
            header: "状態",
            render: (r) => (r.status === "invited" && r.invitation_expired ? <StatusBadge status="failed" label="招待の期限切れ" /> : <StatusBadge status={r.status === "active" ? "enabled" : "invited"} label={r.status === "active" ? "有効" : "招待中"} />),
          },
          { key: "seen", header: "最終利用", render: (r) => (r.status === "invited" ? `招待：${dateTime(r.invited_at)}` : dateTime(r.last_seen_at)) },
          {
            key: "actions",
            header: "",
            align: "right",
            render: (r) =>
              can.allowed && (
                <Menu position="bottom-end" withinPortal>
                  <Menu.Target>
                    <Button variant="subtle" color="gray" size="xs" px={6} aria-label={`${r.email} の操作`}>
                      <IconDots size={16} />
                    </Button>
                  </Menu.Target>
                  <Menu.Dropdown>
                    <Menu.Item leftSection={<IconUserCog size={14} />} onClick={() => setEditing(r)}>
                      ロールを変更
                    </Menu.Item>
                    {r.status === "invited" && (
                      <Menu.Item leftSection={<IconMail size={14} />} onClick={() => resend.mutate(r)}>
                        招待メールを再送
                      </Menu.Item>
                    )}
                    <Menu.Divider />
                    <Menu.Item
                      color="red"
                      leftSection={<IconTrash size={14} />}
                      onClick={() =>
                        confirmDanger({
                          title: `${r.display_name} を削除しますか？`,
                          message:
                            r.email.toLowerCase() === me
                              ? "自分を削除すると、この事業者のコンソールを使えなくなります。"
                              : "削除すると、この人はこの事業者のコンソールを使えなくなります（ほかの事業者への所属には影響しません）。",
                          confirm: "削除",
                          onConfirm: () => remove.mutate(r),
                        })
                      }
                    >
                      削除
                    </Menu.Item>
                  </Menu.Dropdown>
                </Menu>
              ),
          },
        ]}
        empty={{ title: "スタッフはいません" }}
        pager={{ page: pager.page, hasMore: !!list.data?.has_more, onNext: () => pager.next(list.data?.next_cursor ?? null), onPrev: pager.prev }}
      />
      <InviteDrawer opened={inviting} onClose={invite.close} />
      {editing && <RoleModal staff={editing} onClose={() => setEditing(null)} />}
    </>
  );
}

function InviteDrawer({ opened, onClose }: { opened: boolean; onClose: () => void }) {
  const api = useApi();
  const form = useForm({
    initialValues: { email: "", display_name: "", role: "viewer" },
    validate: {
      email: (v) => (/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(v.trim()) ? null : "メールアドレスの形式が正しくありません"),
      display_name: (v) => (v.trim() ? null : "入力してください"),
    },
  });
  const save = useWrite({
    fn: (v: typeof form.values) => api.post<Staff>("/v1/console/staff", { email: v.email.trim(), display_name: v.display_name.trim(), role: v.role }),
    invalidate: [["staff"]],
    success: (s) => (s.status === "invited" ? `${s.email} に招待メールを送りました` : `${s.email} を追加しました（通知メールを送ります）`),
    form,
    onSuccess: () => (form.reset(), onClose()),
  });
  return (
    <Drawer opened={opened} onClose={onClose} title="スタッフを招待" position="right">
      <form onSubmit={form.onSubmit((v) => save.mutate(v))}>
        <Stack>
          <TextInput label="メールアドレス" type="email" withAsterisk {...form.getInputProps("email")} />
          <TextInput label="名前" withAsterisk {...form.getInputProps("display_name")} />
          <RoleRadios value={form.values.role} onChange={(v) => form.setFieldValue("role", v)} />
          <Text size="xs" c="dimmed">
            招待メールに仮のパスワードが届きます（7 日間有効）。すでにアカウントがある人は、この事業者に追加され、お知らせのメールが届きます。
          </Text>
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>
              キャンセル
            </Button>
            <Button type="submit" loading={save.isPending}>
              招待
            </Button>
          </Group>
        </Stack>
      </form>
    </Drawer>
  );
}

function RoleModal({ staff, onClose }: { staff: Staff; onClose: () => void }) {
  const api = useApi();
  const [role, setRole] = useState<string>(staff.role);
  const save = useWrite({
    fn: () => api.patch<Staff>(`/v1/console/staff/${staff.id}`, { role }, staff.version),
    invalidate: [["staff"], ["memberships"]],
    success: "ロールを変更しました",
    onSuccess: onClose,
  });
  return (
    <Drawer opened onClose={onClose} title={`${staff.display_name} のロール`} position="right">
      <Stack>
        <RoleRadios value={role} onChange={setRole} />
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            キャンセル
          </Button>
          <Button loading={save.isPending} disabled={role === staff.role} onClick={() => save.mutate(undefined)}>
            変更
          </Button>
        </Group>
      </Stack>
    </Drawer>
  );
}
