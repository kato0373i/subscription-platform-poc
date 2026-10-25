// 事業者の選択（FR-004）：1 つなら自動で開き、複数なら選ぶ。最後に選んだ事業者を次回の既定にする。
import { Alert, Avatar, Button, Center, Group, Paper, Stack, Text, Title, UnstyledButton } from "@mantine/core";
import { IconChevronRight } from "@tabler/icons-react";
import { Navigate, useNavigate, useSearchParams } from "react-router";
import { useAuth } from "../auth/AuthProvider.tsx";
import { useMemberships } from "../api/tenant.tsx";
import { ErrorBanner, StatusBadge } from "../components/ui.tsx";
import { FullLoader, LAST_TENANT_KEY } from "../components/guards.tsx";

const roleLabel: Record<string, string> = { owner: "オーナー", admin: "管理者", viewer: "閲覧者" };

export function TenantSelectPage() {
  const { data, isLoading, error, refetch } = useMemberships();
  const auth = useAuth();
  const navigate = useNavigate();
  const [params] = useSearchParams();
  if (isLoading) return <FullLoader />;
  const lost = params.get("lost") === "1";
  let last: string | null = null;
  try {
    last = localStorage.getItem(LAST_TENANT_KEY);
  } catch {
    last = null;
  }
  if (!lost && data) {
    if (data.length === 1) return <Navigate to={`/t/${data[0]!.tenant_id}/subscriptions`} replace />;
    const prev = data.find((m) => m.tenant_id === last);
    if (prev && !params.has("choose")) return <Navigate to={`/t/${prev.tenant_id}/subscriptions`} replace />;
  }
  return (
    <Center mih="100vh" bg="#f6f8fa" p="md">
      <Stack w={460}>
        <Text fw={700} size="lg" c="brand.6" ta="center">
          事業者コンソール
        </Text>
        <Paper p="xl" shadow="xs">
          <Title order={2} mb="xs">
            事業者を選ぶ
          </Title>
          <Text size="sm" c="dimmed" mb="lg">
            {auth.email}
          </Text>
          {error && <ErrorBanner error={error} onRetry={() => void refetch()} />}
          {lost && <Alert color="yellow" variant="light" mb="md">この事業者へのアクセス権がなくなりました。</Alert>}
          {data && data.length === 0 && (
            <Alert color="gray" variant="light" mb="md" data-testid="no-membership">
              所属している事業者がありません。事業者のオーナーにお問い合わせください。
            </Alert>
          )}
          <Stack gap="xs">
            {data?.map((m) => (
              <UnstyledButton key={m.tenant_id} onClick={() => navigate(`/t/${m.tenant_id}/subscriptions`)} p="sm" style={{ border: "1px solid var(--mantine-color-gray-3)", borderRadius: 8 }}>
                <Group justify="space-between" wrap="nowrap">
                  <Group wrap="nowrap">
                    <Avatar color="brand" radius="md">
                      {m.tenant_name.slice(0, 1)}
                    </Avatar>
                    <div>
                      <Text fw={600} size="sm">
                        {m.tenant_name}
                      </Text>
                      <Text size="xs" c="dimmed">
                        {roleLabel[m.role]}
                      </Text>
                    </div>
                  </Group>
                  <Group gap="xs" wrap="nowrap">
                    {m.tenant_status !== "active" && <StatusBadge status={m.tenant_status} />}
                    <IconChevronRight size={16} />
                  </Group>
                </Group>
              </UnstyledButton>
            ))}
          </Stack>
          <Button variant="subtle" color="gray" mt="lg" fullWidth onClick={() => void auth.logout("manual")}>
            ログアウト
          </Button>
        </Paper>
      </Stack>
    </Center>
  );
}
