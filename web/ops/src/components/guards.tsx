// ルートのガード：ログイン済みか、グループ operators か（FR-001。境界は API の 403、ここは使いやすさのため）。
import { Button, Center, Loader, Stack, Text, Title } from "@mantine/core";
import { Navigate, Outlet, useLocation } from "react-router";
import { useAuth } from "../auth/AuthProvider.tsx";

export function FullLoader() {
  return (
    <Center h="100vh">
      <Loader />
    </Center>
  );
}

export function RequireAuth() {
  const auth = useAuth();
  const loc = useLocation();
  if (auth.status === "loading") return <FullLoader />;
  if (auth.status === "signedOut") {
    const reason = auth.lastLogoutReason && auth.lastLogoutReason !== "manual" ? `?reason=${auth.lastLogoutReason}` : "";
    return <Navigate to={`/login${reason}`} replace state={{ from: loc.pathname }} />;
  }
  if (!auth.isOperator) return <NotOperator />;
  return <Outlet />;
}

function NotOperator() {
  const auth = useAuth();
  return (
    <Center h="100vh" bg="#f6f8fa">
      <Stack align="center" gap="xs" data-testid="not-operator">
        <Title order={2}>運営者の権限がありません</Title>
        <Text c="dimmed" size="sm">
          {auth.email} は運営者として登録されていません。インフラの管理者に確認してください。
        </Text>
        <Button variant="light" mt="sm" onClick={() => void auth.logout("manual")}>
          ログアウト
        </Button>
      </Stack>
    </Center>
  );
}
