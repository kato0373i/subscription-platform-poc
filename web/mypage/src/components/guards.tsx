// ログインしていなければログインの画面へ（開こうとした画面を ?return= で覚えておく。FR-003）。
import { Center, Loader } from "@mantine/core";
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
    if (auth.lastLogout) return <Navigate to={`/login?notice=${auth.lastLogout}`} replace />;
    const back = `/mypage${loc.pathname}${loc.search}`;
    return <Navigate to={`/login?return=${encodeURIComponent(back)}`} replace />;
  }
  return <Outlet />;
}
