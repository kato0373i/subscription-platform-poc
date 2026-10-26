// レイアウト（consumer-mypage §7）：上部に薄いヘッダー、スマートフォンでは下部のナビゲーション、PC では中央の 720px。
import { AppShell, Box, Container, Group, Text, UnstyledButton } from "@mantine/core";
import { IconCreditCard, IconFileText, IconUserCircle } from "@tabler/icons-react";
import { NavLink, Outlet, useLocation } from "react-router";

const nav = [
  { to: "/", label: "契約", icon: IconFileText, match: (p: string) => p === "/" || p.startsWith("/subscriptions") },
  { to: "/payment-methods", label: "支払方法", icon: IconCreditCard, match: (p: string) => p.startsWith("/payment-methods") },
  { to: "/account", label: "アカウント", icon: IconUserCircle, match: (p: string) => p.startsWith("/account") },
];

export function Layout() {
  const { pathname } = useLocation();
  return (
    <AppShell header={{ height: 52 }} footer={{ height: 60 }} padding={0} bg="#f6f8fa">
      <AppShell.Header px="md" style={{ borderBottom: "1px solid var(--mantine-color-gray-2)" }}>
        <Group h="100%" justify="space-between">
          <Group gap={8}>
            <Box w={22} h={22} bg="brand.6" style={{ borderRadius: 6 }} />
            <Text fw={700} size="sm">
              マイページ
            </Text>
          </Group>
        </Group>
      </AppShell.Header>
      <AppShell.Main>
        <Container size={720} px="md" py="md" pb={84}>
          <Outlet />
        </Container>
      </AppShell.Main>
      <AppShell.Footer style={{ borderTop: "1px solid var(--mantine-color-gray-2)" }}>
        <Group h="100%" grow gap={0} maw={720} mx="auto" component="nav" aria-label="メニュー">
          {nav.map((n) => {
            const active = n.match(pathname);
            return (
              <UnstyledButton key={n.to} component={NavLink} to={n.to} h="100%" aria-current={active ? "page" : undefined}>
                <Box ta="center" c={active ? "brand.6" : "gray.6"}>
                  <n.icon size={22} stroke={1.7} style={{ display: "block", margin: "0 auto" }} />
                  <Text size="xs" fw={active ? 700 : 500} mt={2}>
                    {n.label}
                  </Text>
                </Box>
              </UnstyledButton>
            );
          })}
        </Group>
      </AppShell.Footer>
    </AppShell>
  );
}
