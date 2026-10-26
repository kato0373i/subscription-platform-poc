// ログインの後の行き先（FR-003・FR-011）。申込ページに戻るときは、既存の受け取り方（#token=）で ID トークンを渡す。
import { useNavigate, useSearchParams } from "react-router";
import { safeReturn, useAuth } from "../../auth/AuthProvider.tsx";

export function useFinishLogin() {
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const auth = useAuth();
  const ret = safeReturn(params.get("return"));
  return {
    ret,
    /** ログインの画面の間を移るときに return を引き継ぐ */
    query: ret ? `?return=${encodeURIComponent(ret)}` : "",
    async finish() {
      if (ret?.startsWith("/checkout")) {
        const t = await auth.idToken();
        window.location.assign(`${ret}#token=${encodeURIComponent(t)}`);
        return;
      }
      navigate(ret ? ret.replace(/^\/mypage/, "") || "/" : "/", { replace: true });
    },
  };
}
