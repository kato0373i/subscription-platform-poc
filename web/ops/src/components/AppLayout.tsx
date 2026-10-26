// レイアウト（operator-console §7）：左に固定のサイドバー（主要メニュー）、上部に薄いヘッダー（ユーザーメニュー）。PC 前提。
import { AppShell, Avatar, Box, Container, Group, Menu, NavLink, ScrollArea, Text, UnstyledButton } from "@mantine/core";
import { IconBuildingStore, IconCalendarRepeat, IconChevronDown, IconFileSearch, IconHome, IconLogout, IconQuestionMark, IconScale } from "@tabler/icons-react";
import { NavLink as RouterLink, Outlet } from "react-router";
import { useAuth } from "../auth/AuthProvider.tsx";

const nav = [
  { to: "/", label: "ホーム", icon: IconHome, end: true },
  { to: "/tenants", label: "事業者", icon: IconBuildingStore },
  { to: "/payments/attempts", label: "結果が不明な決済", icon: IconQuestionMark },
  { to: "/payments/reconciliation", label: "照合の不整合", icon: IconScale },
  { to: "/billing-runs", label: "更新処理", icon: IconCalendarRepeat },
  { to: "/audit", label: "監査ログ", icon: IconFileSearch },
];

export function AppLayout() {
  const auth = useAuth();
  return (
    <AppShell navbar={{ width: 240, breakpoint: 0 }} header={{ height: 52 }} padding="xl" bg="#f6f8fa">
      <AppShell.Header px="lg" style={{ borderBottom: "1px solid var(--mantine-color-gray-2)" }}>
        <Group h="100%" justify="space-between">
          <Group gap={8} wrap="nowrap">
            <Box w={22} h={22} bg="dark.6" style={{ borderRadius: 6 }} />
            <Text fw={700} size="sm">
              運営者コンソール
            </Text>
          </Group>
          <Menu position="bottom-end" width={220}>
            <Menu.Target>
              <UnstyledButton data-testid="user-menu">
                <Group gap={6}>
                  <Avatar size={26} radius="xl" color="gray">
                    {(auth.email ?? "?").slice(0, 1).toUpperCase()}
                  </Avatar>
                  <Text size="sm">{auth.email}</Text>
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
        <ScrollArea style={{ flex: 1 }}>
          {nav.map((i) => (
            <NavLink
              key={i.to}
              component={RouterLink}
              to={i.to}
              end={i.end}
              label={i.label}
              leftSection={<i.icon size={18} stroke={1.6} />}
              style={{ borderRadius: 6 }}
              fw={500}
            />
          ))}
        </ScrollArea>
      </AppShell.Navbar>
      <AppShell.Main>
        <Container size={1200} px={0}>
          <Outlet />
        </Container>
      </AppShell.Main>
    </AppShell>
  );
}
