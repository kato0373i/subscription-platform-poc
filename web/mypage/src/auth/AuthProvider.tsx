// 認証（consumer-mypage §5.4、事業者コンソールの ADR-C01・C07 と同じ方式）：Amplify Auth（SRP）で Cognito の契約者のプールにログインする。
// 登録（確認コード）・パスワードの再設定と変更・ログアウト・アカウントの削除（退会。ADR-M03）。ログインの状態はリフレッシュトークンで 30 日保つ。
import { Amplify } from "aws-amplify";
import {
  autoSignIn,
  confirmResetPassword,
  confirmSignUp,
  deleteUser,
  fetchAuthSession,
  getCurrentUser,
  resendSignUpCode,
  resetPassword,
  signIn,
  signOut,
  signUp,
  updatePassword,
} from "aws-amplify/auth";
import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import type { MypageConfig } from "../config.ts";

type Status = "loading" | "signedOut" | "signedIn";
/** ログアウトの理由（ログインの画面の案内に使う）。 */
export type LogoutReason = "logout" | "withdrawn";

interface AuthContextValue {
  status: Status;
  email: string | null;
  lastLogout: LogoutReason | null;
  login(email: string, password: string): Promise<"done" | "confirm">;
  register(email: string, password: string, name: string): Promise<void>;
  confirmRegistration(email: string, code: string): Promise<void>;
  resendCode(email: string): Promise<void>;
  logout(reason?: LogoutReason): Promise<void>;
  requestReset(email: string): Promise<void>;
  confirmReset(email: string, code: string, password: string): Promise<void>;
  changePassword(oldPassword: string, newPassword: string): Promise<void>;
  deleteAccount(): Promise<void>;
  idToken(): Promise<string>;
}

const AuthContext = createContext<AuthContextValue | null>(null);

export function configureAmplify(cfg: MypageConfig) {
  Amplify.configure({
    Auth: { Cognito: { userPoolId: cfg.cognito.userPoolId, userPoolClientId: cfg.cognito.clientId, ...(cfg.cognito.endpoint ? { userPoolEndpoint: cfg.cognito.endpoint } : {}) } },
  });
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [status, setStatus] = useState<Status>("loading");
  const [email, setEmail] = useState<string | null>(null);
  const [lastLogout, setLastLogout] = useState<LogoutReason | null>(null);

  const restore = useCallback(async () => {
    try {
      const u = await getCurrentUser();
      const s = await fetchAuthSession();
      if (!s.tokens?.idToken) throw new Error("no session");
      setEmail((s.tokens.idToken.payload.email as string | undefined) ?? u.signInDetails?.loginId ?? u.username);
      setStatus("signedIn");
    } catch {
      setStatus("signedOut");
    }
  }, []);

  useEffect(() => {
    void restore();
  }, [restore]);

  const logout = useCallback(async (reason?: LogoutReason) => {
    try {
      await signOut({ global: true }); // リフレッシュトークンも失効させる
    } catch {
      /* 期限切れなどで global が失敗しても、下でこのブラウザの認証情報は消す */
    }
    await signOut().catch(() => undefined);
    setEmail(null);
    setLastLogout(reason ?? null);
    setStatus("signedOut");
  }, []);

  const value = useMemo<AuthContextValue>(
    () => ({
      status,
      email,
      lastLogout,
      async login(mail, password) {
        const r = await signIn({ username: mail.trim(), password, options: { authFlowType: "USER_SRP_AUTH" } });
        if (r.nextStep.signInStep === "CONFIRM_SIGN_UP") return "confirm"; // 登録の確認コードが未入力
        if (!r.isSignedIn) throw new Error(`unsupported sign-in step: ${r.nextStep.signInStep}`);
        setEmail(mail.trim());
        setLastLogout(null);
        setStatus("signedIn");
        return "done";
      },
      async register(mail, password, name) {
        await signUp({
          username: mail.trim(),
          password,
          options: { userAttributes: { email: mail.trim(), name: name.trim() }, autoSignIn: { authFlowType: "USER_SRP_AUTH" } },
        });
      },
      async confirmRegistration(mail, code) {
        const r = await confirmSignUp({ username: mail.trim(), confirmationCode: code.trim() });
        if (r.nextStep.signUpStep === "COMPLETE_AUTO_SIGN_IN") {
          await autoSignIn();
          await restore();
        }
      },
      async resendCode(mail) {
        await resendSignUpCode({ username: mail.trim() });
      },
      logout,
      async requestReset(mail) {
        await resetPassword({ username: mail.trim() });
      },
      async confirmReset(mail, code, password) {
        await confirmResetPassword({ username: mail.trim(), confirmationCode: code.trim(), newPassword: password });
      },
      async changePassword(oldPassword, newPassword) {
        await updatePassword({ oldPassword, newPassword });
      },
      async deleteAccount() {
        await deleteUser(); // 本人のアクセストークンで自分を削除する（退会。ADR-M03）
        await signOut().catch(() => undefined);
        setEmail(null);
        setLastLogout("withdrawn");
        setStatus("signedOut");
      },
      async idToken() {
        const s = await fetchAuthSession(); // 期限が近ければリフレッシュトークンで更新する。30 日を過ぎると失敗する
        const t = s.tokens?.idToken?.toString();
        if (!t) {
          await logout();
          throw new Error("session expired");
        }
        return t;
      },
    }),
    [status, email, lastLogout, logout, restore],
  );
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextValue {
  const v = useContext(AuthContext);
  if (!v) throw new Error("useAuth outside AuthProvider");
  return v;
}

export const PASSWORD_HINT = "8 文字以上で、英字（小文字）と数字を含めてください";

/** 画面での事前の確認（Cognito の方針と同じ。FR-001）。 */
export function passwordProblem(pw: string): string | null {
  if (pw.length < 8) return "8 文字以上にしてください";
  if (!/[a-z]/.test(pw)) return "英字（小文字）を含めてください";
  if (!/[0-9]/.test(pw)) return "数字を含めてください";
  return null;
}

/** Cognito のエラーを画面の文言にする（ログイン・再設定ではアカウントの有無を明かさない。FR-001・FR-002）。 */
export function authErrorMessage(e: unknown): string {
  const name = (e as { name?: string })?.name ?? "";
  switch (name) {
    case "NotAuthorizedException":
    case "UserNotFoundException":
      return "メールアドレスまたはパスワードが正しくありません。";
    case "UsernameExistsException":
      return "このメールアドレスは登録済みです。ログインするか、パスワードを再設定してください。";
    case "LimitExceededException":
    case "TooManyRequestsException":
    case "TooManyFailedAttemptsException":
      return "しばらく時間をおいてお試しください。";
    case "InvalidPasswordException":
      return `パスワードは${PASSWORD_HINT}。`;
    case "CodeMismatchException":
    case "ExpiredCodeException":
      return "確認コードが正しくないか、期限が切れています。";
    case "EmptySignInUsername":
    case "EmptySignInPassword":
      return "メールアドレスとパスワードを入力してください。";
    default:
      return "処理に失敗しました。時間をおいて再度お試しください。";
  }
}

/** ログインの後に戻る先（同一オリジンの /mypage/ か /checkout だけ。オープンリダイレクトを防ぐ。設計 §5.3）。 */
export function safeReturn(raw: string | null): string | null {
  if (!raw || !raw.startsWith("/") || raw.startsWith("//")) return null;
  return raw.startsWith("/mypage/") || raw.startsWith("/checkout") ? raw : null;
}
