// レイアウト（設計 §5.6）：左に固定のサイドバー（事業者の切り替え・主要メニュー・設定）、上部に薄いヘッダー（ユーザーメニュー）。
// 幅が狭いとき（sm 未満）はサイドバーを隠し、ヘッダーのメニューボタンで開閉する（console-dashboard FR-008）。
import { Alert, AppShell, Avatar, Box, Burger, Container, Group, Menu, NavLink, ScrollArea, Text, UnstyledButton } from "@mantine/core";
import { useDisclosure } from "@mantine/hooks";
import { IconBox, IconChartAreaLine, IconChevronDown, IconCreditCard, IconKey, IconLogout, IconReceiptRefund, IconSelector, IconTicket, IconUsers, IconWebhook } from "@tabler/icons-react";
import { NavLink as RouterLink, Outlet, useNavigate, useParams } from "react-router";
import { useAuth } from "../auth/AuthProvider.tsx";
import { useMemberships, useTenant } from "../api/tenant.tsx";
import { StatusBadge } from "./ui.tsx";

const roleLabel: Record<string, string> = { owner: "オーナー", admin: "管理者", viewer: "閲覧者" };

function TenantSwitcher() {
  const t = useTenant();
  const { data } = useMemberships();
  const navigate = useNavigate();
  return (
    <Menu width={260} position="bottom-start">
      <Menu.Target>
        <UnstyledButton p="sm" w="100%" style={{ borderRadius: 8 }} data-testid="tenant-switcher">
          <Group justify="space-between" wrap="nowrap">
            <Group gap="sm" wrap="nowrap">
              <Avatar color="brand" radius="md" size={32}>
                {t.tenant_name.slice(0, 1)}
              </Avatar>
              <Box style={{ minWidth: 0 }}>
                <Text fw={600} size="sm" truncate>
                  {t.tenant_name}
                </Text>
                <Text size="xs" c="dimmed">
                  {roleLabel[t.role]}
                </Text>
              </Box>
            </Group>
            <IconSelector size={16} color="var(--mantine-color-gray-6)" />
          </Group>
        </UnstyledButton>
      </Menu.Target>
      <Menu.Dropdown>
        <Menu.Label>事業者を切り替える</Menu.Label>
        {data?.map((m) => (
          <Menu.Item key={m.tenant_id} onClick={() => navigate(`/t/${m.tenant_id}/dashboard`)} rightSection={<Text size="xs" c="dimmed">{roleLabel[m.role]}</Text>}>
            {m.tenant_name}
          </Menu.Item>
        ))}
      </Menu.Dropdown>
    </Menu>
  );
}

const nav = [
  { to: "dashboard", label: "ダッシュボード", icon: IconChartAreaLine },
  { to: "subscriptions", label: "契約", icon: IconReceiptRefund },
  { to: "products", label: "商品・プラン", icon: IconBox },
  { to: "coupons", label: "クーポン", icon: IconTicket },
  { to: "webhooks", label: "Webhook", icon: IconWebhook },
];
const settings = [
  { to: "settings/dunning", label: "リトライ方針", icon: IconCreditCard },
  { to: "settings/api-keys", label: "API キー", icon: IconKey },
  { to: "settings/staff", label: "スタッフ", icon: IconUsers },
];

export function AppLayout() {
  const { tenantId } = useParams();
  const t = useTenant();
  const auth = useAuth();
  const [opened, { toggle, close }] = useDisclosure(false);
  const link = (i: (typeof nav)[number]) => (
    <NavLink
      key={i.to}
      component={RouterLink}
      to={`/t/${tenantId}/${i.to}`}
      label={i.label}
      leftSection={<i.icon size={18} stroke={1.6} />}
      style={{ borderRadius: 6 }}
      fw={500}
      onClick={close}
    />
  );
  return (
    <AppShell navbar={{ width: 240, breakpoint: "sm", collapsed: { mobile: !opened } }} header={{ height: 52 }} padding={{ base: "md", sm: "xl" }} bg="#f6f8fa">
      <AppShell.Header px={{ base: "md", sm: "lg" }} style={{ borderBottom: "1px solid var(--mantine-color-gray-2)" }}>
        <Group h="100%" justify="space-between">
          <Group gap={8} wrap="nowrap">
            <Burger opened={opened} onClick={toggle} hiddenFrom="sm" size="sm" aria-label="メニュー" data-testid="nav-toggle" />
            <Box w={22} h={22} bg="brand.6" style={{ borderRadius: 6 }} />
            <Text fw={700} size="sm">
              事業者コンソール
            </Text>
          </Group>
          <Menu position="bottom-end" width={220}>
            <Menu.Target>
              <UnstyledButton data-testid="user-menu">
                <Group gap={6}>
                  <Avatar size={26} radius="xl" color="gray">
                    {(auth.email ?? "?").slice(0, 1).toUpperCase()}
                  </Avatar>
                  <Text size="sm" visibleFrom="sm">
                    {auth.email}
                  </Text>
                  <IconChevronDown size={14} />
                </Group>
              </UnstyledButton>
            </Menu.Target>
            <Menu.Dropdown>
              <Menu.Item leftSection={<IconLogout size={16} />} onClick={() => void auth.logout("manual")}>
                ログアウト
              </Menu.Item>
            </Menu.Dropdown>
          </Menu>
        </Group>
      </AppShell.Header>
      <AppShell.Navbar p="sm" bg="white">
        <TenantSwitcher />
        <ScrollArea mt="sm" style={{ flex: 1 }}>
          {nav.map(link)}
          <Text size="xs" c="dimmed" fw={600} mt="lg" mb={4} px="sm">
            設定
          </Text>
          {settings.map(link)}
        </ScrollArea>
      </AppShell.Navbar>
      <AppShell.Main>
        <Container size={1200} px={0}>
          {t.tenant_status === "suspended" && (
            <Alert color="red" variant="light" mb="lg" data-testid="suspended-banner">
              この事業者は停止されています。閲覧のみ可能です。
            </Alert>
          )}
          {t.tenant_status === "pending_review" && (
            <Alert color="yellow" variant="light" mb="lg" icon={<StatusBadge status="pending_review" />}>
              事業者の審査中です。承認後にプランを公開できます。
            </Alert>
          )}
          <Outlet />
        </Container>
      </AppShell.Main>
    </AppShell>
  );
}
