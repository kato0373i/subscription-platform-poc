// ルートのガード：ログイン済みか、URL の事業者に所属しているか（FR-004）。
import { Center, Loader } from "@mantine/core";
import { Navigate, Outlet, useLocation, useParams } from "react-router";
import { useAuth } from "../auth/AuthProvider.tsx";
import { TenantProvider, useMemberships } from "../api/tenant.tsx";
import { ErrorBanner } from "./ui.tsx";

export const LAST_TENANT_KEY = "sp.console.lastTenant";

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
  return <Outlet />;
}

export function RequireTenant() {
  const { tenantId } = useParams();
  const { data, isLoading, error, refetch } = useMemberships();
  if (isLoading) return <FullLoader />;
  if (error) return <ErrorBanner error={error} onRetry={() => void refetch()} />;
  const m = data?.find((x) => x.tenant_id === tenantId);
  if (!m) return <Navigate to="/tenants?lost=1" replace />;
  try {
    localStorage.setItem(LAST_TENANT_KEY, m.tenant_id);
  } catch {
    /* 保存できなくても使える */
  }
  return (
    <TenantProvider membership={m}>
      <Outlet />
    </TenantProvider>
  );
}
