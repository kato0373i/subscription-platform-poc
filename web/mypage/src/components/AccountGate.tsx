// ログインした契約者のアカウントを確かめる（初回の呼び出しで契約者が作られる。退会済みなら案内の画面へ）。
import { Navigate, Outlet } from "react-router";
import { ApiError } from "../api/client.ts";
import { useMe } from "../api/queries.ts";
import { FullLoader } from "./guards.tsx";

export function AccountGate() {
  const me = useMe();
  if (me.isLoading) return <FullLoader />;
  if (me.error instanceof ApiError && me.error.code === "account_withdrawn") return <Navigate to="/withdrawn" replace />;
  return <Outlet />;
}
